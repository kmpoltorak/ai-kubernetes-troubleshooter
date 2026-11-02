//go:build integration

package storage

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/domain"
)

type snapshotKey struct{}
type snapshotTracer struct {
	ready, resume chan struct{}
	once          sync.Once
}

func (p *snapshotTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if strings.HasPrefix(data.SQL, "SELECT "+investigationColumns) {
		return context.WithValue(ctx, snapshotKey{}, true)
	}
	return ctx
}
func (p *snapshotTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryEndData) {
	if ctx.Value(snapshotKey{}) == true {
		p.once.Do(func() {
			close(p.ready)
			select {
			case <-p.resume:
			case <-ctx.Done():
			}
		})
	}
}

func TestHistoryReadsOneSnapshot(t *testing.T) {
	s := testStore(t)
	inc, rec := purgeFixture(t, s, domain.InvestigationRunning)
	tracer := &snapshotTracer{ready: make(chan struct{}), resume: make(chan struct{})}
	cfg, err := pgxpool.ParseConfig(os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.Tracer = tracer
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	reader := &Store{pool: pool}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	type result struct {
		rec domain.InvestigationRecord
		err error
	}
	done := make(chan result, 1)
	go func() { r, err := reader.GetInvestigation(ctx, inc.ID, rec.Investigation.ID); done <- result{r, err} }()
	select {
	case <-tracer.ready:
	case <-ctx.Done():
		t.Fatal("header was not read")
	}
	// The real finish transaction commits between the header and child reads.
	now := time.Now()
	rec.Investigation.Status = domain.InvestigationCompleted
	rec.Investigation.CompletedAt = &now
	rec.Report = &domain.Report{ID: domain.NewID(), Provider: "rules/rules-v1", CreatedAt: now, Analysis: domain.Analysis{Summary: "test", RootCause: "test", Confidence: 0.8, Severity: domain.SeverityHigh}}
	if err := s.FinishInvestigation(ctx, rec); err != nil {
		t.Fatal(err)
	}
	close(tracer.resume)
	first := <-done
	if first.err != nil || first.rec.Investigation.Status != domain.InvestigationRunning || first.rec.Report != nil {
		t.Fatalf("mixed snapshots: %+v %v", first.rec, first.err)
	}
	next, err := s.GetInvestigation(ctx, inc.ID, rec.Investigation.ID)
	if err != nil || next.Investigation.Status != domain.InvestigationCompleted || next.Report == nil {
		t.Fatalf("new snapshot missing commit: %+v %v", next, err)
	}
}
