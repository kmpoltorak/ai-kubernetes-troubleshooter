//go:build integration

package jobs_test

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/domain"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/investigation"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/jobs"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/llm"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/simulation"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/storage"
)

func workerDB(t *testing.T) (*storage.Store, *pgxpool.Pool) {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	s, err := storage.Connect(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	if _, err := s.MigrateUp(context.Background()); err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return s, pool
}
func workerEngine(t *testing.T, s *storage.Store, p llm.Provider) *investigation.Engine {
	t.Helper()
	clusters, err := simulation.NewClusters("healthy")
	if err != nil {
		t.Fatal(err)
	}
	return investigation.NewEngine(s, clusters, p, slog.New(slog.NewTextHandler(io.Discard, nil)), investigation.Config{Timeout: 5 * time.Second, MaxConcurrent: 1, Policy: domain.TargetPolicy{Clusters: map[string][]string{"local": nil}}})
}
func workerIncident(t *testing.T, s *storage.Store, pool *pgxpool.Pool) domain.Incident {
	t.Helper()
	inc := domain.Incident{Title: "test", Description: "test", Target: domain.Target{Cluster: "local", Namespace: "payments", ResourceType: domain.ResourceDeployment, ResourceName: "payment-service"}}
	if err := s.CreateIncident(context.Background(), &inc); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), `DELETE FROM incidents WHERE id=$1`, inc.ID); err != nil {
			t.Error(err)
		}
	})
	return inc
}
func workerConfig() jobs.Config {
	return jobs.Config{MaxConcurrent: 1, Capacity: 100, Timeout: 15 * time.Second, Lease: time.Second, Poll: 10 * time.Millisecond}
}
func startWorkers(t *testing.T, svc *jobs.Service) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); svc.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("worker did not stop")
		}
	})
}

type pausedRunner struct {
	*investigation.Engine
	marker string
}

func (r pausedRunner) Execute(ctx context.Context, inv domain.Investigation, p investigation.Progress) (domain.InvestigationRecord, error) {
	return r.Engine.Execute(ctx, inv, func(stepCtx context.Context, rec domain.InvestigationRecord, stage string) error {
		if err := p(stepCtx, rec, stage); err != nil {
			return err
		}
		if stage == "diagnostics" {
			req, err := http.NewRequestWithContext(ctx, "POST", r.marker+"/checkpoint", nil)
			if err != nil {
				return err
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return err
			}
			_ = resp.Body.Close()
			<-ctx.Done()
			return ctx.Err()
		}
		return nil
	})
}

func TestDurableWorkerProcess(t *testing.T) {
	if mode := os.Getenv("T05_WORKER_MODE"); mode != "" {
		s, _ := workerDB(t)
		var provider llm.Provider = llm.RulesProvider{}
		if mode == "analyzing" {
			provider = llm.NewOpenAIProvider(os.Getenv("T05_MARKER_URL"), "fixture", "fixture", "", 10*time.Second)
		}
		engine := workerEngine(t, s, provider)
		var runner jobs.Runner = engine
		if mode == "diagnostics" {
			runner = pausedRunner{engine, os.Getenv("T05_MARKER_URL")}
		}
		svc := jobs.New(s, runner, slog.New(slog.NewTextHandler(io.Discard, nil)), workerConfig())
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		svc.Run(ctx)
		return
	}
	for _, mode := range []string{"diagnostics", "analyzing"} {
		t.Run(mode, func(t *testing.T) {
			s, pool := workerDB(t)
			inc := workerIncident(t, s, pool)
			j, _, err := s.EnqueueJob(context.Background(), inc.ID, domain.NewID(), "", "healthy", 100, 15*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			reached := make(chan struct{})
			var once sync.Once
			var calls atomic.Int32
			endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				if r.URL.Path == "/checkpoint" {
					once.Do(func() { close(reached) })
					w.WriteHeader(204)
					return
				}
				if calls.Add(1) == 1 {
					once.Do(func() { close(reached) })
					<-r.Context().Done()
					return
				}
				http.Error(w, "unexpected model retry", 500)
			}))
			t.Cleanup(endpoint.Close)
			cmd := exec.Command(os.Args[0], "-test.run=^TestDurableWorkerProcess$")
			cmd.Env = append(os.Environ(), "T05_WORKER_MODE="+mode, "T05_MARKER_URL="+endpoint.URL)
			var stdout, stderr bytes.Buffer
			cmd.Stdout = &stdout
			cmd.Stderr = &stderr
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if cmd.ProcessState == nil {
					_ = cmd.Process.Kill()
					_ = cmd.Wait()
				}
			})
			select {
			case <-reached:
			case <-time.After(8 * time.Second):
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
				t.Fatalf("worker never reached %s: %s %s", mode, stdout.String(), stderr.String())
			}
			before, err := s.GetJob(context.Background(), inc.ID, j.ID)
			if err != nil {
				t.Fatal(err)
			}
			if before.Job.Stage != mode || len(before.Result.Evidence) == 0 {
				t.Fatalf("checkpoint not durable: %+v", before)
			}
			if err := cmd.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			_ = cmd.Wait()
			provider := llm.Provider(llm.RulesProvider{})
			if mode == "analyzing" {
				provider = llm.NewOpenAIProvider(endpoint.URL, "fixture", "fixture", "", time.Second)
			}
			svc := jobs.New(s, workerEngine(t, s, provider), slog.New(slog.NewTextHandler(io.Discard, nil)), workerConfig())
			startWorkers(t, svc)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			after, err := svc.Wait(ctx, inc.ID, j.ID)
			if err != nil {
				t.Fatal(err)
			}
			old, err := s.GetInvestigation(ctx, inc.ID, before.Result.Investigation.ID)
			if err != nil || old.Investigation.Status != domain.InvestigationFailed || len(old.Evidence) == 0 {
				t.Fatalf("interrupted evidence lost: %+v %v", old, err)
			}
			if mode == "diagnostics" {
				if after.Job.Status != domain.InvestigationCompleted || after.Job.Attempts != 2 || after.Result.Report == nil || after.Result.Investigation.ID == before.Result.Investigation.ID {
					t.Fatalf("job not recovered: %+v", after)
				}
			} else if after.Job.Status != domain.InvestigationFailed || after.Job.Attempts != 1 || !strings.Contains(after.Job.Error, "unknown") || calls.Load() != 1 {
				t.Fatalf("uncertain model call retried: %+v calls=%d", after, calls.Load())
			}
		})
	}
}
