//go:build integration

package storage

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/domain"
)

func jobIncident(t *testing.T, s *Store) domain.Incident {
	t.Helper()
	inc := testIncident()
	if err := s.CreateIncident(context.Background(), &inc); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := s.pool.Exec(context.Background(), `DELETE FROM incidents WHERE id=$1`, inc.ID); err != nil {
			t.Error(err)
		}
	})
	return inc
}
func queuedJob(t *testing.T, s *Store) domain.Job {
	t.Helper()
	inc := jobIncident(t, s)
	j, created, err := s.EnqueueJob(context.Background(), inc.ID, domain.NewID(), "", "healthy", 100, time.Hour)
	if err != nil || !created {
		t.Fatalf("enqueue: %+v %t %v", j, created, err)
	}
	return j
}
func claimJob(t *testing.T, s *Store) domain.Claim {
	t.Helper()
	c, err := s.ClaimJob(context.Background(), 4, 100, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return c
}
func dueJob(t *testing.T, s *Store, j domain.Job) {
	t.Helper()
	if _, err := s.pool.Exec(context.Background(), `UPDATE investigation_jobs SET next_run_at=clock_timestamp()-interval '1 second' WHERE id=$1`, j.ID); err != nil {
		t.Fatal(err)
	}
}
func expireJob(t *testing.T, s *Store, c domain.Claim) {
	t.Helper()
	if _, err := s.pool.Exec(context.Background(), `UPDATE investigation_jobs SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, c.Job.ID); err != nil {
		t.Fatal(err)
	}
}
func jobStep(c domain.Claim) domain.InvestigationRecord {
	te := domain.ToolExecution{ID: domain.NewID(), ToolName: "get_namespace", Status: domain.ToolSucceeded, Input: json.RawMessage(`{}`), Output: json.RawMessage(`{"exists":true,"password":"synthetic-secret"}`), StartedAt: time.Now()}
	ev := domain.Evidence{ID: domain.NewID(), ToolExecutionID: te.ID, Source: te.ToolName, Subject: "namespace/payments", Health: domain.Healthy, Summary: "exists password=synthetic-secret", Data: te.Output}
	return domain.InvestigationRecord{Investigation: c.Investigation, ToolExecutions: []domain.ToolExecution{te}, Evidence: []domain.Evidence{ev}}
}
func completedJob(c domain.Claim) domain.InvestigationRecord {
	now := time.Now()
	inv := c.Investigation
	inv.Status = domain.InvestigationCompleted
	inv.CompletedAt = &now
	a := domain.Analysis{Summary: "exists", RootCause: "test", Confidence: 0.8, Severity: domain.SeverityLow, Evidence: []domain.EvidenceReference{{Source: "get_namespace", Description: "exists"}}, RecommendedActions: []string{"inspect workload"}, SafeCommands: []string{"kubectl get pods"}}
	return domain.InvestigationRecord{Investigation: inv, Report: &domain.Report{ID: domain.NewID(), Provider: "rules/rules-v1", Analysis: a, RemediationActions: domain.RemediationActions(a), CreatedAt: now}}
}

func TestJobsIdempotencyBeforeCapacity(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	inc := jobIncident(t, s)
	key := domain.NewID()
	first, created, err := s.EnqueueJob(ctx, inc.ID, key, "", "healthy", 1, time.Hour)
	if err != nil || !created {
		t.Fatal(err)
	}
	again, created, err := s.EnqueueJob(ctx, inc.ID, key, "", "healthy", 1, time.Hour)
	if err != nil || created || again.ID != first.ID {
		t.Fatalf("replay: %+v %t %v", again, created, err)
	}
	if _, _, err := s.EnqueueJob(ctx, inc.ID, key, "oom_killed", "oom_killed", 1, time.Hour); !errors.Is(err, domain.ErrIdempotencyConflict) {
		t.Fatalf("conflict: %v", err)
	}
	if _, _, err := s.EnqueueJob(ctx, inc.ID, domain.NewID(), "", "healthy", 1, time.Hour); !errors.Is(err, domain.ErrQueueFull) {
		t.Fatalf("capacity: %v", err)
	}
	rec, err := s.GetJob(ctx, inc.ID, first.ID)
	if err != nil || rec.Job.Attempts != 1 || rec.Result.Investigation.Status != domain.InvestigationQueued {
		t.Fatalf("queued record: %+v %v", rec, err)
	}
	other := jobIncident(t, s)
	if _, err := s.GetJob(ctx, other.ID, first.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("foreign job exposed: %v", err)
	}
}

func TestJobsConcurrentReplayAndGlobalClaims(t *testing.T) {
	s := testStore(t)
	peer := testStore(t)
	ctx := context.Background()
	inc := jobIncident(t, s)
	key := domain.NewID()
	type result struct {
		j       domain.Job
		created bool
		err     error
	}
	results := make(chan result, 8)
	var group sync.WaitGroup
	for i := range 8 {
		group.Go(func() {
			store := s
			if i%2 == 1 {
				store = peer
			}
			j, created, err := store.EnqueueJob(ctx, inc.ID, key, "", "healthy", 100, time.Hour)
			results <- result{j, created, err}
		})
	}
	group.Wait()
	close(results)
	id := ""
	created := 0
	for r := range results {
		if r.err != nil {
			t.Fatal(r.err)
		}
		if id == "" {
			id = r.j.ID
		}
		if r.j.ID != id {
			t.Fatal("duplicate jobs")
		}
		if r.created {
			created++
		}
	}
	if created != 1 {
		t.Fatalf("created=%d", created)
	}
	for range 3 {
		queuedJob(t, s)
	}
	claims := make(chan domain.Claim, 8)
	errs := make(chan error, 8)
	for i := range 8 {
		group.Go(func() {
			store := s
			if i%2 == 1 {
				store = peer
			}
			c, err := store.ClaimJob(ctx, 2, 100, 5*time.Second)
			if err == nil {
				claims <- c
			} else if !errors.Is(err, domain.ErrNotFound) {
				errs <- err
			}
		})
	}
	group.Wait()
	close(claims)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for c := range claims {
		if seen[c.Job.ID] {
			t.Fatal("job claimed twice")
		}
		seen[c.Job.ID] = true
	}
	_, active, err := s.JobCounts(ctx)
	if err != nil || active != 2 || len(seen) != 2 {
		t.Fatalf("global active=%d claims=%d %v", active, len(seen), err)
	}
}

func TestJobsRecoveryFencesOldOwnerAndKeepsSteps(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	j := queuedJob(t, s)
	old := claimJob(t, s)
	step := jobStep(old)
	if err := s.SaveProgress(ctx, old, step, "diagnostics"); err != nil {
		t.Fatal(err)
	}
	expireJob(t, s, old)
	if err := s.RenewJob(ctx, old, time.Second); !errors.Is(err, domain.ErrLeaseLost) {
		t.Fatalf("expired renewal: %v", err)
	}
	if _, err := s.ClaimJob(ctx, 1, 100, time.Second); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("backoff skipped: %v", err)
	}
	retry, err := s.GetJob(ctx, j.IncidentID, j.ID)
	if err != nil || retry.Job.Attempts != 2 || retry.Job.Status != domain.InvestigationQueued {
		t.Fatalf("recovery: %+v %v", retry, err)
	}
	previous, err := s.GetInvestigation(ctx, j.IncidentID, old.Investigation.ID)
	raw, _ := json.Marshal(previous)
	if err != nil || previous.Investigation.Status != domain.InvestigationFailed || len(previous.Evidence) != 1 || strings.Contains(string(raw), "synthetic-secret") {
		t.Fatalf("old evidence: %s %v", raw, err)
	}
	dueJob(t, s, j)
	current := claimJob(t, s)
	if current.Token == old.Token || current.Investigation.ID == old.Investigation.ID {
		t.Fatal("ownership reused")
	}
	if err := s.SaveProgress(ctx, old, step, "diagnostics"); !errors.Is(err, domain.ErrLeaseLost) {
		t.Fatalf("stale checkpoint: %v", err)
	}
	if err := s.CompleteJob(ctx, old, completedJob(old), false, 100); !errors.Is(err, domain.ErrLeaseLost) {
		t.Fatalf("stale report: %v", err)
	}
	if err := s.SaveProgress(ctx, current, jobStep(current), "diagnostics"); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveProgress(ctx, current, domain.InvestigationRecord{Investigation: current.Investigation}, "analyzing"); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteJob(ctx, current, completedJob(current), false, 100); err != nil {
		t.Fatal(err)
	}
	done, err := s.GetJob(ctx, j.IncidentID, j.ID)
	if err != nil || done.Job.Status != domain.InvestigationCompleted || done.Result.Report == nil || len(done.Result.Evidence) != 1 {
		t.Fatalf("complete: %+v %v", done, err)
	}
}

func TestJobsUnknownAnalysisIsNotRetried(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	j := queuedJob(t, s)
	c := claimJob(t, s)
	if err := s.SaveProgress(ctx, c, jobStep(c), "diagnostics"); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveProgress(ctx, c, domain.InvestigationRecord{Investigation: c.Investigation}, "analyzing"); err != nil {
		t.Fatal(err)
	}
	expireJob(t, s, c)
	if _, err := s.ClaimJob(ctx, 1, 100, time.Second); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("analysis reclaimed: %v", err)
	}
	done, err := s.GetJob(ctx, j.IncidentID, j.ID)
	if err != nil || done.Job.Status != domain.InvestigationFailed || done.Job.Attempts != 1 || !strings.Contains(done.Job.Error, "unknown") || len(done.Result.Evidence) != 1 {
		t.Fatalf("uncertain outcome: %+v %v", done, err)
	}
}

func TestJobsDeadlineAttemptLimitAndFullRetryQueue(t *testing.T) {
	t.Run("deadline", func(t *testing.T) {
		s := testStore(t)
		j := queuedJob(t, s)
		if _, err := s.pool.Exec(context.Background(), `UPDATE investigation_jobs SET deadline_at=clock_timestamp()-interval '1 second' WHERE id=$1`, j.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := s.ClaimJob(context.Background(), 1, 100, time.Second); !errors.Is(err, domain.ErrNotFound) {
			t.Fatal(err)
		}
		rec, err := s.GetJob(context.Background(), j.IncidentID, j.ID)
		if err != nil || rec.Job.Status != domain.InvestigationFailed || !strings.Contains(rec.Job.Error, "deadline") {
			t.Fatalf("deadline: %+v %v", rec, err)
		}
	})
	t.Run("three attempts", func(t *testing.T) {
		s := testStore(t)
		j := queuedJob(t, s)
		for attempt := 1; attempt <= 3; attempt++ {
			dueJob(t, s, j)
			c := claimJob(t, s)
			expireJob(t, s, c)
			if c.Job.Attempts != attempt {
				t.Fatalf("attempt=%d", c.Job.Attempts)
			}
			if _, err := s.ClaimJob(context.Background(), 1, 100, time.Second); !errors.Is(err, domain.ErrNotFound) {
				t.Fatalf("recovery backoff: %v", err)
			}
		}
		rec, err := s.GetJob(context.Background(), j.IncidentID, j.ID)
		if err != nil || rec.Job.Attempts != 3 || rec.Job.Status != domain.InvestigationFailed {
			t.Fatalf("attempt limit: %+v %v", rec, err)
		}
	})
	t.Run("full retry queue", func(t *testing.T) {
		s := testStore(t)
		j := queuedJob(t, s)
		c := claimJob(t, s)
		queuedJob(t, s)
		inv := c.Investigation
		inv.Status = domain.InvestigationFailed
		inv.Error = "temporary target failure"
		if err := s.CompleteJob(context.Background(), c, domain.InvestigationRecord{Investigation: inv}, true, 1); err != nil {
			t.Fatal(err)
		}
		rec, err := s.GetJob(context.Background(), j.IncidentID, j.ID)
		queued, _, countErr := s.JobCounts(context.Background())
		if err != nil || countErr != nil || queued != 1 || rec.Job.Status != domain.InvestigationFailed || !strings.Contains(rec.Job.Error, "queue full") {
			t.Fatalf("retry cap: %+v queued=%d %v %v", rec, queued, err, countErr)
		}
	})
}

func TestJobCheckpointIsAtomic(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	j := queuedJob(t, s)
	c := claimJob(t, s)
	step := jobStep(c)
	step.Evidence[0].ToolExecutionID = domain.NewID()
	if err := s.SaveProgress(ctx, c, step, "diagnostics"); err == nil {
		t.Fatal("invalid checkpoint accepted")
	}
	rec, err := s.GetJob(ctx, j.IncidentID, j.ID)
	if err != nil || len(rec.Result.ToolExecutions) != 0 || len(rec.Result.Evidence) != 0 {
		t.Fatalf("partial checkpoint: %+v %v", rec, err)
	}
}

func TestJobRejectsMismatchedResultAndUnstartedAnalysis(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	j := queuedJob(t, s)
	c := claimJob(t, s)
	for _, field := range []string{"incident", "job"} {
		step := jobStep(c)
		if field == "incident" {
			step.Investigation.IncidentID = domain.NewID()
		} else {
			step.Investigation.JobID = domain.NewID()
		}
		if err := s.SaveProgress(ctx, c, step, "diagnostics"); !errors.Is(err, domain.ErrLeaseLost) {
			t.Fatalf("mismatched %s checkpoint: %v", field, err)
		}
		rec := completedJob(c)
		rec.Investigation = step.Investigation
		rec.Investigation.Status = domain.InvestigationCompleted
		if err := s.CompleteJob(ctx, c, rec, false, 100); !errors.Is(err, domain.ErrLeaseLost) {
			t.Fatalf("mismatched %s completion: %v", field, err)
		}
	}
	if err := s.CompleteJob(ctx, c, completedJob(c), false, 100); err == nil {
		t.Fatal("report accepted before analyzing")
	}
	rec, err := s.GetJob(ctx, j.IncidentID, j.ID)
	if err != nil || rec.Result.Report != nil || len(rec.Result.ToolExecutions) != 0 || rec.Job.Stage != "diagnostics" {
		t.Fatalf("rejected result changed data: %+v %v", rec, err)
	}
}

func TestJobMigrationPreservesLegacyAndRejectsActiveDowngrade(t *testing.T) {
	s := isolatedStore(t)
	ctx := context.Background()
	// Roll back both report v2 and durable jobs to exercise the original schema.
	if err := s.MigrateDown(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.MigrateDown(ctx); err != nil {
		t.Fatal(err)
	}
	// The old schema can create an interrupted attempt without queue metadata.
	inc := jobIncident(t, s)
	legacy := domain.Investigation{IncidentID: inc.ID, Status: domain.InvestigationRunning}
	if err := s.CreateInvestigation(ctx, &legacy); err != nil {
		t.Fatal(err)
	}
	if _, err := s.MigrateUp(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetInvestigation(ctx, inc.ID, legacy.ID)
	if err != nil || got.Investigation.Status != domain.InvestigationRunning || got.Investigation.JobID != "" || !got.Investigation.StartedAt.Equal(legacy.StartedAt) {
		t.Fatalf("legacy row changed: %+v %v", got, err)
	}
	if _, err := s.ClaimJob(ctx, 1, 100, time.Second); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("legacy adopted: %v", err)
	}
	if err := s.MigrateDown(ctx); err != nil {
		t.Fatal(err)
	}
	j := queuedJob(t, s)
	if err := s.MigrateDown(ctx); err == nil || !strings.Contains(err.Error(), "active jobs") {
		t.Fatalf("active downgrade accepted: %v", err)
	}
	rec, err := s.GetJob(ctx, j.IncidentID, j.ID)
	if err != nil || rec.Job.Status != domain.InvestigationQueued {
		t.Fatalf("failed downgrade lost job: %+v %v", rec, err)
	}
}
