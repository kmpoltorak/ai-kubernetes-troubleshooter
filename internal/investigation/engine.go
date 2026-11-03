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
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/redact"
)

var (
	// ErrBusy means all investigation slots are in use; retry later.
	ErrBusy     = errors.New("too many concurrent investigations")
	ErrProgress = errors.New("could not persist investigation progress")
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

func (e *Engine) prepare(ctx context.Context, incidentID string, opts Options) (domain.Incident, kube.Cluster, string, error) {
	inc, err := e.store.GetIncident(ctx, incidentID)
	if err != nil {
		return inc, kube.Cluster{}, "", fmt.Errorf("load incident: %w", err)
	}
	inc = inc.Redacted()
	if err := e.cfg.Policy.Check(inc.Target); err != nil {
		return inc, kube.Cluster{}, "", fmt.Errorf("%w: %w", ErrInvalidOptions, err)
	}
	cluster, scenario, err := e.clusters.For(inc.Target, opts.Scenario)
	if err != nil {
		return inc, cluster, "", fmt.Errorf("%w: %w", ErrInvalidOptions, err)
	}
	return inc, cluster, scenario, nil
}

func (e *Engine) Validate(ctx context.Context, incidentID string, opts Options) (string, error) {
	_, _, scenario, err := e.prepare(ctx, incidentID, opts)
	return scenario, err
}

type Progress func(context.Context, domain.InvestigationRecord, string) error

// Execute runs one pre-created attempt. The owner persists every completed step
// and must fence the final write; this function never creates a second attempt.
func (e *Engine) Execute(ctx context.Context, inv domain.Investigation, progress Progress) (domain.InvestigationRecord, error) {
	inc, cluster, _, err := e.prepare(ctx, inv.IncidentID, Options{Scenario: inv.Scenario})
	if err != nil {
		now := time.Now().UTC()
		inv.Status = domain.InvestigationFailed
		inv.CompletedAt = &now
		inv.Error = redact.Text(err.Error())
		return domain.InvestigationRecord{Investigation: inv, ToolExecutions: []domain.ToolExecution{}, Evidence: []domain.Evidence{}}, err
	}
	return e.execute(ctx, inc, cluster, inv, progress)
}

// Investigate is the synchronous runner used by rules evaluations and unit tests.
// The HTTP server submits durable jobs instead of calling it directly.
func (e *Engine) Investigate(ctx context.Context, incidentID string, opts Options) (domain.InvestigationRecord, error) {
	select {
	case e.slots <- struct{}{}:
		defer func() { <-e.slots }()
	default:
		return domain.InvestigationRecord{}, ErrBusy
	}
	inc, cluster, scenario, err := e.prepare(ctx, incidentID, opts)
	if err != nil {
		return domain.InvestigationRecord{}, err
	}
	inv := domain.Investigation{IncidentID: inc.ID, Status: domain.InvestigationRunning, Scenario: scenario}
	if err := e.store.CreateInvestigation(ctx, &inv); err != nil {
		return domain.InvestigationRecord{}, fmt.Errorf("create investigation: %w", err)
	}
	rec, analysisErr := e.execute(ctx, inc, cluster, inv, nil)
	saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := e.store.FinishInvestigation(saveCtx, rec); err != nil {
		return rec, fmt.Errorf("persist investigation: %w", err)
	}
	observability.InvestigationsTotal.WithLabelValues(string(rec.Investigation.Status)).Inc()
	observability.InvestigationDuration.Observe(time.Since(inv.StartedAt).Seconds())
	return rec, analysisErr
}

func (e *Engine) execute(ctx context.Context, inc domain.Incident, cluster kube.Cluster, inv domain.Investigation, progress Progress) (domain.InvestigationRecord, error) {
	ctx, cancel := context.WithTimeout(ctx, e.cfg.Timeout)
	defer cancel()
	rec := domain.InvestigationRecord{Investigation: inv, ToolExecutions: []domain.ToolExecution{}, Evidence: []domain.Evidence{}}
	log := e.log.With("incident_id", inc.ID, "investigation_id", inv.ID)
	log.InfoContext(ctx, "investigation started", "scenario", inv.Scenario, "target", inc.Ref(), "namespace", inc.Namespace)
	r := &runner{engine: e, log: log, cluster: cluster, ns: inc.Namespace, rec: &rec, progress: progress}
	analysisErr := r.collect(ctx, inc.Target)
	if r.progressErr != nil {
		analysisErr = r.progressErr
	}
	if analysisErr == nil && progress != nil {
		if err := progress(ctx, domain.InvestigationRecord{Investigation: inv}, "analyzing"); err != nil {
			analysisErr = fmt.Errorf("%w: %w", ErrProgress, err)
		}
	}
	if analysisErr == nil {
		analysisErr = e.analyze(ctx, log, inc, &rec)
	}
	now := time.Now().UTC()
	rec.Investigation.CompletedAt = &now
	rec.Investigation.Status = domain.InvestigationCompleted
	if analysisErr != nil {
		rec.Investigation.Status = domain.InvestigationFailed
		rec.Investigation.Error = redact.Text(analysisErr.Error())
	}
	log.InfoContext(ctx, "investigation finished", "status", rec.Investigation.Status, "tool_executions", len(rec.ToolExecutions), "evidence", len(rec.Evidence))
	if analysisErr != nil {
		return rec, fmt.Errorf("%w: %w", ErrAnalysisFailed, redact.Error(analysisErr))
	}
	return rec, nil
}

// runner executes one investigation's plan and accumulates the record.
type runner struct {
	engine      *Engine
	log         *slog.Logger
	cluster     kube.Cluster
	ns          string
	rec         *domain.InvestigationRecord
	progress    Progress
	progressErr error
	lastToolErr error
}

// collect is the diagnostic plan. Each step's input is derived from earlier
// results (selector from the Deployment, containers from the pods, ConfigMap
// names from the pod spec), never from model output. It fails when the
// target itself cannot be read: no diagnosis, least of all "healthy", can be
// made about a resource that was not observed.
func (r *runner) collect(ctx context.Context, t domain.Target) error {
	if res, ok := r.run(ctx, diagnostics.GetNamespace, diagnostics.Input{}); ok && res.Health == domain.Down {
		return nil
	}

	var selector string
	var podLabels map[string]string
	var spec *diagnostics.PodSpecSummary
	var pods diagnostics.PodsInfo

	switch t.ResourceType {
	case domain.ResourceDeployment:
		res, ok := r.run(ctx, diagnostics.GetDeployment, diagnostics.Input{Name: t.ResourceName})
		if !ok {
			return r.unobserved(t)
		}
		if slices.Contains(res.Signals, diagnostics.SignalWorkloadNotFound) {
			return nil
		}
		d := res.Data.(diagnostics.DeploymentInfo)
		selector, podLabels, spec = d.Selector, d.PodLabels, &d.Template
	case domain.ResourceService:
		res, ok := r.run(ctx, diagnostics.GetService, diagnostics.Input{Name: t.ResourceName})
		if !ok {
			return r.unobserved(t)
		}
		if slices.Contains(res.Signals, diagnostics.SignalServiceNotFound) {
			return nil
		}
		if svcs := res.Data.(diagnostics.ServicesInfo).Services; len(svcs) > 0 && len(svcs[0].Selector) > 0 {
			podLabels = svcs[0].Selector
			selector = labels.SelectorFromSet(podLabels).String()
		}
	case domain.ResourcePod:
		res, ok := r.run(ctx, diagnostics.GetPods, diagnostics.Input{Name: t.ResourceName})
		if !ok {
			return r.unobserved(t)
		}
		if slices.Contains(res.Signals, diagnostics.SignalWorkloadNotFound) {
			return nil
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
		r.run(ctx, diagnostics.GetPodLogs, diagnostics.Input{Name: p.Name, Names: logContainers(p)})
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
	return nil
}

// unobserved reports the error of the last, failed, target read.
func (r *runner) unobserved(t domain.Target) error {
	if r.progressErr != nil {
		return r.progressErr
	}
	te := r.rec.ToolExecutions[len(r.rec.ToolExecutions)-1]
	return fmt.Errorf("could not read %s (%s): %w", t.Ref(), te.ToolName, r.lastToolErr)
}

// logPods picks the pods whose logs are read: pods with findings first,
// then the rest, at most maxLogPods.
func logPods(pods []diagnostics.PodInfo) []diagnostics.PodInfo {
	sorted := slices.Clone(pods)
	sort.SliceStable(sorted, func(i, j int) bool { return len(sorted[i].Signals) > 0 && len(sorted[j].Signals) == 0 })
	return sorted[:min(len(sorted), maxLogPods)]
}

// logContainers lists the containers whose logs are read: app containers
// and init containers that are running or failing. Init containers that
// completed or have not started yet have nothing to explain.
func logContainers(p diagnostics.PodInfo) []string {
	names := make([]string, 0, len(p.Containers))
	for _, c := range p.Containers {
		if c.Init && (c.Reason == "Completed" || c.Reason == "PodInitializing") {
			continue
		}
		names = append(names, c.Name)
	}
	return names
}

// run executes one allowlisted tool, records the execution and, on success,
// the evidence. ok is false when the tool could not observe anything.
func (r *runner) run(ctx context.Context, tool string, in diagnostics.Input) (diagnostics.Result, bool) {
	if r.progressErr != nil {
		return diagnostics.Result{}, false
	}
	in.Namespace = r.ns
	input, _ := json.Marshal(in)  // Input always marshals
	input, _ = redact.JSON(input) // marshaled Input is valid JSON
	te := domain.ToolExecution{ID: domain.NewID(), ToolName: tool, Input: input, StartedAt: time.Now().UTC()}

	toolCtx, cancel := context.WithTimeout(ctx, r.engine.cfg.ToolTimeout)
	res, err := diagnostics.Run(toolCtx, tool, r.cluster, in)
	cancel()
	te.DurationMs = time.Since(te.StartedAt).Milliseconds()

	var output []byte
	if err == nil {
		output, err = json.Marshal(res.Data)
		if err == nil {
			output, err = redact.JSON(output)
		}
	}
	if err != nil {
		r.lastToolErr = err
		te.Status, te.Error = domain.ToolFailed, redact.Text(err.Error())
		r.rec.ToolExecutions = append(r.rec.ToolExecutions, te)
		r.save(ctx, te, nil)
		observability.ToolFailuresTotal.WithLabelValues(tool).Inc()
		r.log.WarnContext(ctx, "diagnostic failed", "tool_name", tool, "duration_ms", te.DurationMs, "error", err)
		return diagnostics.Result{}, false
	}
	te.Status, te.Output = domain.ToolSucceeded, output
	r.rec.ToolExecutions = append(r.rec.ToolExecutions, te)
	r.rec.Evidence = append(r.rec.Evidence, domain.Evidence{
		ID: domain.NewID(), ToolExecutionID: te.ID, Source: tool, Subject: redact.Text(res.Subject),
		Health: res.Health, Summary: redact.Text(res.Summary), Signals: append([]string{}, res.Signals...), Data: output,
	})
	r.save(ctx, te, r.rec.Evidence[len(r.rec.Evidence)-1:])
	if r.progressErr != nil {
		return res, false
	}
	observability.ToolExecutionsTotal.WithLabelValues(tool, string(res.Health)).Inc()
	r.log.InfoContext(ctx, "diagnostic completed", "tool_name", tool, "duration_ms", te.DurationMs,
		"health", res.Health, "signals", res.Signals)
	return res, true
}

func (r *runner) save(ctx context.Context, te domain.ToolExecution, evidence []domain.Evidence) {
	if r.progress == nil {
		return
	}
	step := domain.InvestigationRecord{Investigation: r.rec.Investigation, ToolExecutions: []domain.ToolExecution{te}, Evidence: evidence}
	if err := r.progress(ctx, step, "diagnostics"); err != nil {
		r.progressErr = fmt.Errorf("%w: %w", ErrProgress, err)
	}
}

// analyze calls the provider and validates its output. Only a valid analysis
// becomes a report.
func (e *Engine) analyze(ctx context.Context, log *slog.Logger, inc domain.Incident, rec *domain.InvestigationRecord) error {
	if len(rec.Evidence) == 0 {
		return errors.New("no diagnostic evidence collected; every tool failed")
	}
	provider := e.provider.Name()
	start := time.Now()
	var failed []domain.ToolExecution
	for _, te := range rec.ToolExecutions {
		if te.Status == domain.ToolFailed {
			failed = append(failed, te)
		}
	}
	limits := diagnostics.SamplingLimits()
	limits = append(limits, fmt.Sprintf("Investigation plan: at most %d log pods, %d references per ConfigMap/Secret kind and %d event subjects; observations are not simultaneous.", maxLogPods, maxConfigRefs, maxEventSubject))
	for _, ev := range rec.Evidence {
		if ev.Source == diagnostics.GetNetworkPolicies {
			limits = append(limits, "NetworkPolicies describe declarations for the supplied labels; CNI enforcement and the actual connection path are not observed.")
		}
		if ev.Source == diagnostics.GetPodLogs {
			var logs diagnostics.LogsInfo
			if err := json.Unmarshal(ev.Data, &logs); err != nil {
				return fmt.Errorf("read log sampling metadata: %w", err)
			}
			if slices.ContainsFunc(logs.Streams, func(s diagnostics.LogStream) bool { return s.Truncated }) {
				limits = append(limits, ev.ID+": retained log sample is truncated")
			}
		}
	}
	in := llm.AnalysisInput{Incident: inc, Evidence: rec.Evidence, Failed: failed, Limitations: limits}
	analysis, err := e.provider.Analyze(ctx, in)
	observability.LLMRequestDuration.WithLabelValues(provider).Observe(time.Since(start).Seconds())
	analysis.MissingData = withRequiredContext(analysis.MissingData, in.MissingData())
	analysis.Limitations = withRequiredContext(analysis.Limitations, limits)
	analysis = analysis.Redacted()
	if err == nil {
		if verr := analysis.Validate(rec.Evidence, failed); verr != nil {
			err = fmt.Errorf("%w: %w", llm.ErrInvalidOutput, verr)
		}
	}
	if err != nil {
		reason := "request"
		if errors.Is(err, llm.ErrInvalidOutput) {
			reason = "invalid_output"
		}
		if errors.Is(err, llm.ErrInputTooLarge) {
			reason = "input_limit"
		}
		observability.LLMRequestsTotal.WithLabelValues(provider, "error").Inc()
		observability.LLMFailuresTotal.WithLabelValues(provider, reason).Inc()
		// The error never contains the raw model response, only a summary.
		log.ErrorContext(ctx, "analysis failed", "provider", provider, "reason", reason, "error", err)
		return fmt.Errorf("analysis by %s failed: %w", provider, redact.Error(err))
	}
	observability.LLMRequestsTotal.WithLabelValues(provider, "ok").Inc()
	rec.Report = &domain.Report{ID: domain.NewID(), Provider: provider, Analysis: analysis,
		RemediationActions: domain.RemediationActions(analysis), CreatedAt: time.Now().UTC()}
	log.InfoContext(ctx, "analysis completed", "provider", provider, "root_cause", analysis.RootCause, "verdict", analysis.Verdict, "support", analysis.Support)
	return nil
}

func withRequiredContext(values, required []string) []string {
	for _, value := range required {
		if !slices.Contains(values, value) {
			values = append(values, value)
		}
	}
	return values
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
	for _, reason := range []string{"request", "invalid_output", "input_limit"} {
		observability.LLMFailuresTotal.WithLabelValues(provider, reason)
	}
}
