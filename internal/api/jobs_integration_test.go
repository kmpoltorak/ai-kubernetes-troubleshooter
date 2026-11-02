//go:build integration

package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/config"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/domain"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/incidents"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/jobs"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/llm"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/storage"
)

func jobPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}
func removeJobIncident(t *testing.T, pool *pgxpool.Pool, id string) {
	t.Helper()
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), `DELETE FROM incidents WHERE id=$1`, id); err != nil {
			t.Error(err)
		}
	})
}
func waitJobResponse(t *testing.T, url string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		resp, out := do(t, "GET", url, "")
		if resp.StatusCode != 200 {
			t.Fatalf("job read: %d %v", resp.StatusCode, out)
		}
		status := out["job"].(map[string]any)["status"]
		if status == "completed" || status == "failed" {
			return out
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("job never finished")
	return nil
}

func TestIntegrationJobQueueAPI(t *testing.T) {
	srv := newQueuedIntegrationServer(t, llm.RulesProvider{}, jobs.Config{Capacity: 1, Timeout: time.Minute, Poll: 10 * time.Millisecond}, false)
	pool := jobPool(t)
	id := createIncident(t, srv)
	removeJobIncident(t, pool, id)
	path := srv.URL + "/api/v1/incidents/" + id + "/jobs"
	for _, key := range []string{"", "not-a-uuid"} {
		resp, out := do(t, "POST", path, "", "Idempotency-Key", key)
		if resp.StatusCode != 400 || out["error"].(map[string]any)["code"] != "validation_failed" {
			t.Fatalf("bad key: %d %v", resp.StatusCode, out)
		}
	}
	key := domain.NewID()
	resp, out := do(t, "POST", path, "", "Idempotency-Key", key)
	if resp.StatusCode != 202 {
		t.Fatalf("enqueue: %d %v", resp.StatusCode, out)
	}
	job := out["job"].(map[string]any)
	jobID := job["id"].(string)
	location := resp.Header.Get("Location")
	if location != "/api/v1/incidents/"+id+"/jobs/"+jobID || job["status"] != "queued" || out["result"].(map[string]any)["investigation"].(map[string]any)["status"] != "queued" {
		t.Fatalf("queued contract: %s %v", location, out)
	}
	raw, _ := json.Marshal(out)
	if strings.Contains(string(raw), key) || strings.Contains(string(raw), "lease_token") {
		t.Fatalf("private fields exposed: %s", raw)
	}
	resp, replay := do(t, "POST", path, "", "Idempotency-Key", strings.ToUpper(key))
	if resp.StatusCode != 202 || replay["job"].(map[string]any)["id"] != jobID {
		t.Fatalf("replay at capacity: %d %v", resp.StatusCode, replay)
	}
	if resp, out := do(t, "POST", path, `{"scenario":"oom_killed"}`, "Idempotency-Key", key); resp.StatusCode != 409 || out["error"].(map[string]any)["code"] != "idempotency_conflict" {
		t.Fatalf("conflict: %d %v", resp.StatusCode, out)
	}
	if resp, out := do(t, "POST", path, "", "Idempotency-Key", domain.NewID()); resp.StatusCode != 429 || resp.Header.Get("Retry-After") == "" {
		t.Fatalf("queue full: %d %v", resp.StatusCode, out)
	}
	other := createIncident(t, srv)
	removeJobIncident(t, pool, other)
	if resp, _ := do(t, "GET", srv.URL+"/api/v1/incidents/"+other+"/jobs/"+jobID, ""); resp.StatusCode != 404 {
		t.Fatalf("foreign job: %d", resp.StatusCode)
	}
	if resp, _ := do(t, "GET", path+"/bad", ""); resp.StatusCode != 400 {
		t.Fatalf("bad ID: %d", resp.StatusCode)
	}
	var count int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM investigation_jobs WHERE incident_id=$1`, id).Scan(&count); err != nil || count != 1 {
		t.Fatalf("duplicate submission: %d %v", count, err)
	}
}

func TestIntegrationJobTerminalReplay(t *testing.T) {
	srv := newIntegrationServer(t, llm.RulesProvider{})
	pool := jobPool(t)
	id := createIncident(t, srv)
	removeJobIncident(t, pool, id)
	key := domain.NewID()
	path := srv.URL + "/api/v1/incidents/" + id + "/jobs"
	resp, out := do(t, "POST", path, "", "Idempotency-Key", key)
	if resp.StatusCode != 202 {
		t.Fatalf("new job must be 202: %d %v", resp.StatusCode, out)
	}
	final := waitJobResponse(t, srv.URL+resp.Header.Get("Location"))
	if final["job"].(map[string]any)["status"] != "completed" || final["result"].(map[string]any)["report"] == nil {
		t.Fatalf("finished: %v", final)
	}
	resp, replay := do(t, "POST", path, "", "Idempotency-Key", key)
	if resp.StatusCode != 200 || replay["job"].(map[string]any)["id"] != final["job"].(map[string]any)["id"] {
		t.Fatalf("terminal replay: %d %v", resp.StatusCode, replay)
	}
	var count int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM investigations WHERE incident_id=$1`, id).Scan(&count); err != nil || count != 1 {
		t.Fatalf("duplicate execution: %d %v", count, err)
	}
}

type disconnectProvider struct {
	started, release chan struct{}
	cancelled        atomic.Bool
}

func (p *disconnectProvider) Name() string { return "test/disconnect" }
func (p *disconnectProvider) Analyze(ctx context.Context, in llm.AnalysisInput) (domain.Analysis, error) {
	close(p.started)
	select {
	case <-p.release:
		return (llm.RulesProvider{}).Analyze(ctx, in)
	case <-ctx.Done():
		p.cancelled.Store(true)
		return domain.Analysis{}, ctx.Err()
	}
}
func TestIntegrationClientDisconnectKeepsJob(t *testing.T) {
	provider := &disconnectProvider{started: make(chan struct{}), release: make(chan struct{})}
	srv := newIntegrationServer(t, provider)
	pool := jobPool(t)
	id := createIncident(t, srv)
	removeJobIncident(t, pool, id)
	key := domain.NewID()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", srv.URL+"/api/v1/incidents/"+id+"/investigate", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Idempotency-Key", key)
	done := make(chan error, 1)
	go func() {
		resp, err := http.DefaultClient.Do(req)
		if resp != nil {
			_ = resp.Body.Close()
		}
		done <- err
	}()
	select {
	case <-provider.started:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not reach analyzer")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("HTTP did not disconnect: %v", err)
	}
	close(provider.release)
	var jobID string
	if err := pool.QueryRow(context.Background(), `SELECT id FROM investigation_jobs WHERE incident_id=$1 AND idempotency_key=$2`, id, key).Scan(&jobID); err != nil {
		t.Fatal(err)
	}
	final := waitJobResponse(t, srv.URL+"/api/v1/incidents/"+id+"/jobs/"+jobID)
	if final["job"].(map[string]any)["status"] != "completed" || provider.cancelled.Load() {
		t.Fatalf("request cancelled accepted job: %v", final)
	}
}

func TestIntegrationJobStorageUnavailable(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	store, err := storage.Connect(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	store.Close()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	// A closed real pool rejects both reads and submissions before any runner call.
	scheduler := jobs.New(store, nil, log, jobs.Config{})
	srv := httptest.NewServer(New(incidents.NewService(store, domain.TargetPolicy{ClusterName: "local"}), scheduler, nil, log, 1000, config.Auth{}))
	t.Cleanup(srv.Close)
	path := srv.URL + "/api/v1/incidents/" + domain.NewID() + "/jobs"
	for _, method := range []string{"GET", "POST"} {
		target := path
		if method == "GET" {
			target += "/" + domain.NewID()
		}
		resp, out := do(t, method, target, "", "Idempotency-Key", domain.NewID())
		if resp.StatusCode != 503 || out["error"].(map[string]any)["code"] != "unavailable" {
			t.Fatalf("storage failure: %d %v", resp.StatusCode, out)
		}
	}
}
