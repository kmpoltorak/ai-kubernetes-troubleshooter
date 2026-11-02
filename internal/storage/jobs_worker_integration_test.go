//go:build integration

package storage

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/domain"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/investigation"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/jobs"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/llm"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/simulation"
)

type disconnectRenew struct {
	pid    chan uint32
	resume chan struct{}
	ready  <-chan struct{}
	once   sync.Once
}

func (p *disconnectRenew) TraceQueryStart(ctx context.Context, conn *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if strings.HasPrefix(data.SQL, "UPDATE investigation_jobs SET lease_until=") {
		if p.ready != nil {
			select {
			case <-p.ready:
			default:
				return ctx
			}
		}
		p.once.Do(func() {
			p.pid <- conn.PgConn().PID()
			select {
			case <-p.resume:
			case <-ctx.Done():
			}
		})
	}
	return ctx
}
func (*disconnectRenew) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

type pauseFirstProgress struct {
	*investigation.Engine
	once             sync.Once
	ready, cancelled chan struct{}
}

func (p *pauseFirstProgress) Execute(ctx context.Context, inv domain.Investigation, save investigation.Progress) (domain.InvestigationRecord, error) {
	return p.Engine.Execute(ctx, inv, func(stepCtx context.Context, step domain.InvestigationRecord, stage string) error {
		if err := save(stepCtx, step, stage); err != nil {
			return err
		}
		if stage == "diagnostics" {
			first := false
			p.once.Do(func() { first = true })
			if first {
				close(p.ready)
				<-ctx.Done()
				close(p.cancelled)
				return ctx.Err()
			}
		}
		return nil
	})
}

func TestJobStopsOnDatabaseConnectionLoss(t *testing.T) {
	admin := testStore(t)
	j := queuedJob(t, admin)
	runner := &pauseFirstProgress{ready: make(chan struct{}), cancelled: make(chan struct{})}
	tracer := &disconnectRenew{pid: make(chan uint32, 1), resume: make(chan struct{}), ready: runner.ready}
	cfg, err := pgxpool.ParseConfig(admin.pool.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.Tracer = tracer
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	store := &Store{pool: pool}
	clusters, err := simulation.NewClusters("healthy")
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	engine := investigation.NewEngine(store, clusters, llm.RulesProvider{}, log, investigation.Config{Timeout: 5 * time.Second, MaxConcurrent: 1, Policy: domain.TargetPolicy{ClusterName: "local"}})
	runner.Engine = engine
	svc := jobs.New(store, runner, log, jobs.Config{MaxConcurrent: 1, Lease: time.Second, Poll: 10 * time.Millisecond})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); svc.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("worker did not stop")
		}
	})
	select {
	case <-runner.ready:
	case <-ctx.Done():
		t.Fatal("no checkpoint")
	}
	var pid uint32
	select {
	case pid = <-tracer.pid:
	case <-ctx.Done():
		t.Fatal("heartbeat did not start")
	}
	// Kill only the connection just identified by our own worker's tracer.
	var terminated bool
	if err := admin.pool.QueryRow(context.Background(), `SELECT pg_terminate_backend($1)`, pid).Scan(&terminated); err != nil || !terminated {
		t.Fatalf("connection not terminated: %t %v", terminated, err)
	}
	close(tracer.resume)
	select {
	case <-runner.cancelled:
	case <-ctx.Done():
		t.Fatal("worker continued after failed heartbeat")
	}
	result, err := svc.Wait(ctx, j.IncidentID, j.ID)
	if err != nil || result.Job.Status != domain.InvestigationCompleted || result.Job.Attempts != 2 || result.Result.Report == nil {
		history, readErr := admin.ListInvestigations(context.Background(), j.IncidentID, 10, 0)
		t.Fatalf("DB loss not recovered: job=%+v history=%+v error=%v read_error=%v", result.Job, history, err, readErr)
	}
	attempts, err := admin.ListInvestigations(ctx, j.IncidentID, 10, 0)
	if err != nil || len(attempts) != 2 {
		t.Fatalf("attempt history: %+v %v", attempts, err)
	}
	old, err := admin.GetInvestigation(ctx, j.IncidentID, attempts[1].ID)
	if err != nil || old.Investigation.Status != domain.InvestigationFailed || len(old.Evidence) != 1 {
		t.Fatalf("checkpoint lost: %+v %v", old, err)
	}
}

type finishDuringHeartbeat struct{ finish chan struct{} }

func (*finishDuringHeartbeat) Name() string { return "rules/rules-v1" }
func (p *finishDuringHeartbeat) Analyze(ctx context.Context, in llm.AnalysisInput) (domain.Analysis, error) {
	select {
	case <-p.finish:
		return (llm.RulesProvider{}).Analyze(ctx, in)
	case <-ctx.Done():
		return domain.Analysis{}, ctx.Err()
	}
}

func TestJobCompletionCanStopAnInFlightHeartbeat(t *testing.T) {
	admin := testStore(t)
	j := queuedJob(t, admin)
	tracer := &disconnectRenew{pid: make(chan uint32, 1), resume: make(chan struct{})}
	cfg, err := pgxpool.ParseConfig(admin.pool.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.Tracer = tracer
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	store := &Store{pool: pool}
	clusters, err := simulation.NewClusters("healthy")
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	provider := &finishDuringHeartbeat{finish: make(chan struct{})}
	engine := investigation.NewEngine(store, clusters, provider, log, investigation.Config{Timeout: 5 * time.Second, MaxConcurrent: 1, Policy: domain.TargetPolicy{ClusterName: "local"}})
	svc := jobs.New(store, engine, log, jobs.Config{MaxConcurrent: 1, Lease: 3 * time.Second, Poll: 10 * time.Millisecond})
	ctx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); svc.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("worker did not stop")
		}
	})
	select {
	case <-tracer.pid:
	case <-ctx.Done():
		t.Fatal("heartbeat did not start")
	}
	// Finishing the analyzer cancels a renewal paused before its SQL request.
	close(provider.finish)
	result, err := svc.Wait(ctx, j.IncidentID, j.ID)
	if err != nil || result.Job.Status != domain.InvestigationCompleted || result.Job.Attempts != 1 || result.Result.Report == nil {
		t.Fatalf("normal heartbeat shutdown lost result: %+v %v", result, err)
	}
}
