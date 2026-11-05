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
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/config"
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
	records   map[string]domain.InvestigationRecord
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
	m.mu.Lock()
	defer m.mu.Unlock()
	inv.ID, inv.StartedAt = domain.NewID(), time.Now()
	if m.records == nil {
		m.records = map[string]domain.InvestigationRecord{}
	}
	m.records[inv.ID] = domain.InvestigationRecord{Investigation: *inv, ToolExecutions: []domain.ToolExecution{}, Evidence: []domain.Evidence{}}
	return nil
}

func (m *memStore) FinishInvestigation(_ context.Context, rec domain.InvestigationRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.records[rec.Investigation.ID] = rec
	if rec.Report != nil {
		m.reports[rec.Investigation.IncidentID] = rec
	}
	return nil
}

func (m *memStore) ListInvestigations(_ context.Context, id string, limit, offset int) ([]domain.Investigation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	list := []domain.Investigation{}
	for _, r := range m.records {
		if r.Investigation.IncidentID == id {
			list = append(list, r.Investigation)
		}
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].StartedAt.Equal(list[j].StartedAt) {
			return list[i].ID > list[j].ID
		}
		return list[i].StartedAt.After(list[j].StartedAt)
	})
	if offset >= len(list) {
		return []domain.Investigation{}, nil
	}
	return list[offset:min(len(list), offset+limit)], nil
}
func (m *memStore) GetInvestigation(_ context.Context, incidentID, id string) (domain.InvestigationRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.records[id]
	if !ok || r.Investigation.IncidentID != incidentID {
		return domain.InvestigationRecord{}, domain.ErrNotFound
	}
	return r, nil
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
	srv := httptest.NewServer(newHandler(t, provider, ready, rps, config.Auth{}))
	t.Cleanup(srv.Close)
	return srv
}

func newHandler(t *testing.T, provider llm.Provider, ready error, rps float64, auth config.Auth) http.Handler {
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
	return New(incidents.NewService(store, policy), &syncTestJobs{engine: engine}, checks, log, rps, auth, RuntimeFrom(config.Config{SimulationEnabled: true, ClusterName: "local", LLMProvider: "rules", LLMModel: "rules-v2"}), nil)
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

func TestRuntimeDescribesModeWithoutSecrets(t *testing.T) {
	secret := map[string]string{
		"DATABASE_URL": "postgres://app:db-secret@db.example.com/app", "OPENAI_API_KEY": "sk-runtime-secret",
		"OPENAI_BASE_URL": "https://llm.example.net/v1", "KUBECONFIG": "/var/run/secret-kubeconfig",
		"KUBERNETES_CONTEXT": "admin-context", "KUBERNETES_CLUSTER_NAME": "prod",
	}
	for _, tc := range []struct {
		name string
		env  map[string]string
		want string
	}{
		{"live", map[string]string{"SIMULATION_ENABLED": "false", "LLM_PROVIDER": "openai", "KUBERNETES_ALLOWED_NAMESPACES": "payments,checkout", "KUBERNETES_CUSTOM_RESOURCES": "argoproj.io/v1alpha1/rollouts"},
			`{"mode":"live","cluster_name":"prod","allowed_namespaces":["payments","checkout"],"analyzer":{"provider":"openai","model":"gpt-4o-mini"},"scenarios":[],"resource_types":["deployment","statefulset","daemonset","job","cronjob","service","pod","ingress","persistentvolumeclaim","horizontalpodautoscaler","rollouts.argoproj.io"]}`},
		{"simulation", map[string]string{},
			`{"mode":"simulation","cluster_name":"prod","allowed_namespaces":[],"analyzer":{"provider":"rules","model":"rules-v2"},"scenarios":["` + strings.Join(simulation.Scenarios, `","`) + `"],"resource_types":["deployment","statefulset","daemonset","job","cronjob","service","pod","ingress","persistentvolumeclaim","horizontalpodautoscaler"]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := config.Load(func(k string) string {
				if v, ok := tc.env[k]; ok {
					return v
				}
				return secret[k]
			})
			if err != nil {
				t.Fatal(err)
			}
			srv := httptest.NewServer(New(nil, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), 1000, config.Auth{}, RuntimeFrom(cfg), nil))
			t.Cleanup(srv.Close)
			resp, err := http.Get(srv.URL + "/api/v1/runtime")
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != 200 || strings.TrimSpace(string(body)) != tc.want {
				t.Fatalf("runtime %d:\n got %s\nwant %s", resp.StatusCode, body, tc.want)
			}
		})
	}
}

func TestProbeBuildMetadataIsPublicAndAdditive(t *testing.T) {
	for _, dependencyError := range []error{nil, errors.New("database down")} {
		srv := newServer(t, llm.RulesProvider{}, dependencyError)
		for _, path := range []string{"/health", "/ready"} {
			resp, out := do(t, "GET", srv.URL+path, "")
			wantCode, wantStatus := 200, "ok"
			if path == "/ready" {
				wantStatus = "ready"
				if dependencyError != nil {
					wantCode, wantStatus = 503, "unavailable"
				}
			}
			if resp.StatusCode != wantCode || out["status"] != wantStatus {
				t.Fatalf("probe contract changed: %s HTTP %d", path, resp.StatusCode)
			}
			build, ok := out["build"].(map[string]any)
			if !ok || len(build) != 3 || build["version"] != "dev" || build["revision"] != "unknown" || build["built_at"] != nil {
				t.Fatalf("unexpected default build: %v", build)
			}
			if path == "/ready" && out["checks"] == nil {
				t.Fatal("readiness checks lost")
			}
		}
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

func TestWebUI(t *testing.T) {
	srv := newServer(t, llm.RulesProvider{}, nil)
	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(resp.Header.Get("Content-Type"), "text/html") {
		t.Fatalf("/: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	csp := resp.Header.Get("Content-Security-Policy")
	if !strings.Contains(csp, "default-src 'self'") || !strings.Contains(csp, "frame-ancestors 'none'") ||
		resp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("security headers: %v", resp.Header)
	}
	// When the frontend is built, its hashed assets must be served too.
	if m := regexp.MustCompile(`/assets/[\w.-]+\.js`).Find(page); m != nil {
		if r, _ := do(t, "GET", srv.URL+string(m), ""); r.StatusCode != 200 {
			t.Fatalf("%s: %d", m, r.StatusCode)
		}
	}
	if resp, _ := do(t, "GET", srv.URL+"/nope", ""); resp.StatusCode != 404 {
		t.Errorf("unknown path: %d", resp.StatusCode)
	}
	if resp, _ := do(t, "GET", srv.URL+"/api/v1/incidents", ""); resp.Header.Get("Cache-Control") != "no-store" {
		t.Error("API responses must not be cached")
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

func TestAPIErrorMasksEchoedInput(t *testing.T) {
	srv := newServer(t, llm.RulesProvider{}, nil)
	resp, out := do(t, "POST", srv.URL+"/api/v1/incidents", `{"password=synthetic-secret":true}`)
	raw, _ := json.Marshal(out)
	if resp.StatusCode != 400 || strings.Contains(string(raw), "synthetic-secret") || out["error"].(map[string]any)["code"] != "invalid_json" {
		t.Fatalf("unsafe validation error: %d %s", resp.StatusCode, raw)
	}
}

func TestHistoryValidation(t *testing.T) {
	srv := newServer(t, llm.RulesProvider{}, nil)
	id := createIncident(t, srv)
	for _, path := range []string{
		"/bad/investigations", "/" + id + "/investigations/bad", "/" + id + "/investigations?offset=-1", "/" + id + "/investigations?limit=many", "/" + id + "/investigations?offset=9223372036854775808",
	} {
		resp, out := do(t, "GET", srv.URL+"/api/v1/incidents"+path, "")
		if resp.StatusCode != 400 || out["error"].(map[string]any)["code"] != "validation_failed" {
			t.Fatalf("%s: %d %v", path, resp.StatusCode, out)
		}
	}
	if resp, _ := do(t, "GET", srv.URL+"/api/v1/incidents/"+domain.NewID()+"/investigations", ""); resp.StatusCode != 404 {
		t.Fatalf("missing parent: %d", resp.StatusCode)
	}
}

func TestHistoryPageLimits(t *testing.T) {
	srv := newServer(t, llm.RulesProvider{}, nil)
	id := createIncident(t, srv)
	for _, tc := range []struct {
		query string
		want  float64
	}{{"", 20}, {"?limit=-1", 20}, {"?limit=1000", 100}} {
		resp, page := do(t, "GET", srv.URL+"/api/v1/incidents/"+id+"/investigations"+tc.query, "")
		if resp.StatusCode != 200 || page["limit"] != tc.want || page["has_more"] != false || len(page["investigations"].([]any)) != 0 {
			t.Fatalf("bad page %s: %d %v", tc.query, resp.StatusCode, page)
		}
	}
}

// Unit router tests run the synchronous in-memory runner. PostgreSQL integration
// tests exercise the actual durable service and workers.
type syncTestJobs struct {
	engine  *investigation.Engine
	mu      sync.Mutex
	records map[string]domain.JobRecord
}

func (s *syncTestJobs) Submit(ctx context.Context, id, _ string, opts investigation.Options) (domain.JobRecord, error) {
	rec, err := s.engine.Investigate(ctx, id, opts)
	if err != nil && !errors.Is(err, investigation.ErrAnalysisFailed) {
		return domain.JobRecord{}, err
	}
	job := domain.Job{ID: domain.NewID(), IncidentID: id, Status: rec.Investigation.Status, Error: rec.Investigation.Error}
	r := domain.JobRecord{Job: job, Result: rec}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.records == nil {
		s.records = map[string]domain.JobRecord{}
	}
	s.records[job.ID] = r
	return r, nil
}
func (s *syncTestJobs) Get(_ context.Context, incidentID, id string) (domain.JobRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.records[id]
	if !ok || r.Job.IncidentID != incidentID {
		return domain.JobRecord{}, domain.ErrNotFound
	}
	return r, nil
}
func (s *syncTestJobs) Wait(ctx context.Context, incidentID, id string) (domain.JobRecord, error) {
	return s.Get(ctx, incidentID, id)
}

func TestTargetsListsWithinPolicy(t *testing.T) {
	rt := Runtime{Mode: "live", ClusterName: "lab", AllowedNamespaces: []string{"payments"}, ResourceTypes: []domain.ResourceType{"deployment", "pod", "rollouts.argoproj.io"}}
	var asked []string
	list := func(_ context.Context, ns string, kind domain.ResourceType) ([]string, error) {
		asked = append(asked, ns+"/"+string(kind))
		if kind == domain.ResourcePod {
			return nil, errors.New("pods is forbidden: User \"system:serviceaccount:troubleshooter:troubleshooter\" cannot list")
		}
		return []string{"checkout", "ledger"}, nil
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(New(nil, nil, nil, log, 1000, config.Auth{}, rt, list))
	t.Cleanup(srv.Close)
	get := func(query string) (int, string) {
		resp, err := http.Get(srv.URL + "/api/v1/targets?" + query)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp.StatusCode, strings.TrimSpace(string(body))
	}
	if code, body := get("namespace=payments&kind=deployment"); code != 200 || body != `{"names":["checkout","ledger"]}` {
		t.Fatalf("list: %d %s", code, body)
	}
	if code, body := get("namespace=payments&kind=rollouts.argoproj.io"); code != 200 || body != `{"names":["checkout","ledger"]}` {
		t.Fatalf("configured custom resource: %d %s", code, body)
	}
	for _, q := range []string{"namespace=kube-system&kind=deployment", "namespace=payments&kind=secret", "kind=deployment", "namespace=Bad_NS&kind=pod", "namespace=payments&kind=widgets.example.com"} {
		if code, body := get(q); code != 400 || !strings.Contains(body, "validation_failed") {
			t.Errorf("%s: %d %s", q, code, body)
		}
	}
	if code, body := get("namespace=payments&kind=pod"); code != 502 || !strings.Contains(body, "list_failed") || !strings.Contains(body, "forbidden") {
		t.Fatalf("lister error: %d %s", code, body)
	}
	if len(asked) != 3 {
		t.Fatalf("lister called outside the policy: %v", asked)
	}
	sim := httptest.NewServer(New(nil, nil, nil, log, 1000, config.Auth{}, Runtime{Mode: "simulation", ClusterName: "local"}, nil))
	t.Cleanup(sim.Close)
	resp, err := http.Get(sim.URL + "/api/v1/targets?namespace=payments&kind=service")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || strings.TrimSpace(string(body)) != `{"names":[]}` {
		t.Fatalf("simulation: %d %s", resp.StatusCode, body)
	}
}
