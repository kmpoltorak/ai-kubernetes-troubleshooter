package investigation

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/diagnostics"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/domain"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/kube"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/llm"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/simulation"
)

// memStore is an in-memory Store.
type memStore struct {
	mu        sync.Mutex
	incidents map[string]domain.Incident
	finished  []domain.InvestigationRecord
	finishErr error
}

func newMemStore(incs ...domain.Incident) *memStore {
	s := &memStore{incidents: map[string]domain.Incident{}}
	for _, i := range incs {
		s.incidents[i.ID] = i
	}
	return s
}

func (s *memStore) GetIncident(_ context.Context, id string) (domain.Incident, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if inc, ok := s.incidents[id]; ok {
		return inc, nil
	}
	return domain.Incident{}, domain.ErrNotFound
}

func (s *memStore) CreateInvestigation(_ context.Context, inv *domain.Investigation) error {
	inv.ID, inv.StartedAt = domain.NewID(), time.Now()
	return nil
}

func (s *memStore) FinishInvestigation(ctx context.Context, rec domain.InvestigationRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	s.finished = append(s.finished, rec)
	return s.finishErr
}

var policy = domain.TargetPolicy{ClusterName: "local"}

func incident(rt domain.ResourceType, name string) domain.Incident {
	return domain.Incident{ID: domain.NewID(), Title: "t", Description: "d",
		Target: domain.Target{Cluster: "local", Namespace: "payments", ResourceType: rt, ResourceName: name}}
}

func newEngine(store Store, clusters Clusters, p llm.Provider) *Engine {
	return NewEngine(store, clusters, p, slog.New(slog.NewTextHandler(io.Discard, nil)),
		Config{Timeout: 10 * time.Second, MaxConcurrent: 2, Policy: policy})
}

func sim(t *testing.T) simulation.Clusters {
	c, err := simulation.NewClusters("healthy")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func tools(rec domain.InvestigationRecord) []string {
	var out []string
	for _, te := range rec.ToolExecutions {
		out = append(out, te.ToolName)
	}
	return out
}

func TestInvestigateDeploymentPlan(t *testing.T) {
	inc := incident(domain.ResourceDeployment, "payment-service")
	store := newMemStore(inc)
	rec, err := newEngine(store, sim(t), llm.RulesProvider{}).Investigate(context.Background(), inc.ID, Options{Scenario: "oom_killed"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		diagnostics.GetNamespace, diagnostics.GetDeployment, diagnostics.GetPods, diagnostics.GetEvents,
		diagnostics.GetPodLogs, diagnostics.GetPodLogs, diagnostics.GetService, diagnostics.GetConfigMap,
		diagnostics.GetSecretMetadata, diagnostics.GetNetworkPolicies, diagnostics.GetResourceUsage,
	}
	if got := tools(rec); !slices.Equal(got, want) {
		t.Fatalf("plan:\n got %v\nwant %v", got, want)
	}
	if rec.Investigation.Status != domain.InvestigationCompleted || rec.Investigation.Scenario != "oom_killed" ||
		rec.Report == nil || len(rec.Report.RemediationActions) == 0 || len(store.finished) != 1 {
		t.Fatalf("unexpected record: %+v", rec.Investigation)
	}
	if len(rec.Evidence) != len(rec.ToolExecutions) {
		t.Fatalf("every successful tool must produce evidence: %d vs %d", len(rec.Evidence), len(rec.ToolExecutions))
	}
}

func TestInvestigateStopsWhenTargetMissing(t *testing.T) {
	inc := incident(domain.ResourceDeployment, "payment-service")
	store := newMemStore(inc)
	// The simulated namespace holds a different workload, so the target is missing.
	clusters := clustersFunc(func(t domain.Target, _ string) (kube.Cluster, string, error) {
		c, err := simulation.Build("healthy", domain.Target{Cluster: "local", Namespace: t.Namespace, ResourceType: domain.ResourceDeployment, ResourceName: "other"})
		return c, "healthy", err
	})
	rec, err := newEngine(store, clusters, llm.RulesProvider{}).Investigate(context.Background(), inc.ID, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got := tools(rec); !slices.Equal(got, []string{diagnostics.GetNamespace, diagnostics.GetDeployment}) {
		t.Fatalf("plan should stop after missing deployment, got %v", got)
	}
	if !strings.Contains(rec.Report.Analysis.RootCause, "not found") {
		t.Fatalf("root cause = %q", rec.Report.Analysis.RootCause)
	}
}

type clustersFunc func(domain.Target, string) (kube.Cluster, string, error)

func (f clustersFunc) For(t domain.Target, s string) (kube.Cluster, string, error) { return f(t, s) }

func TestInvestigatePodAndServiceTargets(t *testing.T) {
	for _, tc := range []struct {
		inc      domain.Incident
		scenario string
		want     string
	}{
		{incident(domain.ResourcePod, "payment-service-0"), "crash_loop_backoff", "CrashLoopBackOff"},
		{incident(domain.ResourceService, "payment-service"), "service_selector_mismatch", "selector"},
	} {
		store := newMemStore(tc.inc)
		rec, err := newEngine(store, sim(t), llm.RulesProvider{}).Investigate(context.Background(), tc.inc.ID, Options{Scenario: tc.scenario})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(rec.Report.Analysis.RootCause, tc.want) {
			t.Errorf("%s: root cause %q, want %q (tools %v)", tc.inc.Ref(), rec.Report.Analysis.RootCause, tc.want, tools(rec))
		}
	}
}

func TestInvestigateRejectsBadOptionsAndPolicy(t *testing.T) {
	inc := incident(domain.ResourceDeployment, "payment-service")
	e := newEngine(newMemStore(inc), sim(t), llm.RulesProvider{})
	if _, err := e.Investigate(context.Background(), inc.ID, Options{Scenario: "meteor"}); !errors.Is(err, ErrInvalidOptions) {
		t.Fatalf("unknown scenario: %v", err)
	}
	other := inc
	other.ID, other.Cluster = domain.NewID(), "prod"
	e = newEngine(newMemStore(other), sim(t), llm.RulesProvider{})
	if _, err := e.Investigate(context.Background(), other.ID, Options{}); !errors.Is(err, ErrInvalidOptions) {
		t.Fatalf("cluster outside policy: %v", err)
	}
	if _, err := e.Investigate(context.Background(), domain.NewID(), Options{}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing incident: %v", err)
	}
}

type invalidProvider struct{}

func (invalidProvider) Name() string { return "test/invalid" }
func (invalidProvider) Analyze(context.Context, llm.AnalysisInput) (domain.Analysis, error) {
	return domain.Analysis{Summary: "s", RootCause: "r", Confidence: 0.9, Severity: domain.SeverityHigh,
		Evidence:           []domain.EvidenceReference{{Source: "get_nodes", Description: "invented"}},
		RecommendedActions: []string{"a"}, SafeCommands: []string{"kubectl delete pod x"}}, nil
}

func TestInvalidAnalysisFailsButKeepsEvidence(t *testing.T) {
	inc := incident(domain.ResourceDeployment, "payment-service")
	store := newMemStore(inc)
	rec, err := newEngine(store, sim(t), invalidProvider{}).Investigate(context.Background(), inc.ID, Options{})
	if !errors.Is(err, ErrAnalysisFailed) || !errors.Is(err, llm.ErrInvalidOutput) {
		t.Fatalf("err = %v", err)
	}
	if rec.Report != nil || rec.Investigation.Status != domain.InvestigationFailed || len(rec.Evidence) == 0 {
		t.Fatalf("record: %+v", rec.Investigation)
	}
	for _, want := range []string{"get_nodes", "safe command"} {
		if !strings.Contains(rec.Investigation.Error, want) {
			t.Errorf("error %q does not mention %q", rec.Investigation.Error, want)
		}
	}
	if len(store.finished) != 1 {
		t.Fatal("failed investigation not persisted")
	}
}

func TestToolFailuresAreRecorded(t *testing.T) {
	inc := incident(domain.ResourceDeployment, "payment-service")
	clusters := clustersFunc(func(t domain.Target, _ string) (kube.Cluster, string, error) {
		c, err := simulation.Build("healthy", t)
		c.PodMetrics = nil // metrics-server not installed
		return c, "", err
	})
	rec, err := newEngine(newMemStore(inc), clusters, llm.RulesProvider{}).Investigate(context.Background(), inc.ID, Options{})
	if err != nil {
		t.Fatal(err)
	}
	last := rec.ToolExecutions[len(rec.ToolExecutions)-1]
	if last.ToolName != diagnostics.GetResourceUsage || last.Status != domain.ToolFailed || !strings.Contains(last.Error, "metrics API unavailable") {
		t.Fatalf("last execution: %+v", last)
	}
}

func TestBusy(t *testing.T) {
	inc := incident(domain.ResourceDeployment, "payment-service")
	block := make(chan struct{})
	clusters := clustersFunc(func(t domain.Target, _ string) (kube.Cluster, string, error) {
		<-block
		c, err := simulation.Build("healthy", t)
		return c, "", err
	})
	e := NewEngine(newMemStore(inc), clusters, llm.RulesProvider{}, slog.New(slog.NewTextHandler(io.Discard, nil)),
		Config{MaxConcurrent: 1, Policy: policy})
	done := make(chan error)
	go func() {
		_, err := e.Investigate(context.Background(), inc.ID, Options{})
		done <- err
	}()
	// Wait until the first investigation holds the only slot.
	for len(e.slots) == 0 {
		time.Sleep(time.Millisecond)
	}
	if _, err := e.Investigate(context.Background(), inc.ID, Options{}); !errors.Is(err, ErrBusy) {
		t.Fatalf("want ErrBusy, got %v", err)
	}
	close(block)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestPersistsAfterCallerCancels(t *testing.T) {
	inc := incident(domain.ResourceDeployment, "payment-service")
	store := newMemStore(inc)
	ctx, cancel := context.WithCancel(context.Background())
	clusters := clustersFunc(func(t domain.Target, _ string) (kube.Cluster, string, error) {
		cancel() // caller disconnects before any tool runs
		c, err := simulation.Build("healthy", t)
		return c, "", err
	})
	_, err := newEngine(store, clusters, llm.RulesProvider{}).Investigate(ctx, inc.ID, Options{})
	if len(store.finished) != 1 {
		t.Fatalf("investigation not persisted after cancel (err=%v)", err)
	}
}

// A target that cannot be read must fail the investigation, not be
// reported healthy.
func TestInvestigateFailsWhenTargetUnreadable(t *testing.T) {
	for _, inc := range []domain.Incident{
		incident(domain.ResourceDeployment, "payment-service"),
		incident(domain.ResourceService, "payment-service"),
		incident(domain.ResourcePod, "payment-service-0"),
	} {
		clusters := clustersFunc(func(t domain.Target, _ string) (kube.Cluster, string, error) {
			c, err := simulation.Build("healthy", t)
			for _, res := range []string{"deployments", "services", "pods"} {
				c.Client.(*fake.Clientset).PrependReactor("get", res, func(k8stesting.Action) (bool, runtime.Object, error) {
					return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: res}, t.ResourceName, errors.New("rbac"))
				})
			}
			return c, "healthy", err
		})
		rec, err := newEngine(newMemStore(inc), clusters, llm.RulesProvider{}).Investigate(context.Background(), inc.ID, Options{})
		if !errors.Is(err, ErrAnalysisFailed) || rec.Report != nil || rec.Investigation.Status != domain.InvestigationFailed ||
			!strings.Contains(rec.Investigation.Error, "forbidden") {
			t.Errorf("%s: err=%v status=%s error=%q", inc.Ref(), err, rec.Investigation.Status, rec.Investigation.Error)
		}
	}
}

// Collections serialize as [] so the UI can render any record.
func TestRecordJSONHasNoNullArrays(t *testing.T) {
	inc := incident(domain.ResourceDeployment, "payment-service")
	rec, err := newEngine(newMemStore(inc), sim(t), llm.RulesProvider{}).Investigate(context.Background(), inc.ID, Options{Scenario: "healthy"})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(rec)
	if strings.Contains(string(raw), `"signals":null`) {
		t.Fatalf("null signals in %s", raw)
	}
	empty := newEngine(newMemStore(inc), clustersFunc(func(t domain.Target, _ string) (kube.Cluster, string, error) {
		c, err := simulation.Build("healthy", t)
		c.Client.(*fake.Clientset).PrependReactor("*", "*", func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, errors.New("api down")
		})
		return c, "healthy", err
	}), llm.RulesProvider{})
	rec, _ = empty.Investigate(context.Background(), inc.ID, Options{})
	if raw, _ := json.Marshal(rec); !strings.Contains(string(raw), `"evidence":[]`) {
		t.Fatalf("evidence not [] when every tool failed: %s", raw)
	}
}

func TestLogContainersIncludeFailingInitContainers(t *testing.T) {
	p := diagnostics.PodInfo{Containers: []diagnostics.ContainerStatus{
		{Name: "done", Init: true, State: "terminated", Reason: "Completed"},
		{Name: "migrate", Init: true, State: "waiting", Reason: "CrashLoopBackOff"},
		{Name: "app", State: "waiting", Reason: "PodInitializing"},
	}}
	if got := logContainers(p); !slices.Equal(got, []string{"migrate", "app"}) {
		t.Fatalf("containers = %v", got)
	}
}
