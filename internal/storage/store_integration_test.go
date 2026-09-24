//go:build integration

package storage

import (
	"context"
	"encoding/json"
	"errors"
	"os"
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
	s := testStore(t)
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
