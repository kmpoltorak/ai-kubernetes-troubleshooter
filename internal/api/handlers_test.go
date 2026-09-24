package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/domain"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/incidents"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/investigation"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/llm"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/simulation"
)

// memStore implements both incidents.Repository and investigation.Store.
type memStore struct {
	mu        sync.Mutex
	incidents map[string]domain.Incident
	reports   map[string]domain.InvestigationRecord
}

func (m *memStore) CreateIncident(_ context.Context, in *domain.Incident) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	in.ID, in.Status, in.CreatedAt = domain.NewID(), domain.IncidentOpen, time.Now()
	m.incidents[in.ID] = *in
	return nil
}

func (m *memStore) GetIncident(_ context.Context, id string) (domain.Incident, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if i, ok := m.incidents[id]; ok {
		return i, nil
	}
	return domain.Incident{}, domain.ErrNotFound
}

func (m *memStore) ListIncidents(context.Context, int, int) ([]domain.Incident, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []domain.Incident
	for _, i := range m.incidents {
		out = append(out, i)
	}
	return out, nil
}

func (m *memStore) LatestReport(_ context.Context, id string) (domain.InvestigationRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r, ok := m.reports[id]; ok {
		return r, nil
	}
	return domain.InvestigationRecord{}, domain.ErrNotFound
}

func (m *memStore) CreateInvestigation(_ context.Context, inv *domain.Investigation) error {
	inv.ID = domain.NewID()
	return nil
}

func (m *memStore) FinishInvestigation(_ context.Context, rec domain.InvestigationRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if rec.Report != nil {
		m.reports[rec.Investigation.IncidentID] = rec
	}
	return nil
}

type brokenProvider struct{}

func (brokenProvider) Name() string { return "broken/x" }
func (brokenProvider) Analyze(context.Context, llm.AnalysisInput) (domain.Analysis, error) {
	return domain.Analysis{}, llm.ErrInvalidOutput
}

func newServer(t *testing.T, provider llm.Provider, ready error) *httptest.Server {
	return newServerRPS(t, provider, ready, 1000)
}

func newServerRPS(t *testing.T, provider llm.Provider, ready error, rps float64) *httptest.Server {
	t.Helper()
	store := &memStore{incidents: map[string]domain.Incident{}, reports: map[string]domain.InvestigationRecord{}}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	clusters, err := simulation.NewClusters("healthy")
	if err != nil {
		t.Fatal(err)
	}
	policy := domain.TargetPolicy{ClusterName: "local"}
	engine := investigation.NewEngine(store, clusters, provider, log, investigation.Config{Timeout: 5 * time.Second, MaxConcurrent: 2, Policy: policy})
	checks := map[string]func(context.Context) error{"database": func(context.Context) error { return ready }}
	h := New(incidents.NewService(store, policy), engine, checks, log, rps)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func do(t *testing.T, method, url, body string, headers ...string) (*http.Response, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(method, url, strings.NewReader(body))
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return resp, out
}

const incidentBody = `{"cluster":"local","namespace":"payments","resource_type":"deployment",
"resource_name":"payment-service","description":"Pods are repeatedly restarting"}`

func createIncident(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	resp, out := do(t, "POST", srv.URL+"/api/v1/incidents", incidentBody)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d %v", resp.StatusCode, out)
	}
	if resp.Header.Get("Location") != "/api/v1/incidents/"+out["id"].(string) {
		t.Fatalf("location header: %q", resp.Header.Get("Location"))
	}
	return out["id"].(string)
}

func TestIncidentLifecycle(t *testing.T) {
	srv := newServer(t, llm.RulesProvider{}, nil)
	id := createIncident(t, srv)

	resp, out := do(t, "GET", srv.URL+"/api/v1/incidents/"+id, "")
	if resp.StatusCode != 200 || out["status"] != "open" {
		t.Fatalf("get: %d %v", resp.StatusCode, out)
	}
	resp, out = do(t, "GET", srv.URL+"/api/v1/incidents?limit=5", "")
	if resp.StatusCode != 200 || len(out["incidents"].([]any)) != 1 {
		t.Fatalf("list: %d %v", resp.StatusCode, out)
	}

	resp, _ = do(t, "GET", srv.URL+"/api/v1/incidents/"+id+"/report", "")
	if resp.StatusCode != 404 {
		t.Fatalf("report before investigation: %d", resp.StatusCode)
	}

	resp, out = do(t, "POST", srv.URL+"/api/v1/incidents/"+id+"/investigate", `{"scenario":"oom_killed"}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("investigate: %d %v", resp.StatusCode, out)
	}
	report := out["report"].(map[string]any)
	analysis := report["analysis"].(map[string]any)
	if !strings.Contains(analysis["root_cause"].(string), "OOMKilled") || len(report["remediation_actions"].([]any)) == 0 {
		t.Fatalf("unexpected report: %v", report)
	}

	resp, out = do(t, "GET", srv.URL+"/api/v1/incidents/"+id+"/report", "")
	if resp.StatusCode != 200 || out["report"] == nil {
		t.Fatalf("report: %d %v", resp.StatusCode, out)
	}

	// Empty body uses the default scenario.
	if resp, _ = do(t, "POST", srv.URL+"/api/v1/incidents/"+id+"/investigate", ""); resp.StatusCode != http.StatusCreated {
		t.Fatalf("investigate without body: %d", resp.StatusCode)
	}
}

func TestErrorResponses(t *testing.T) {
	srv := newServer(t, llm.RulesProvider{}, nil)
	id := createIncident(t, srv)
	missing := domain.NewID()
	tests := []struct {
		name, method, path, body string
		status                   int
		code                     string
	}{
		{"missing description", "POST", "/api/v1/incidents", `{"cluster":"local","namespace":"payments","resource_type":"pod","resource_name":"p"}`, 400, "validation_failed"},
		{"injection name", "POST", "/api/v1/incidents", `{"cluster":"local","namespace":"payments","resource_type":"pod","resource_name":"p;id","description":"d"}`, 400, "validation_failed"},
		{"unknown cluster", "POST", "/api/v1/incidents", `{"cluster":"prod","namespace":"payments","resource_type":"pod","resource_name":"p","description":"d"}`, 400, "validation_failed"},
		{"unsupported kind", "POST", "/api/v1/incidents", `{"cluster":"local","namespace":"payments","resource_type":"node","resource_name":"n","description":"d"}`, 400, "validation_failed"},
		{"unknown field", "POST", "/api/v1/incidents", `{"description":"d","admin":true}`, 400, "invalid_json"},
		{"malformed json", "POST", "/api/v1/incidents", `{"title":`, 400, "invalid_json"},
		{"empty create body", "POST", "/api/v1/incidents", ``, 400, "invalid_json"},
		{"bad limit", "GET", "/api/v1/incidents?limit=ten", "", 400, "validation_failed"},
		{"negative offset", "GET", "/api/v1/incidents?offset=-1", "", 400, "validation_failed"},
		{"non uuid id", "GET", "/api/v1/incidents/123", "", 400, "validation_failed"},
		{"unknown incident", "GET", "/api/v1/incidents/" + missing, "", 404, "not_found"},
		{"investigate unknown", "POST", "/api/v1/incidents/" + missing + "/investigate", "", 404, "not_found"},
		{"bad scenario", "POST", "/api/v1/incidents/" + id + "/investigate", `{"scenario":"nope"}`, 400, "validation_failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, out := do(t, tt.method, srv.URL+tt.path, tt.body)
			if resp.StatusCode != tt.status {
				t.Fatalf("status = %d, want %d (%v)", resp.StatusCode, tt.status, out)
			}
			e, _ := out["error"].(map[string]any)
			if e["code"] != tt.code || e["message"] == "" || out["request_id"] == "" {
				t.Fatalf("bad error body: %v", out)
			}
		})
	}
}

func TestBodyTooLarge(t *testing.T) {
	srv := newServer(t, llm.RulesProvider{}, nil)
	big := `{"description":"` + strings.Repeat("x", maxBodyBytes) + `"}`
	resp, err := http.Post(srv.URL+"/api/v1/incidents", "application/json", bytes.NewBufferString(big))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

func TestAnalysisFailureReturns502WithEvidence(t *testing.T) {
	srv := newServer(t, brokenProvider{}, nil)
	id := createIncident(t, srv)
	resp, out := do(t, "POST", srv.URL+"/api/v1/incidents/"+id+"/investigate", "")
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	result := out["result"].(map[string]any)
	if out["error"].(map[string]any)["code"] != "analysis_failed" || len(result["evidence"].([]any)) == 0 || result["report"] != nil {
		t.Fatalf("body = %v", out)
	}
}

func TestHealthReadyMetrics(t *testing.T) {
	srv := newServer(t, llm.RulesProvider{}, nil)
	if resp, out := do(t, "GET", srv.URL+"/health", ""); resp.StatusCode != 200 || out["status"] != "ok" {
		t.Fatalf("health: %d", resp.StatusCode)
	}
	if resp, _ := do(t, "GET", srv.URL+"/ready", ""); resp.StatusCode != 200 {
		t.Fatalf("ready: %d", resp.StatusCode)
	}
	down := newServer(t, llm.RulesProvider{}, errors.New("db down"))
	if resp, out := do(t, "GET", down.URL+"/ready", ""); resp.StatusCode != 503 || out["checks"].(map[string]any)["database"] != "unreachable" {
		t.Fatalf("not ready: %d %v", resp.StatusCode, out)
	}

	do(t, "GET", srv.URL+"/api/v1/incidents/"+domain.NewID(), "")
	resp, err := http.Get(srv.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	for _, want := range []string{`route="GET /api/v1/incidents/{id}"`, "http_request_duration_seconds", "incidents_total"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("metrics missing %s", want)
		}
	}
	if strings.Contains(string(body), "/api/v1/incidents/"+"0") {
		t.Error("raw path leaked into metric labels")
	}
}

func TestRequestIDs(t *testing.T) {
	srv := newServer(t, llm.RulesProvider{}, nil)
	resp, out := do(t, "GET", srv.URL+"/api/v1/incidents/bad", "", "X-Request-ID", "client-abc-123")
	if resp.Header.Get("X-Request-ID") != "client-abc-123" || out["request_id"] != "client-abc-123" {
		t.Fatalf("client request id not propagated: %v", out)
	}
	resp, _ = do(t, "GET", srv.URL+"/health", "", "X-Request-ID", "bad id with spaces")
	if got := resp.Header.Get("X-Request-ID"); got == "" || strings.ContainsAny(got, " \n") {
		t.Fatalf("unsafe request id accepted: %q", got)
	}
}

func TestSecurityHeaders(t *testing.T) {
	srv := newServer(t, llm.RulesProvider{}, nil)
	resp, _ := do(t, "GET", srv.URL+"/health", "")
	if resp.Header.Get("X-Content-Type-Options") != "nosniff" || !strings.Contains(resp.Header.Get("Content-Security-Policy"), "default-src 'none'") {
		t.Fatalf("headers: %v", resp.Header)
	}
	if resp, _ := do(t, "GET", srv.URL+"/nope", ""); resp.StatusCode != 404 {
		t.Errorf("unknown path: %d", resp.StatusCode)
	}
}

func TestRateLimit(t *testing.T) {
	srv := newServerRPS(t, llm.RulesProvider{}, nil, 1) // burst 2
	var limited bool
	for range 5 {
		resp, out := do(t, "GET", srv.URL+"/api/v1/incidents", "")
		if resp.StatusCode == http.StatusTooManyRequests {
			limited = resp.Header.Get("Retry-After") != "" && out["error"].(map[string]any)["code"] == "rate_limited"
			break
		}
	}
	if !limited {
		t.Fatal("rate limit not enforced")
	}
	// Probes are never limited.
	for range 5 {
		if resp, _ := do(t, "GET", srv.URL+"/health", ""); resp.StatusCode != 200 {
			t.Fatalf("health limited: %d", resp.StatusCode)
		}
	}
}
