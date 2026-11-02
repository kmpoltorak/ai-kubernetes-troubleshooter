//go:build integration

package jobs_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/domain"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/investigation"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/jobs"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/kube"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/llm"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/simulation"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

type failingTarget struct {
	simulation.Clusters
	calls  atomic.Int32
	denied bool
}

func (c *failingTarget) For(target domain.Target, scenario string) (kube.Cluster, string, error) {
	cluster, resolved, err := c.Clusters.For(target, scenario)
	if err != nil {
		return cluster, resolved, err
	}
	cluster.Client.(*fake.Clientset).PrependReactor("get", "deployments", func(ktesting.Action) (bool, runtime.Object, error) {
		call := c.calls.Add(1)
		if c.denied {
			return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: "apps", Resource: "deployments"}, target.ResourceName, nil)
		}
		if call <= 2 {
			return true, nil, apierrors.NewServiceUnavailable("target temporarily unavailable")
		}
		return false, nil, nil
	})
	return cluster, resolved, nil
}

type countingRules struct{ calls atomic.Int32 }

func (*countingRules) Name() string { return "rules/rules-v1" }
func (p *countingRules) Analyze(ctx context.Context, in llm.AnalysisInput) (domain.Analysis, error) {
	p.calls.Add(1)
	return (llm.RulesProvider{}).Analyze(ctx, in)
}

func TestWorkerRetriesOnlyTransientTargetFailures(t *testing.T) {
	for _, denied := range []bool{false, true} {
		name := "503 then success"
		if denied {
			name = "403 terminal"
		}
		t.Run(name, func(t *testing.T) {
			s, pool := workerDB(t)
			inc := workerIncident(t, s, pool)
			clusters, err := simulation.NewClusters("healthy")
			if err != nil {
				t.Fatal(err)
			}
			target := &failingTarget{Clusters: clusters, denied: denied}
			provider := &countingRules{}
			log := slog.New(slog.NewTextHandler(io.Discard, nil))
			engine := investigation.NewEngine(s, target, provider, log, investigation.Config{Timeout: time.Second, MaxConcurrent: 1, Policy: domain.TargetPolicy{ClusterName: "local"}})
			svc := jobs.New(s, engine, log, workerConfig())
			accepted, err := svc.Submit(context.Background(), inc.ID, domain.NewID(), investigation.Options{})
			if err != nil {
				t.Fatal(err)
			}
			startWorkers(t, svc)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			done, err := svc.Wait(ctx, inc.ID, accepted.Job.ID)
			if err != nil {
				t.Fatal(err)
			}
			if denied {
				if done.Job.Status != domain.InvestigationFailed || done.Job.Attempts != 1 || target.calls.Load() != 1 || provider.calls.Load() != 0 {
					t.Fatalf("denied target retried: %+v reads=%d llm=%d", done, target.calls.Load(), provider.calls.Load())
				}
			} else {
				if done.Job.Status != domain.InvestigationCompleted || done.Job.Attempts != 3 || target.calls.Load() != 3 || provider.calls.Load() != 1 || done.Result.Report == nil {
					t.Fatalf("transient target not recovered: %+v reads=%d llm=%d", done, target.calls.Load(), provider.calls.Load())
				}
			}
		})
	}
}

func TestWorkerDoesNotRetryLLMTimeout(t *testing.T) {
	s, pool := workerDB(t)
	inc := workerIncident(t, s, pool)
	var calls atomic.Int32
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		calls.Add(1)
		<-r.Context().Done()
	}))
	t.Cleanup(endpoint.Close)
	provider := llm.NewOpenAIProvider(endpoint.URL, "fixture", "fixture", 100*time.Millisecond)
	svc := jobs.New(s, workerEngine(t, s, provider), slog.New(slog.NewTextHandler(io.Discard, nil)), workerConfig())
	accepted, err := svc.Submit(context.Background(), inc.ID, domain.NewID(), investigation.Options{})
	if err != nil {
		t.Fatal(err)
	}
	startWorkers(t, svc)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done, err := svc.Wait(ctx, inc.ID, accepted.Job.ID)
	if err != nil || done.Job.Status != domain.InvestigationFailed || done.Job.Attempts != 1 || calls.Load() != 1 || done.Result.Report != nil || len(done.Result.Evidence) == 0 {
		t.Fatalf("LLM timeout retried or lost evidence: job=%+v calls=%d error=%v", done.Job, calls.Load(), err)
	}
}
