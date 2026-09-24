//go:build integration

package api

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/domain"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/incidents"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/investigation"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/llm"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/simulation"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/storage"
)

// newIntegrationServer wires the real stack: PostgreSQL store, simulated
// cluster (client-go fake clients) and the given provider.
func newIntegrationServer(t *testing.T, provider llm.Provider) *httptest.Server {
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
	srv := httptest.NewServer(New(incidents.NewService(store, policy), engine,
		map[string]func(context.Context) error{"database": store.Ping}, log, 1000))
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
			rootCause := report["analysis"].(map[string]any)["root_cause"].(string)
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
