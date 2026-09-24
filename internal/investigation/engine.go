// Package investigation owns the investigation workflow. The workflow is
// deterministic: the application decides which diagnostics run, in which
// order and with which inputs; the LLM only analyzes the evidence they
// produce and cannot request any Kubernetes call.
package investigation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"sort"
	"time"

	"k8s.io/apimachinery/pkg/labels"

	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/diagnostics"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/domain"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/kube"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/llm"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/observability"
)

var (
	// ErrBusy means all investigation slots are in use; retry later.
	ErrBusy = errors.New("too many concurrent investigations")
	// ErrInvalidOptions wraps problems with the caller's options or target.
	ErrInvalidOptions = errors.New("invalid investigation options")
	// ErrAnalysisFailed wraps failures after the investigation started. The
	// returned record still contains the stored executions and evidence.
	ErrAnalysisFailed = errors.New("investigation failed")
)

// Plan limits keep one investigation's API load and evidence size bounded.
const (
	maxLogPods      = 3
	maxConfigRefs   = 5
	maxEventSubject = 21
)

type Store interface {
	GetIncident(ctx context.Context, id string) (domain.Incident, error)
	CreateInvestigation(ctx context.Context, inv *domain.Investigation) error
	FinishInvestigation(ctx context.Context, rec domain.InvestigationRecord) error
}

// Clusters supplies the cluster for one investigation: the live cluster or
// a simulated one (kube.Live, simulation.Clusters).
type Clusters interface {
	For(target domain.Target, scenario string) (kube.Cluster, string, error)
}

type Config struct {
	Timeout       time.Duration // whole investigation
	ToolTimeout   time.Duration // each diagnostic
	MaxConcurrent int
	Policy        domain.TargetPolicy
}

type Engine struct {
	store    Store
	clusters Clusters
	provider llm.Provider
	log      *slog.Logger
	cfg      Config
	slots    chan struct{}
}

func NewEngine(store Store, clusters Clusters, provider llm.Provider, log *slog.Logger, cfg Config) *Engine {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 90 * time.Second
	}
	if cfg.ToolTimeout <= 0 {
		cfg.ToolTimeout = 15 * time.Second
	}
	initMetrics(provider.Name())
	return &Engine{store: store, clusters: clusters, provider: provider, log: log, cfg: cfg,
		slots: make(chan struct{}, max(cfg.MaxConcurrent, 1))}
}

type Options struct {
	// Scenario selects a simulation scenario; empty uses the default.
	Scenario string `json:"scenario"`
}

// Investigate runs the full workflow for one incident and returns everything
// it produced. On ErrAnalysisFailed the record is still returned.
func (e *Engine) Investigate(ctx context.Context, incidentID string, opts Options) (domain.InvestigationRecord, error) {
	select {
	case e.slots <- struct{}{}:
		defer func() { <-e.slots }()
	default:
		return domain.InvestigationRecord{}, ErrBusy
	}

	inc, err := e.store.GetIncident(ctx, incidentID)
	if err != nil {
		return domain.InvestigationRecord{}, fmt.Errorf("load incident: %w", err)
	}
	// Re-checked here: the policy may have changed since the incident was filed.
	if err := e.cfg.Policy.Check(inc.Target); err != nil {
		return domain.InvestigationRecord{}, fmt.Errorf("%w: %w", ErrInvalidOptions, err)
	}
	cluster, scenario, err := e.clusters.For(inc.Target, opts.Scenario)
	if err != nil {
		return domain.InvestigationRecord{}, fmt.Errorf("%w: %w", ErrInvalidOptions, err)
	}

	ctx, cancel := context.WithTimeout(ctx, e.cfg.Timeout)
	defer cancel()
	start := time.Now()

	rec := domain.InvestigationRecord{
		Investigation: domain.Investigation{IncidentID: inc.ID, Status: domain.InvestigationRunning, Scenario: scenario},
	}
	if err := e.store.CreateInvestigation(ctx, &rec.Investigation); err != nil {
		return domain.InvestigationRecord{}, fmt.Errorf("create investigation: %w", err)
	}
	log := e.log.With("incident_id", inc.ID, "investigation_id", rec.Investigation.ID)
	log.InfoContext(ctx, "investigation started", "scenario", scenario, "target", inc.Ref(), "namespace", inc.Namespace)

	r := &runner{engine: e, log: log, cluster: cluster, ns: inc.Namespace, rec: &rec}
	r.collect(ctx, inc.Target)
	analysisErr := e.analyze(ctx, log, inc, &rec)

	now := time.Now().UTC()
	rec.Investigation.CompletedAt = &now
	rec.Investigation.Status = domain.InvestigationCompleted
	if analysisErr != nil {
		rec.Investigation.Status = domain.InvestigationFailed
		rec.Investigation.Error = analysisErr.Error()
	}

	// Persist even if the caller went away or the deadline passed, so an
	// investigation never stays "running".
	saveCtx, saveCancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer saveCancel()
	if err := e.store.FinishInvestigation(saveCtx, rec); err != nil {
		log.ErrorContext(ctx, "persist investigation", "error", err)
		return rec, fmt.Errorf("persist investigation: %w", err)
	}

	duration := time.Since(start)
	observability.InvestigationsTotal.WithLabelValues(string(rec.Investigation.Status)).Inc()
	observability.InvestigationDuration.Observe(duration.Seconds())
	log.InfoContext(ctx, "investigation finished", "status", rec.Investigation.Status, "duration_ms", duration.Milliseconds(),
		"tool_executions", len(rec.ToolExecutions), "evidence", len(rec.Evidence))

	if analysisErr != nil {
		return rec, fmt.Errorf("%w: %w", ErrAnalysisFailed, analysisErr)
	}
	return rec, nil
}

// runner executes one investigation's plan and accumulates the record.
type runner struct {
	engine  *Engine
	log     *slog.Logger
	cluster kube.Cluster
	ns      string
	rec     *domain.InvestigationRecord
}

// collect is the diagnostic plan. Each step's input is derived from earlier
// results (selector from the Deployment, containers from the pods, ConfigMap
// names from the pod spec), never from model output.
func (r *runner) collect(ctx context.Context, t domain.Target) {
	if res, ok := r.run(ctx, diagnostics.GetNamespace, diagnostics.Input{}); ok && res.Health == domain.Down {
		return
	}

	var selector string
	var podLabels map[string]string
	var spec *diagnostics.PodSpecSummary
	var pods diagnostics.PodsInfo

	switch t.ResourceType {
	case domain.ResourceDeployment:
		res, ok := r.run(ctx, diagnostics.GetDeployment, diagnostics.Input{Name: t.ResourceName})
		if !ok || slices.Contains(res.Signals, diagnostics.SignalWorkloadNotFound) {
			return
		}
		d := res.Data.(diagnostics.DeploymentInfo)
		selector, podLabels, spec = d.Selector, d.PodLabels, &d.Template
	case domain.ResourceService:
		res, ok := r.run(ctx, diagnostics.GetService, diagnostics.Input{Name: t.ResourceName})
		if !ok || slices.Contains(res.Signals, diagnostics.SignalServiceNotFound) {
			return
		}
		if svcs := res.Data.(diagnostics.ServicesInfo).Services; len(svcs) > 0 && len(svcs[0].Selector) > 0 {
			podLabels = svcs[0].Selector
			selector = labels.SelectorFromSet(podLabels).String()
		}
	case domain.ResourcePod:
		res, ok := r.run(ctx, diagnostics.GetPods, diagnostics.Input{Name: t.ResourceName})
		if !ok || slices.Contains(res.Signals, diagnostics.SignalWorkloadNotFound) {
			return
		}
		pods = res.Data.(diagnostics.PodsInfo)
		if len(pods.Pods) > 0 {
			podLabels = maps.Clone(pods.Pods[0].Labels)
			delete(podLabels, "pod-template-hash")
		}
	}

	if t.ResourceType != domain.ResourcePod && selector != "" {
		if res, ok := r.run(ctx, diagnostics.GetPods, diagnostics.Input{LabelSelector: selector}); ok {
			pods = res.Data.(diagnostics.PodsInfo)
		}
	}
	if spec == nil {
		spec = pods.Spec
	}
	podNames := make([]string, 0, len(pods.Pods))
	for _, p := range pods.Pods {
		podNames = append(podNames, p.Name)
	}

	subjects := append([]string{t.ResourceName}, podNames...)
	r.run(ctx, diagnostics.GetEvents, diagnostics.Input{Names: subjects[:min(len(subjects), maxEventSubject)]})

	for _, p := range logPods(pods.Pods) {
		containers := make([]string, 0, len(p.Containers))
		for _, c := range p.Containers {
			containers = append(containers, c.Name)
		}
		r.run(ctx, diagnostics.GetPodLogs, diagnostics.Input{Name: p.Name, Names: containers})
	}

	if t.ResourceType != domain.ResourceService && len(podLabels) > 0 {
		r.run(ctx, diagnostics.GetService, diagnostics.Input{Name: t.ResourceName, Labels: podLabels})
	}
	if spec != nil {
		for _, cm := range spec.ConfigMaps[:min(len(spec.ConfigMaps), maxConfigRefs)] {
			r.run(ctx, diagnostics.GetConfigMap, diagnostics.Input{Name: cm})
		}
		for _, s := range spec.Secrets[:min(len(spec.Secrets), maxConfigRefs)] {
			r.run(ctx, diagnostics.GetSecretMetadata, diagnostics.Input{Name: s})
		}
	}
	if len(podLabels) > 0 {
		r.run(ctx, diagnostics.GetNetworkPolicies, diagnostics.Input{Labels: podLabels})
	}
	if len(podNames) > 0 {
		r.run(ctx, diagnostics.GetResourceUsage, diagnostics.Input{Names: podNames})
	}
}

// logPods picks the pods whose logs are read: pods with findings first,
// then the rest, at most maxLogPods.
func logPods(pods []diagnostics.PodInfo) []diagnostics.PodInfo {
	sorted := slices.Clone(pods)
	sort.SliceStable(sorted, func(i, j int) bool { return len(sorted[i].Signals) > 0 && len(sorted[j].Signals) == 0 })
	return sorted[:min(len(sorted), maxLogPods)]
}

// run executes one allowlisted tool, records the execution and, on success,
// the evidence. ok is false when the tool could not observe anything.
func (r *runner) run(ctx context.Context, tool string, in diagnostics.Input) (diagnostics.Result, bool) {
	in.Namespace = r.ns
	input, _ := json.Marshal(in) // Input always marshals
	te := domain.ToolExecution{ID: domain.NewID(), ToolName: tool, Input: input, StartedAt: time.Now().UTC()}

	toolCtx, cancel := context.WithTimeout(ctx, r.engine.cfg.ToolTimeout)
	res, err := diagnostics.Run(toolCtx, tool, r.cluster, in)
	cancel()
	te.DurationMs = time.Since(te.StartedAt).Milliseconds()

	var output []byte
	if err == nil {
		output, err = json.Marshal(res.Data)
	}
	if err != nil {
		te.Status, te.Error = domain.ToolFailed, err.Error()
		r.rec.ToolExecutions = append(r.rec.ToolExecutions, te)
		observability.ToolFailuresTotal.WithLabelValues(tool).Inc()
		r.log.WarnContext(ctx, "diagnostic failed", "tool_name", tool, "duration_ms", te.DurationMs, "error", err)
		return diagnostics.Result{}, false
	}
	te.Status, te.Output = domain.ToolSucceeded, output
	r.rec.ToolExecutions = append(r.rec.ToolExecutions, te)
	r.rec.Evidence = append(r.rec.Evidence, domain.Evidence{
		ID: domain.NewID(), ToolExecutionID: te.ID, Source: tool, Subject: res.Subject,
		Health: res.Health, Summary: res.Summary, Signals: res.Signals, Data: output,
	})
	observability.ToolExecutionsTotal.WithLabelValues(tool, string(res.Health)).Inc()
	r.log.InfoContext(ctx, "diagnostic completed", "tool_name", tool, "duration_ms", te.DurationMs,
		"health", res.Health, "signals", res.Signals)
	return res, true
}

// analyze calls the provider and validates its output. Only a valid analysis
// becomes a report.
func (e *Engine) analyze(ctx context.Context, log *slog.Logger, inc domain.Incident, rec *domain.InvestigationRecord) error {
	if len(rec.Evidence) == 0 {
		return errors.New("no diagnostic evidence collected; every tool failed")
	}
	provider := e.provider.Name()
	start := time.Now()
	analysis, err := e.provider.Analyze(ctx, llm.AnalysisInput{Incident: inc, Evidence: rec.Evidence})
	observability.LLMRequestDuration.WithLabelValues(provider).Observe(time.Since(start).Seconds())
	if err == nil {
		sources := make([]string, len(rec.Evidence))
		for i, ev := range rec.Evidence {
			sources[i] = ev.Source
		}
		if verr := analysis.Validate(sources); verr != nil {
			err = fmt.Errorf("%w: %w", llm.ErrInvalidOutput, verr)
		}
	}
	if err != nil {
		reason := "request"
		if errors.Is(err, llm.ErrInvalidOutput) {
			reason = "invalid_output"
		}
		observability.LLMRequestsTotal.WithLabelValues(provider, "error").Inc()
		observability.LLMFailuresTotal.WithLabelValues(provider, reason).Inc()
		// The error never contains the raw model response, only a summary.
		log.ErrorContext(ctx, "analysis failed", "provider", provider, "reason", reason, "error", err)
		return fmt.Errorf("analysis by %s failed: %w", provider, err)
	}
	observability.LLMRequestsTotal.WithLabelValues(provider, "ok").Inc()
	rec.Report = &domain.Report{ID: domain.NewID(), Provider: provider, Analysis: analysis,
		RemediationActions: domain.RemediationActions(analysis), CreatedAt: time.Now().UTC()}
	log.InfoContext(ctx, "analysis completed", "provider", provider, "root_cause", analysis.RootCause, "confidence", analysis.Confidence)
	return nil
}

// initMetrics creates every known label combination at zero. Labeled
// counters otherwise appear only on their first increment, and Prometheus
// increase()/rate() cannot see that first event.
func initMetrics(provider string) {
	for _, s := range []domain.InvestigationStatus{domain.InvestigationCompleted, domain.InvestigationFailed} {
		observability.InvestigationsTotal.WithLabelValues(string(s))
	}
	for tool := range diagnostics.Registry {
		observability.ToolFailuresTotal.WithLabelValues(tool)
		for _, h := range []domain.Health{domain.Healthy, domain.Degraded, domain.Down} {
			observability.ToolExecutionsTotal.WithLabelValues(tool, string(h))
		}
	}
	for _, status := range []string{"ok", "error"} {
		observability.LLMRequestsTotal.WithLabelValues(provider, status)
	}
	for _, reason := range []string{"request", "invalid_output"} {
		observability.LLMFailuresTotal.WithLabelValues(provider, reason)
	}
}
