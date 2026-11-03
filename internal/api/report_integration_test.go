//go:build integration

package api

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/domain"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/llm"
)

func TestIntegrationLegacyReportSurvivesNewV2Report(t *testing.T) {
	srv := newIntegrationServer(t, llm.RulesProvider{})
	pool := jobPool(t)
	id := createIncident(t, srv)
	removeJobIncident(t, pool, id)
	ctx := context.Background()
	invID := domain.NewID()
	reportID := domain.NewID()
	legacy := domain.Analysis{Summary: "historical assessment", RootCause: "old root cause", Confidence: 0.94, Severity: domain.SeverityHigh, Evidence: []domain.EvidenceReference{{Source: "get_pods", Description: "historical observation"}}}
	raw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO investigations(id,incident_id,status,started_at,completed_at) VALUES($1,$2,'completed','2000-01-01','2000-01-01')`, invID, id); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO reports(id,investigation_id,summary,root_cause,confidence,severity,analysis,provider,created_at) VALUES($1,$2,'historical assessment','old root cause',0.94,'high',$3,'legacy','2000-01-01')`, reportID, invID, raw); err != nil {
		t.Fatal(err)
	}
	var before string
	if err := pool.QueryRow(ctx, `SELECT analysis::text FROM reports WHERE id=$1`, reportID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/report", "/investigations/" + invID} {
		resp, out := do(t, "GET", srv.URL+"/api/v1/incidents/"+id+path, "")
		if resp.StatusCode != 200 {
			t.Fatalf("legacy status: %d %v", resp.StatusCode, out)
		}
		a := out["report"].(map[string]any)["analysis"].(map[string]any)
		if a["schema_version"] != nil || a["confidence"] != 0.94 || a["evidence"].([]any)[0].(map[string]any)["source"] != "get_pods" {
			t.Fatalf("legacy changed: %v", a)
		}
	}
	if resp, out := do(t, "POST", srv.URL+"/api/v1/incidents/"+id+"/investigate", ""); resp.StatusCode != 201 || out["report"].(map[string]any)["analysis"].(map[string]any)["schema_version"] != float64(2) {
		t.Fatalf("v2 failure: %d %v", resp.StatusCode, out)
	}
	resp, out := do(t, "GET", srv.URL+"/api/v1/incidents/"+id+"/investigations/"+invID, "")
	if resp.StatusCode != 200 || out["report"].(map[string]any)["analysis"].(map[string]any)["confidence"] != 0.94 {
		t.Fatalf("legacy hidden: %v", out)
	}
	var after string
	if err := pool.QueryRow(ctx, `SELECT analysis::text FROM reports WHERE id=$1`, reportID).Scan(&after); err != nil || after != before {
		t.Fatalf("read rewrote legacy: %v", err)
	}
}
