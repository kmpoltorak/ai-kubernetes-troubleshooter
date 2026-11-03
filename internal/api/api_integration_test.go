//go:build integration

package api

import (
	"context"
	"encoding/json"
	"github.com/jackc/pgx/v5/pgxpool"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/config"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/domain"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/incidents"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/investigation"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/jobs"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/llm"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/simulation"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/storage"
)

// newIntegrationServer wires the real stack: PostgreSQL store, simulated
// cluster (client-go fake clients) and the given provider.
func newIntegrationServer(t *testing.T, provider llm.Provider) *httptest.Server {
	return newQueuedIntegrationServer(t, provider, jobs.Config{MaxConcurrent: 4, Timeout: 30 * time.Second, Poll: 10 * time.Millisecond}, true)
}

func newQueuedIntegrationServer(t *testing.T, provider llm.Provider, cfg jobs.Config, workers bool) *httptest.Server {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	store, err := storage.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	if _, err := store.MigrateUp(ctx); err != nil {
		t.Fatal(err)
	}
	clusters, err := simulation.NewClusters("healthy")
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	policy := domain.TargetPolicy{ClusterName: "local", AllowedNamespaces: []string{"payments"}}
	engine := investigation.NewEngine(store, clusters, provider, log, investigation.Config{Timeout: 10 * time.Second, MaxConcurrent: 4, Policy: policy})
	scheduler := jobs.New(store, engine, log, cfg)
	if workers {
		workerCtx, stop := context.WithCancel(ctx)
		done := make(chan struct{})
		go func() { defer close(done); scheduler.Run(workerCtx) }()
		t.Cleanup(func() { stop(); <-done })
	}
	srv := httptest.NewServer(New(incidents.NewService(store, policy), scheduler,
		map[string]func(context.Context) error{"database": store.Ping}, log, 1000, config.Auth{}))
	t.Cleanup(srv.Close)
	return srv
}

func TestIntegrationSampleIncidents(t *testing.T) {
	srv := newIntegrationServer(t, llm.RulesProvider{})
	samples := []struct {
		file, scenario, keyword string
	}{
		{"crash_loop_backoff.json", "crash_loop_backoff", "crashloopbackoff"},
		{"oom_killed.json", "oom_killed", "oomkilled"},
		{"service_selector_mismatch.json", "service_selector_mismatch", "selector"},
		{"readiness_probe_failure.json", "readiness_probe_failure", "readiness"},
	}
	for _, s := range samples {
		t.Run(s.scenario, func(t *testing.T) {
			body, err := os.ReadFile("../../docs/examples/" + s.file)
			if err != nil {
				t.Fatal(err)
			}
			resp, out := do(t, "POST", srv.URL+"/api/v1/incidents", string(body))
			if resp.StatusCode != http.StatusCreated {
				t.Fatalf("create: %d %v", resp.StatusCode, out)
			}
			id := out["id"].(string)

			resp, out = do(t, "POST", srv.URL+"/api/v1/incidents/"+id+"/investigate", `{"scenario":"`+s.scenario+`"}`)
			if resp.StatusCode != http.StatusCreated {
				t.Fatalf("investigate: %d %v", resp.StatusCode, out)
			}
			executions := len(out["tool_executions"].([]any))

			// The report must be readable back from PostgreSQL.
			resp, out = do(t, "GET", srv.URL+"/api/v1/incidents/"+id+"/report", "")
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("report: %d %v", resp.StatusCode, out)
			}
			report := out["report"].(map[string]any)
			analysis := report["analysis"].(map[string]any)
			rootCause := analysis["root_cause"].(string)
			if _, present := analysis["confidence"]; present || analysis["schema_version"] != float64(2) || analysis["verdict"] == nil || analysis["support"] == nil || analysis["missing_data"] == nil || len(analysis["limitations"].([]any)) == 0 {
				t.Fatalf("invalid v2 contract: %v", analysis)
			}
			collected := map[string]string{}
			for _, item := range out["evidence"].([]any) {
				ev := item.(map[string]any)
				collected[ev["id"].(string)] = ev["source"].(string)
			}
			for _, item := range analysis["evidence"].([]any) {
				ref := item.(map[string]any)
				if ref["evidence_id"] == nil || collected[ref["evidence_id"].(string)] != ref["source"] {
					t.Fatalf("foreign citation: %v", ref)
				}
			}
			if !strings.Contains(strings.ToLower(rootCause), s.keyword) {
				t.Fatalf("root cause %q does not mention %q", rootCause, s.keyword)
			}
			if n := len(out["tool_executions"].([]any)); n != executions || n < 5 {
				t.Fatalf("stored executions = %d, returned %d", n, executions)
			}
			if len(out["evidence"].([]any)) == 0 || len(report["remediation_actions"].([]any)) == 0 {
				t.Fatalf("evidence or remediation actions missing: %v", out)
			}
			if _, inc := do(t, "GET", srv.URL+"/api/v1/incidents/"+id, ""); inc["status"] != "analyzed" {
				t.Fatalf("incident status = %v", inc["status"])
			}
		})
	}
}

func TestIntegrationNamespaceOutsideAllowlist(t *testing.T) {
	srv := newIntegrationServer(t, llm.RulesProvider{})
	resp, out := do(t, "POST", srv.URL+"/api/v1/incidents",
		`{"cluster":"local","namespace":"kube-system","resource_type":"deployment","resource_name":"coredns","description":"d"}`)
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(out["error"].(map[string]any)["message"].(string), "allowed namespaces") {
		t.Fatalf("got %d %v", resp.StatusCode, out)
	}
}

func TestIntegrationFailedAnalysisIsStoredWithoutReport(t *testing.T) {
	srv := newIntegrationServer(t, brokenProvider{})
	resp, out := do(t, "POST", srv.URL+"/api/v1/incidents", incidentBody)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d", resp.StatusCode)
	}
	id := out["id"].(string)

	resp, out = do(t, "POST", srv.URL+"/api/v1/incidents/"+id+"/investigate", "")
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("investigate: %d %v", resp.StatusCode, out)
	}
	if resp, _ := do(t, "GET", srv.URL+"/api/v1/incidents/"+id+"/report", ""); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("report after failed analysis: %d", resp.StatusCode)
	}
	if _, inc := do(t, "GET", srv.URL+"/api/v1/incidents/"+id, ""); inc["status"] != "open" {
		t.Fatalf("incident status = %v, want open", inc["status"])
	}
}

func TestIntegrationReadiness(t *testing.T) {
	srv := newIntegrationServer(t, llm.RulesProvider{})
	resp, out := do(t, "GET", srv.URL+"/ready", "")
	if resp.StatusCode != http.StatusOK || out["checks"].(map[string]any)["database"] != "ok" {
		t.Fatalf("ready: %d %v", resp.StatusCode, out)
	}
}

func TestIntegrationLegacyReadsMaskWithoutRewriting(t *testing.T) {
	srv := newIntegrationServer(t, llm.RulesProvider{})
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	id := createIncident(t, srv)
	if _, err := pool.Exec(ctx, `UPDATE incidents SET title='password=synthetic-title', description='password=synthetic-description' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	resp, out := do(t, "POST", srv.URL+"/api/v1/incidents/"+id+"/investigate", "")
	if resp.StatusCode != 201 {
		t.Fatalf("investigate: %d %v", resp.StatusCode, out)
	}
	invID := out["investigation"].(map[string]any)["id"].(string)
	if _, err := pool.Exec(ctx, `UPDATE tool_executions SET error='password=synthetic-tool', output='{"password":"synthetic-output"}' WHERE investigation_id=$1`, invID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE evidence SET summary='password=synthetic-summary', data='{"token":"synthetic-data"}' WHERE investigation_id=$1`, invID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE reports SET analysis=jsonb_set(analysis,'{summary}','"password=synthetic-analysis"') WHERE investigation_id=$1`, invID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE remediation_actions SET description='password=synthetic-action' WHERE report_id=(SELECT id FROM reports WHERE investigation_id=$1)`, invID); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"", "/" + id, "/" + id + "/report"} {
		resp, out := do(t, "GET", srv.URL+"/api/v1/incidents"+path, "")
		raw, _ := json.Marshal(out)
		if resp.StatusCode != 200 || strings.Contains(string(raw), "synthetic-") {
			t.Fatalf("unsafe legacy read %s: %d %s", path, resp.StatusCode, raw)
		}
	}
	var description, data, summary string
	if err := pool.QueryRow(ctx, `SELECT description FROM incidents WHERE id=$1`, id).Scan(&description); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT data::text FROM evidence WHERE investigation_id=$1 LIMIT 1`, invID).Scan(&data); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT analysis->>'summary' FROM reports WHERE investigation_id=$1`, invID).Scan(&summary); err != nil {
		t.Fatal(err)
	}
	if description != "password=synthetic-description" || !strings.Contains(data, "synthetic-data") || summary != "password=synthetic-analysis" {
		t.Fatal("historical records rewritten")
	}
}

func TestIntegrationInputLimitPreservesEvidence(t *testing.T) {
	var calls atomic.Int32
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); w.WriteHeader(500) }))
	defer endpoint.Close()
	provider := llm.FromConfig(config.Config{LLMProvider: "openai", OpenAIBaseURL: endpoint.URL, LLMTimeout: time.Second, LLMMaxInputBytes: 1})
	srv := newIntegrationServer(t, provider)
	id := createIncident(t, srv)
	resp, out := do(t, "POST", srv.URL+"/api/v1/incidents/"+id+"/investigate", "")
	if resp.StatusCode != 502 || calls.Load() != 0 || len(out["result"].(map[string]any)["evidence"].([]any)) == 0 {
		t.Fatalf("limit not enforced: %d calls=%d", resp.StatusCode, calls.Load())
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var status, reason string
	var evidence, executions, reports int
	err = pool.QueryRow(ctx, `SELECT status,error,(SELECT count(*) FROM evidence WHERE investigation_id=i.id),(SELECT count(*) FROM tool_executions WHERE investigation_id=i.id),(SELECT count(*) FROM reports WHERE investigation_id=i.id) FROM investigations i WHERE incident_id=$1`, id).Scan(&status, &reason, &evidence, &executions, &reports)
	if err != nil || status != "failed" || !strings.Contains(reason, "LLM_MAX_INPUT_BYTES") || evidence == 0 || executions == 0 || reports != 0 {
		t.Fatalf("failure not stored: %s %s evidence=%d executions=%d reports=%d %v", status, reason, evidence, executions, reports, err)
	}
}
