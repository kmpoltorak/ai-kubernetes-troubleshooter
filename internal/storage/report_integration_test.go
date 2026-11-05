//go:build integration

package storage

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/domain"
)

// Migration tests own a schema so a rollback cannot touch other tests' reports.
func isolatedStore(t *testing.T) *Store {
	t.Helper()
	admin := testStore(t)
	ctx := context.Background()
	name := "test_" + strings.ReplaceAll(domain.NewID(), "-", "")
	quoted := pgx.Identifier{name}.Sanitize()
	if _, err := admin.pool.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.pool.Exec(ctx, "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Error(err)
		}
	})
	cfg := admin.pool.Config().Copy()
	cfg.ConnConfig.RuntimeParams["search_path"] = name
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	s := &Store{pool: pool}
	t.Cleanup(s.Close)
	if _, err := s.MigrateUp(ctx); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestReportV2RoundTripAndGuardedDowngrade(t *testing.T) {
	s := isolatedStore(t)
	ctx := context.Background()
	// Existing v1 rows and their JSON survive the schema change untouched.
	_, legacy := purgeFixture(t, s, domain.InvestigationCompleted)
	var before string
	if err := s.pool.QueryRow(ctx, `SELECT analysis::text FROM reports WHERE id=$1`, legacy.Report.ID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	migrateDownTo(t, s, 2)
	if _, err := s.MigrateUp(ctx); err != nil {
		t.Fatal(err)
	}
	j := queuedJob(t, s)
	c := claimJob(t, s)
	step := jobStep(c)
	if err := s.SaveProgress(ctx, c, step, "diagnostics"); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveProgress(ctx, c, domain.InvestigationRecord{Investigation: c.Investigation}, "analyzing"); err != nil {
		t.Fatal(err)
	}
	done := completedJob(c)
	a := &done.Report.Analysis
	a.SchemaVersion = 2
	a.Confidence = 0
	a.Verdict = "no_fault_observed"
	a.Support = "limited"
	a.Evidence = []domain.EvidenceReference{{EvidenceID: step.Evidence[0].ID, Source: step.Evidence[0].Source, Description: "exists"}}
	a.MissingData = []string{"password=synthetic-missing"}
	a.Limitations = []string{"token=synthetic-limit"}
	if err := s.CompleteJob(ctx, c, done, false, 100); err != nil {
		t.Fatal(err)
	}
	record, err := s.GetJob(ctx, j.IncidentID, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(record.Result.Report.Analysis)
	if err != nil {
		t.Fatal(err)
	}
	if record.Result.Report.Analysis.SchemaVersion != 2 || strings.Contains(string(raw), "confidence") || strings.Contains(string(raw), "synthetic-") || record.Result.Report.Analysis.Evidence[0].EvidenceID != step.Evidence[0].ID {
		t.Fatalf("v2 changed: %s", raw)
	}
	var null bool
	if err := s.pool.QueryRow(ctx, `SELECT confidence IS NULL FROM reports WHERE id=$1`, done.Report.ID).Scan(&null); err != nil || !null {
		t.Fatalf("invented score: null=%t %v", null, err)
	}
	migrateDownTo(t, s, 3)
	if err := s.MigrateDown(ctx); err == nil || !strings.Contains(err.Error(), "v2 reports") {
		t.Fatalf("v2 downgrade accepted: %v", err)
	}
	var after string
	var version int
	if err := s.pool.QueryRow(ctx, `SELECT analysis::text FROM reports WHERE id=$1`, legacy.Report.ID).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if err := s.pool.QueryRow(ctx, `SELECT max(version) FROM schema_migrations`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if before != after || version != 3 {
		t.Fatalf("downgrade modified data/schema: before=%s after=%s version=%d", before, after, version)
	}
	if _, err := s.GetJob(ctx, j.IncidentID, j.ID); err != nil {
		t.Fatalf("rejected downgrade lost v2: %v", err)
	}
}
