//go:build integration

package api

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/domain"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/llm"
)

type historyProvider struct{ calls atomic.Int32 }

func (p *historyProvider) Name() string { return "test/history" }
func (p *historyProvider) Analyze(ctx context.Context, in llm.AnalysisInput) (domain.Analysis, error) {
	if p.calls.Add(1) == 1 {
		return (llm.RulesProvider{}).Analyze(ctx, in)
	}
	return domain.Analysis{}, errors.New("password=synthetic-provider")
}

func TestIntegrationHistoryKeepsFailedAttempts(t *testing.T) {
	srv := newIntegrationServer(t, &historyProvider{})
	id := createIncident(t, srv)
	resp, success := do(t, "POST", srv.URL+"/api/v1/incidents/"+id+"/investigate", "")
	if resp.StatusCode != 201 {
		t.Fatalf("success: %d %v", resp.StatusCode, success)
	}
	successID := success["investigation"].(map[string]any)["id"].(string)
	resp, failure := do(t, "POST", srv.URL+"/api/v1/incidents/"+id+"/investigate", "")
	if resp.StatusCode != 502 {
		t.Fatalf("failure: %d %v", resp.StatusCode, failure)
	}
	failedID := failure["result"].(map[string]any)["investigation"].(map[string]any)["id"].(string)
	pool, err := pgxpool.New(context.Background(), os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	// A legacy error must be masked on reads, without overwriting its stored value.
	if _, err := pool.Exec(context.Background(), `UPDATE investigations SET error='password=legacy-history-secret' WHERE id=$1`, failedID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(context.Background(), `UPDATE tool_executions SET error='password=legacy-tool-secret',output='{"password":"short-secret"}' WHERE investigation_id=$1`, failedID); err != nil {
		t.Fatal(err)
	}
	// A new server/store instance simulates a reload without an in-memory result.
	reloaded := newIntegrationServer(t, llm.RulesProvider{})
	resp, page := do(t, "GET", reloaded.URL+"/api/v1/incidents/"+id+"/investigations?limit=1", "")
	if resp.StatusCode != 200 || page["has_more"] != true || page["limit"] != float64(1) {
		t.Fatalf("page: %d %v", resp.StatusCode, page)
	}
	first := page["investigations"].([]any)[0].(map[string]any)
	if first["id"] != failedID || first["status"] != "failed" || first["started_at"] == nil || first["completed_at"] == nil || strings.Contains(first["error"].(string), "legacy-history-secret") {
		t.Fatalf("failed attempt hidden/unsafe: %v", first)
	}
	resp, page = do(t, "GET", reloaded.URL+"/api/v1/incidents/"+id+"/investigations?limit=1&offset=1", "")
	if resp.StatusCode != 200 || page["has_more"] != false || len(page["investigations"].([]any)) != 1 || page["investigations"].([]any)[0].(map[string]any)["id"] != successID {
		t.Fatalf("older page: %d %v", resp.StatusCode, page)
	}
	resp, detail := do(t, "GET", reloaded.URL+"/api/v1/incidents/"+id+"/investigations/"+failedID, "")
	raw, _ := json.Marshal(detail)
	if resp.StatusCode != 200 || detail["report"] != nil || len(detail["evidence"].([]any)) == 0 || len(detail["tool_executions"].([]any)) == 0 || strings.Contains(string(raw), "legacy-history-secret") || strings.Contains(string(raw), "legacy-tool-secret") || strings.Contains(string(raw), "short-secret") {
		t.Fatalf("failed detail: %d %s", resp.StatusCode, raw)
	}
	resp, last := do(t, "GET", reloaded.URL+"/api/v1/incidents/"+id+"/report", "")
	if resp.StatusCode != 200 || last["investigation"].(map[string]any)["id"] != successID || last["report"].(map[string]any)["created_at"] == nil {
		t.Fatalf("last report changed: %d %v", resp.StatusCode, last)
	}
	var stored string
	if err := pool.QueryRow(context.Background(), `SELECT error FROM investigations WHERE id=$1`, failedID).Scan(&stored); err != nil || stored != "password=legacy-history-secret" {
		t.Fatalf("legacy row overwritten: %q %v", stored, err)
	}
	other := createIncident(t, srv)
	for _, path := range []string{"/" + other + "/investigations/" + failedID, "/" + id + "/investigations/" + domain.NewID()} {
		if resp, out := do(t, "GET", reloaded.URL+"/api/v1/incidents"+path, ""); resp.StatusCode != 404 {
			t.Fatalf("foreign/missing attempt: %d %v", resp.StatusCode, out)
		}
	}
}

func TestIntegrationHistoryRunningAndPagination(t *testing.T) {
	srv := newIntegrationServer(t, llm.RulesProvider{})
	id := createIncident(t, srv)
	resp, page := do(t, "GET", srv.URL+"/api/v1/incidents/"+id+"/investigations", "")
	if resp.StatusCode != 200 || len(page["investigations"].([]any)) != 0 || page["has_more"] != false || page["limit"] != float64(20) {
		t.Fatalf("empty: %d %v", resp.StatusCode, page)
	}
	pool, err := pgxpool.New(context.Background(), os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	ids := []string{domain.NewID(), domain.NewID(), domain.NewID()}
	// Same timestamps require an ID tie-breaker to keep pages deterministic.
	for _, invID := range ids {
		if _, err := pool.Exec(context.Background(), `INSERT INTO investigations(id,incident_id,status,started_at) VALUES($1,$2,'running','2000-01-01')`, invID, id); err != nil {
			t.Fatal(err)
		}
	}
	slices.Sort(ids)
	slices.Reverse(ids)
	for offset, wantID := range ids {
		resp, page := do(t, "GET", srv.URL+"/api/v1/incidents/"+id+"/investigations?limit=1&offset="+strconv.Itoa(offset), "")
		if resp.StatusCode != 200 || len(page["investigations"].([]any)) != 1 || page["investigations"].([]any)[0].(map[string]any)["id"] != wantID || page["has_more"] != (offset < 2) {
			t.Fatalf("page %d: %d %v", offset, resp.StatusCode, page)
		}
	}
	resp, detail := do(t, "GET", srv.URL+"/api/v1/incidents/"+id+"/investigations/"+ids[0], "")
	if resp.StatusCode != 200 || detail["report"] != nil || detail["investigation"].(map[string]any)["status"] != "running" || len(detail["evidence"].([]any)) != 0 || len(detail["tool_executions"].([]any)) != 0 {
		t.Fatalf("running: %d %v", resp.StatusCode, detail)
	}
	if resp, _ := do(t, "GET", srv.URL+"/api/v1/incidents/"+id+"/report", ""); resp.StatusCode != 404 {
		t.Fatalf("running returned a report: %d", resp.StatusCode)
	}
	// Even a legacy completed row without a report stays readable in history.
	if _, err := pool.Exec(context.Background(), `UPDATE investigations SET status='completed',completed_at=now() WHERE id=$1`, ids[0]); err != nil {
		t.Fatal(err)
	}
	if resp, detail := do(t, "GET", srv.URL+"/api/v1/incidents/"+id+"/investigations/"+ids[0], ""); resp.StatusCode != 200 || detail["report"] != nil {
		t.Fatalf("completed without report: %d %v", resp.StatusCode, detail)
	}
	if resp, _ := do(t, "GET", srv.URL+"/api/v1/incidents/"+id+"/report", ""); resp.StatusCode != 404 {
		t.Fatalf("missing report returned: %d", resp.StatusCode)
	}
}
