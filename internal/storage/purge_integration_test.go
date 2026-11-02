//go:build integration

package storage

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/domain"
)

var purgeBefore = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)

// Every fixture owns its rows. The old cutoff excludes other integration data.
func purgeFixture(t *testing.T, s *Store, status domain.InvestigationStatus) (domain.Incident, domain.InvestigationRecord) {
	t.Helper()
	ctx := context.Background()
	inc := testIncident()
	if err := s.CreateIncident(ctx, &inc); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := s.pool.Exec(context.Background(), `DELETE FROM incidents WHERE id=$1`, inc.ID); err != nil {
			t.Error(err)
		}
	})
	rec := domain.InvestigationRecord{}
	if status != "" {
		inv := domain.Investigation{IncidentID: inc.ID, Status: domain.InvestigationRunning}
		if err := s.CreateInvestigation(ctx, &inv); err != nil {
			t.Fatal(err)
		}
		rec.Investigation = inv
		if status != domain.InvestigationRunning {
			now := time.Now()
			rec.Investigation.Status = status
			rec.Investigation.CompletedAt = &now
			te := domain.ToolExecution{ID: domain.NewID(), ToolName: "get_pods", Status: domain.ToolSucceeded, StartedAt: now, Input: json.RawMessage(`{}`), Output: json.RawMessage(`{"total":1}`)}
			rec.ToolExecutions = []domain.ToolExecution{te}
			rec.Evidence = []domain.Evidence{{ID: domain.NewID(), ToolExecutionID: te.ID, Source: te.ToolName, Subject: "pod/api", Health: domain.Down, Summary: "test", Data: te.Output}}
			if status == domain.InvestigationCompleted {
				a := domain.Analysis{Summary: "test", RootCause: "test", Confidence: 0.8, Severity: domain.SeverityHigh, RecommendedActions: []string{"inspect workload"}, SafeCommands: []string{"kubectl get pods"}}
				rec.Report = &domain.Report{ID: domain.NewID(), Provider: "rules/rules-v1", Analysis: a, RemediationActions: domain.RemediationActions(a), CreatedAt: now}
			}
			if err := s.FinishInvestigation(ctx, rec); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := s.pool.Exec(ctx, `UPDATE investigations SET started_at=$2,completed_at=CASE WHEN status='running' THEN NULL ELSE $2::timestamptz END WHERE id=$1`, inv.ID, purgeBefore.Add(-time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.pool.Exec(ctx, `UPDATE incidents SET created_at=$2,updated_at=$2 WHERE id=$1`, inc.ID, purgeBefore.Add(-24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	return inc, rec
}

func mustPreview(t *testing.T, s *Store, limit int) PurgeResult {
	t.Helper()
	result, err := s.Purge(context.Background(), PurgeOptions{Before: purgeBefore, Limit: limit})
	if err != nil {
		t.Fatal(err)
	}
	if result.Deleted || len(result.Confirmation) != 64 {
		t.Fatalf("bad preview: %+v", result)
	}
	return result
}

func assertIncidentExists(t *testing.T, s *Store, id string, want bool) {
	t.Helper()
	_, err := s.GetIncident(context.Background(), id)
	if (want && err != nil) || (!want && !errors.Is(err, domain.ErrNotFound)) {
		t.Fatalf("incident %s exists=%t: %v", id, want, err)
	}
}

func TestPurgePreviewAndCascade(t *testing.T) {
	s := testStore(t)
	completed, rec := purgeFixture(t, s, domain.InvestigationCompleted)
	failed, _ := purgeFixture(t, s, domain.InvestigationFailed)
	running, _ := purgeFixture(t, s, domain.InvestigationRunning)
	preview := mustPreview(t, s, 100)
	if len(preview.Candidates) != 2 {
		t.Fatalf("candidates=%+v", preview.Candidates)
	}
	for _, c := range preview.Candidates {
		if c.Investigations != 1 || c.ToolExecutions != 1 || c.Evidence != 1 {
			t.Fatalf("missing child counts: %+v", c)
		}
		if c.ID == completed.ID && (c.Reports != 1 || c.RemediationActions != len(rec.Report.RemediationActions)) {
			t.Fatalf("missing report counts: %+v", c)
		}
		assertIncidentExists(t, s, c.ID, true)
	}
	_, err := s.Purge(context.Background(), PurgeOptions{Before: purgeBefore, Limit: 100, Confirm: strings.Repeat("0", 64)})
	if !errors.Is(err, ErrPurgeChanged) {
		t.Fatalf("invalid token accepted: %v", err)
	}
	assertIncidentExists(t, s, completed.ID, true)
	assertIncidentExists(t, s, failed.ID, true)
	// Equivalent time zones must not invalidate a preview.
	result, err := s.Purge(context.Background(), PurgeOptions{Before: purgeBefore.In(time.FixedZone("offset", 7200)), Limit: 100, Confirm: preview.Confirmation})
	if err != nil || !result.Deleted {
		t.Fatalf("purge: %+v %v", result, err)
	}
	assertIncidentExists(t, s, completed.ID, false)
	assertIncidentExists(t, s, failed.ID, false)
	assertIncidentExists(t, s, running.ID, true)
	for _, check := range []struct {
		sql string
		id  string
	}{
		{`SELECT count(*) FROM investigations WHERE incident_id=$1`, completed.ID},
		{`SELECT count(*) FROM investigations WHERE incident_id=$1`, failed.ID},
		{`SELECT count(*) FROM tool_executions WHERE investigation_id=$1`, rec.Investigation.ID},
		{`SELECT count(*) FROM evidence WHERE investigation_id=$1`, rec.Investigation.ID},
		{`SELECT count(*) FROM reports WHERE id=$1`, rec.Report.ID},
		{`SELECT count(*) FROM remediation_actions WHERE report_id=$1`, rec.Report.ID},
	} {
		var n int
		if err := s.pool.QueryRow(context.Background(), check.sql, check.id).Scan(&n); err != nil || n != 0 {
			t.Fatalf("cascade missing: %s count=%d %v", check.sql, n, err)
		}
	}
}

func TestPurgeEligibility(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	eligible, _ := purgeFixture(t, s, "")
	cases := []struct {
		name   string
		status domain.InvestigationStatus
		sql    string
	}{
		{"running", domain.InvestigationRunning, ""},
		{"running with old completion", domain.InvestigationRunning, `UPDATE investigations SET completed_at='1999-12-31' WHERE incident_id=$1`},
		{"missing completion", domain.InvestigationFailed, `UPDATE investigations SET completed_at=NULL WHERE incident_id=$1`},
		{"recent completion", domain.InvestigationFailed, `UPDATE investigations SET completed_at='2000-01-02' WHERE incident_id=$1`},
		{"completion at cutoff", domain.InvestigationFailed, `UPDATE investigations SET completed_at='2000-01-01' WHERE incident_id=$1`},
		{"start at cutoff", domain.InvestigationFailed, `UPDATE investigations SET started_at='2000-01-01' WHERE incident_id=$1`},
		{"new incident", "", `UPDATE incidents SET created_at='2000-01-01' WHERE id=$1`},
		{"recent activity", "", `UPDATE incidents SET updated_at='2000-01-01' WHERE id=$1`},
	}
	for _, tc := range cases {
		inc, _ := purgeFixture(t, s, tc.status)
		if tc.sql != "" {
			if _, err := s.pool.Exec(ctx, tc.sql, inc.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	preview := mustPreview(t, s, 100)
	if len(preview.Candidates) != 1 || preview.Candidates[0].ID != eligible.ID {
		t.Fatalf("active/recent incident eligible: %+v", preview.Candidates)
	}
}

func TestPurgeRejectsChangedSelection(t *testing.T) {
	for _, change := range []string{"start investigation", "new candidate", "change limit", "changed cutoff"} {
		t.Run(change, func(t *testing.T) {
			s := testStore(t)
			inc, _ := purgeFixture(t, s, "")
			preview := mustPreview(t, s, 100)
			o := PurgeOptions{Before: purgeBefore, Limit: 100, Confirm: preview.Confirmation}
			switch change {
			case "start investigation":
				inv := domain.Investigation{IncidentID: inc.ID, Status: domain.InvestigationRunning}
				if err := s.CreateInvestigation(context.Background(), &inv); err != nil {
					t.Fatal(err)
				}
			case "new candidate":
				purgeFixture(t, s, "")
			case "change limit":
				o.Limit = 1
			case "changed cutoff":
				o.Before = o.Before.Add(time.Second)
			}
			if _, err := s.Purge(context.Background(), o); !errors.Is(err, ErrPurgeChanged) {
				t.Fatalf("stale preview accepted: %v", err)
			}
			assertIncidentExists(t, s, inc.ID, true)
		})
	}
}

func TestPurgeBatchAndEmptySelection(t *testing.T) {
	s := testStore(t)
	ids := []string{}
	for range 3 {
		inc, _ := purgeFixture(t, s, "")
		ids = append(ids, inc.ID)
	}
	slices.Sort(ids)
	for _, id := range ids {
		p := mustPreview(t, s, 1)
		if len(p.Candidates) != 1 || p.Candidates[0].ID != id {
			t.Fatalf("unstable batch order: %+v", p)
		}
		if _, err := s.Purge(context.Background(), PurgeOptions{Before: purgeBefore, Limit: 1, Confirm: p.Confirmation}); err != nil {
			t.Fatal(err)
		}
	}
	p := mustPreview(t, s, 1)
	raw, _ := json.Marshal(p)
	if !strings.Contains(string(raw), `"candidates":[]`) {
		t.Fatalf("bad empty result: %s", raw)
	}
	if result, err := s.Purge(context.Background(), PurgeOptions{Before: purgeBefore, Limit: 1, Confirm: p.Confirmation}); err != nil || len(result.Candidates) != 0 {
		t.Fatalf("empty confirmation: %+v %v", result, err)
	}
}

func TestPurgeSkipsConcurrentInvestigation(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	inc, _ := purgeFixture(t, s, "")
	p := mustPreview(t, s, 100)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// The insert holds the parent KEY SHARE lock until the other connection commits.
	if _, err := tx.Exec(ctx, `INSERT INTO investigations(incident_id,status) VALUES($1,'running')`, inc.ID); err != nil {
		t.Fatal(err)
	}
	bounded, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if _, err := s.Purge(bounded, PurgeOptions{Before: purgeBefore, Limit: 100, Confirm: p.Confirmation}); !errors.Is(err, ErrPurgeChanged) {
		t.Fatalf("concurrent start not protected: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if got := mustPreview(t, s, 100); len(got.Candidates) != 0 {
		t.Fatalf("running investigation selected: %+v", got)
	}
	assertIncidentExists(t, s, inc.ID, true)
}

func TestPurgeRollsBackOnDeleteFailure(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	first, _ := purgeFixture(t, s, domain.InvestigationCompleted)
	second, _ := purgeFixture(t, s, domain.InvestigationFailed)
	preview := mustPreview(t, s, 100)
	// A statement trigger fails after row deletion/cascades have run.
	_, err := s.pool.Exec(ctx, `CREATE FUNCTION test_reject_purge() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'test delete failure'; END $$;
 CREATE TRIGGER test_reject_purge AFTER DELETE ON incidents FOR EACH STATEMENT EXECUTE FUNCTION test_reject_purge()`)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := s.pool.Exec(context.Background(), `DROP TRIGGER test_reject_purge ON incidents; DROP FUNCTION test_reject_purge()`); err != nil {
			t.Error(err)
		}
	})
	if _, err := s.Purge(ctx, PurgeOptions{Before: purgeBefore, Limit: 100, Confirm: preview.Confirmation}); err == nil {
		t.Fatal("delete failure ignored")
	}
	assertIncidentExists(t, s, first.ID, true)
	assertIncidentExists(t, s, second.ID, true)
	after := mustPreview(t, s, 100)
	if after.Confirmation != preview.Confirmation {
		t.Fatal("dependent data partially deleted")
	}
}

func TestPurgeCancelled(t *testing.T) {
	s := testStore(t)
	inc, _ := purgeFixture(t, s, "")
	p := mustPreview(t, s, 100)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Purge(ctx, PurgeOptions{Before: purgeBefore, Limit: 100, Confirm: p.Confirmation}); err == nil {
		t.Fatal("cancellation ignored")
	}
	assertIncidentExists(t, s, inc.ID, true)
}

func TestPurgeCancellationRollsBackCascades(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	inc, _ := purgeFixture(t, s, domain.InvestigationCompleted)
	preview := mustPreview(t, s, 100)
	_, err := s.pool.Exec(ctx, `CREATE FUNCTION test_pause_purge() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_sleep(10); RETURN NULL; END $$;
 CREATE TRIGGER test_pause_purge AFTER DELETE ON incidents FOR EACH STATEMENT EXECUTE FUNCTION test_pause_purge()`)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := s.pool.Exec(context.Background(), `DROP TRIGGER test_pause_purge ON incidents; DROP FUNCTION test_pause_purge()`); err != nil {
			t.Error(err)
		}
	})
	bounded, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := s.Purge(bounded, PurgeOptions{Before: purgeBefore, Limit: 100, Confirm: preview.Confirmation})
		done <- err
	}()
	// Wait for the trigger after DELETE, so cancellation tests a real rollback.
	observed := false
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event='PgSleep' AND query LIKE 'DELETE FROM incidents WHERE id=ANY%')`).Scan(&observed); err != nil {
			t.Fatal(err)
		}
		if observed {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if err := <-done; err == nil || !observed {
		t.Fatalf("did not interrupt DELETE: sleeping=%t error=%v", observed, err)
	}
	assertIncidentExists(t, s, inc.ID, true)
	if got := mustPreview(t, s, 100); got.Confirmation != preview.Confirmation {
		t.Fatal("cancelled DELETE lost dependent data")
	}
}

func TestPurgeRollsBackSuppressedDeletion(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	skipped, _ := purgeFixture(t, s, domain.InvestigationCompleted)
	deleted, _ := purgeFixture(t, s, domain.InvestigationFailed)
	if _, err := s.pool.Exec(ctx, `UPDATE incidents SET resource_name='skip-purge-test' WHERE id=$1`, skipped.ID); err != nil {
		t.Fatal(err)
	}
	preview := mustPreview(t, s, 100)
	_, err := s.pool.Exec(ctx, `CREATE FUNCTION test_skip_purge() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF OLD.resource_name='skip-purge-test' THEN RETURN NULL; END IF; RETURN OLD; END $$;
 CREATE TRIGGER test_skip_purge BEFORE DELETE ON incidents FOR EACH ROW EXECUTE FUNCTION test_skip_purge()`)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := s.pool.Exec(context.Background(), `DROP TRIGGER test_skip_purge ON incidents; DROP FUNCTION test_skip_purge()`); err != nil {
			t.Error(err)
		}
	})
	if _, err := s.Purge(ctx, PurgeOptions{Before: purgeBefore, Limit: 100, Confirm: preview.Confirmation}); err == nil {
		t.Fatal("partial deletion accepted")
	}
	assertIncidentExists(t, s, skipped.ID, true)
	assertIncidentExists(t, s, deleted.ID, true)
	if got := mustPreview(t, s, 100); got.Confirmation != preview.Confirmation {
		t.Fatal("partial DELETE committed")
	}
}

func TestPurgeProtectsJobsAndDeletesTerminalKeys(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	inc, _ := purgeFixture(t, s, "")
	key := domain.NewID()
	j, _, err := s.EnqueueJob(ctx, inc.ID, key, "", "healthy", 100, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if got := mustPreview(t, s, 100); len(got.Candidates) != 0 {
		t.Fatal("queued job selected for purge")
	}
	c := claimJob(t, s)
	if got := mustPreview(t, s, 100); len(got.Candidates) != 0 {
		t.Fatal("running job selected for purge")
	}
	if err := s.SaveProgress(ctx, c, jobStep(c), "diagnostics"); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveProgress(ctx, c, domain.InvestigationRecord{Investigation: c.Investigation}, "analyzing"); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteJob(ctx, c, completedJob(c), false, 100); err != nil {
		t.Fatal(err)
	}
	// Age the incident and attempt first: recent root job metadata must still protect it.
	if _, err := s.pool.Exec(ctx, `UPDATE incidents SET created_at='1999-01-01',updated_at='1999-01-01' WHERE id=$1`, inc.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `UPDATE investigations SET started_at='1999-01-01',completed_at='1999-01-01' WHERE job_id=$1`, j.ID); err != nil {
		t.Fatal(err)
	}
	if got := mustPreview(t, s, 100); len(got.Candidates) != 0 {
		t.Fatal("recent job selected for purge")
	}
	if _, err := s.pool.Exec(ctx, `UPDATE investigation_jobs SET created_at='1999-01-01',completed_at='1999-01-01' WHERE id=$1`, j.ID); err != nil {
		t.Fatal(err)
	}
	preview := mustPreview(t, s, 100)
	if len(preview.Candidates) != 1 || preview.Candidates[0].Jobs != 1 || preview.Candidates[0].Evidence != 1 {
		t.Fatalf("job counts: %+v", preview)
	}
	if _, err := s.Purge(ctx, PurgeOptions{Before: purgeBefore, Limit: 100, Confirm: preview.Confirmation}); err != nil {
		t.Fatal(err)
	}
	assertIncidentExists(t, s, inc.ID, false)
	if _, err := s.FindJob(ctx, inc.ID, key); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("idempotency key retained: %v", err)
	}
	if _, err := s.GetInvestigation(ctx, inc.ID, c.Investigation.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("job attempt retained: %v", err)
	}
}
