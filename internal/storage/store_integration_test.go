//go:build integration

package storage

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/domain"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	s, err := Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	if _, err := s.MigrateUp(ctx); err != nil {
		t.Fatal(err)
	}
	return s
}

func testIncident() domain.Incident {
	return domain.Incident{
		Title: "payment-service restarting", Description: "Pods are repeatedly restarting",
		Target: domain.Target{Cluster: "local", Namespace: "payments", ResourceType: domain.ResourceDeployment, ResourceName: "payment-service"},
	}
}

func TestMigrateDownAndUp(t *testing.T) {
	s := isolatedStore(t)
	ctx := context.Background()
	if err := s.MigrateDown(ctx); err != nil {
		t.Fatalf("down: %v", err)
	}
	if n, err := s.MigrateUp(ctx); err != nil || n != 1 {
		t.Fatalf("up: applied=%d err=%v", n, err)
	}
	if n, err := s.MigrateUp(ctx); err != nil || n != 0 {
		t.Fatalf("second up should be a no-op: applied=%d err=%v", n, err)
	}
}

// New kinds need no migration; downgrading keeps them from violating the old constraint.
func TestResourceKindsMigration(t *testing.T) {
	s := isolatedStore(t)
	ctx := context.Background()
	in := testIncident()
	in.Target.ResourceType = domain.ResourceStatefulSet
	if err := s.CreateIncident(ctx, &in); err != nil {
		t.Fatalf("statefulset incident: %v", err)
	}
	if _, err := s.pool.Exec(ctx, `UPDATE incidents SET resource_type = 'Bad Kind' WHERE id = $1`, in.ID); err == nil {
		t.Fatal("database accepted a malformed kind")
	}
	if err := s.MigrateDown(ctx); err == nil || !strings.Contains(err.Error(), "other kinds") {
		t.Fatalf("downgrade with a statefulset incident: %v", err)
	}
	if got, err := s.GetIncident(ctx, in.ID); err != nil || got.Target.ResourceType != domain.ResourceStatefulSet {
		t.Fatalf("rejected downgrade changed data: %+v %v", got, err)
	}
}

func TestMigrationsRefuseNewerSchema(t *testing.T) {
	s := isolatedStore(t)
	ctx := context.Background()
	all, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	future := all[len(all)-1].version + 1
	if _, err := s.pool.Exec(ctx, `INSERT INTO schema_migrations (version) VALUES ($1)`, future); err != nil {
		t.Fatal(err)
	}
	if _, err := s.MigrateUp(ctx); err == nil || !strings.Contains(err.Error(), "newer than this binary") {
		t.Fatalf("up on a newer schema: %v", err)
	}
	if err := s.MigrateDown(ctx); err == nil || !strings.Contains(err.Error(), "newer than this binary") {
		t.Fatalf("down on a newer schema: %v", err)
	}
	var rows, latest int
	if err := s.pool.QueryRow(ctx, `SELECT count(*), max(version) FROM schema_migrations`).Scan(&rows, &latest); err != nil {
		t.Fatal(err)
	}
	if rows != len(all)+1 || latest != future {
		t.Fatalf("schema_migrations changed: rows=%d latest=%d", rows, latest)
	}
}

func TestIncidentRoundTrip(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	in := testIncident()
	if err := s.CreateIncident(ctx, &in); err != nil {
		t.Fatal(err)
	}
	if in.ID == "" || in.Status != domain.IncidentOpen || in.CreatedAt.IsZero() {
		t.Fatalf("defaults not returned: %+v", in)
	}

	got, err := s.GetIncident(ctx, in.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Target != in.Target || got.Title != in.Title || got.Description != in.Description {
		t.Fatalf("got %+v", got)
	}

	list, err := s.ListIncidents(ctx, 100, 0)
	if err != nil || len(list) == 0 {
		t.Fatalf("list: %v len=%d", err, len(list))
	}

	for _, id := range []string{domain.NewID(), "not-a-uuid"} {
		if _, err := s.GetIncident(ctx, id); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("GetIncident(%q) err = %v, want ErrNotFound", id, err)
		}
	}
}

func TestFinishInvestigationAndLatestReport(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	inc := testIncident()
	if err := s.CreateIncident(ctx, &inc); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LatestReport(ctx, inc.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("want ErrNotFound before any investigation, got %v", err)
	}

	inv := domain.Investigation{IncidentID: inc.ID, Status: domain.InvestigationRunning, Scenario: "oom_killed"}
	if err := s.CreateInvestigation(ctx, &inv); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	te := domain.ToolExecution{ID: domain.NewID(), ToolName: "get_pods", Status: domain.ToolSucceeded,
		Input: json.RawMessage(`{"namespace":"payments"}`), Output: json.RawMessage(`{"pods":[]}`), StartedAt: now}
	failed := domain.ToolExecution{ID: domain.NewID(), ToolName: "get_resource_usage", Status: domain.ToolFailed,
		Input: json.RawMessage(`{}`), Error: "metrics API unavailable", StartedAt: now.Add(time.Millisecond)}
	ev := domain.Evidence{ID: domain.NewID(), ToolExecutionID: te.ID, Source: "get_pods", Subject: "deployment/payment-service",
		Health: domain.Down, Summary: "1/1 pods OOMKilled", Signals: []string{"OOMKilled", "RestartLoop"}, Data: te.Output}
	analysis := domain.Analysis{Summary: "s", RootCause: "Container OOMKilled", Confidence: 0.94, Severity: domain.SeverityHigh,
		AffectedResources:  []string{"deployment/payment-service"},
		Evidence:           []domain.EvidenceReference{{Source: "get_pods", Description: "OOMKilled"}},
		RecommendedActions: []string{"raise the memory limit"}, SafeCommands: []string{"kubectl top pod -n payments"}}

	inv.Status = domain.InvestigationCompleted
	inv.CompletedAt = &now
	rec := domain.InvestigationRecord{
		Investigation:  inv,
		ToolExecutions: []domain.ToolExecution{te, failed},
		Evidence:       []domain.Evidence{ev},
		Report: &domain.Report{ID: domain.NewID(), Provider: "rules/rules-v1", Analysis: analysis,
			RemediationActions: domain.RemediationActions(analysis), CreatedAt: now},
	}
	if err := s.FinishInvestigation(ctx, rec); err != nil {
		t.Fatal(err)
	}

	got, err := s.LatestReport(ctx, inc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Investigation.Status != domain.InvestigationCompleted || got.Investigation.Scenario != "oom_killed" ||
		len(got.ToolExecutions) != 2 || len(got.Evidence) != 1 || got.Report == nil ||
		got.Report.Analysis.Confidence != 0.94 || got.Report.Analysis.RootCause != "Container OOMKilled" {
		t.Fatalf("unexpected record: %+v", got)
	}
	if sig := got.Evidence[0].Signals; len(sig) != 2 || sig[0] != "OOMKilled" || got.Evidence[0].Subject != ev.Subject {
		t.Fatalf("evidence not round-tripped: %+v", got.Evidence[0])
	}
	acts := got.Report.RemediationActions
	if len(acts) != 2 || acts[0].Kind != domain.RemediationRecommendation || !acts[0].RequiresApproval ||
		acts[1].Command != "kubectl top pod -n payments" {
		t.Fatalf("remediation actions: %+v", acts)
	}

	updated, _ := s.GetIncident(ctx, inc.ID)
	if updated.Status != domain.IncidentAnalyzed {
		t.Fatalf("incident status = %s, want analyzed", updated.Status)
	}
}

func TestStorageMasksWrites(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	inc := testIncident()
	inc.Title = "password=synthetic-title"
	inc.Description = "password=synthetic-description"
	if err := s.CreateIncident(ctx, &inc); err != nil {
		t.Fatal(err)
	}
	var title, description string
	if err := s.pool.QueryRow(ctx, "SELECT title, description FROM incidents WHERE id=$1", inc.ID).Scan(&title, &description); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(title+description, "synthetic-") {
		t.Fatalf("raw secret stored: %s %s", title, description)
	}
	inv := domain.Investigation{IncidentID: inc.ID, Status: domain.InvestigationRunning}
	if err := s.CreateInvestigation(ctx, &inv); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	inv.CompletedAt = &now
	inv.Status = domain.InvestigationFailed
	inv.Error = "password=synthetic-error"
	te := domain.ToolExecution{ID: domain.NewID(), ToolName: "get_pods", Status: domain.ToolSucceeded, StartedAt: now, Input: json.RawMessage(`{"password":"synthetic-input"}`), Output: json.RawMessage(`{"token":"synthetic-output","count":18446744073709551615}`)}
	ev := domain.Evidence{ID: domain.NewID(), ToolExecutionID: te.ID, Source: te.ToolName, Subject: "pods/app=api", Health: domain.Down, Summary: "password=synthetic-summary", Data: te.Output}
	rec := domain.InvestigationRecord{Investigation: inv, ToolExecutions: []domain.ToolExecution{te}, Evidence: []domain.Evidence{ev}}
	if err := s.FinishInvestigation(ctx, rec); err != nil {
		t.Fatal(err)
	}

	var output, input, data, summary, reason string
	if err := s.pool.QueryRow(ctx, `SELECT input::text,output::text FROM tool_executions WHERE id=$1`, te.ID).Scan(&input, &output); err != nil {
		t.Fatal(err)
	}
	if err := s.pool.QueryRow(ctx, `SELECT summary,data::text FROM evidence WHERE id=$1`, ev.ID).Scan(&summary, &data); err != nil {
		t.Fatal(err)
	}
	if err := s.pool.QueryRow(ctx, `SELECT error FROM investigations WHERE id=$1`, inv.ID).Scan(&reason); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(input+output+data+summary+reason, "synthetic-") || !strings.Contains(data, "18446744073709551615") {
		t.Fatalf("unsafe stored record: %s %s %s %s %s", input, output, data, summary, reason)
	}
}
