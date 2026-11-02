// Package jobs owns durable scheduling. PostgreSQL owns leases and fencing;
// workers run only read-only diagnostics and never execute model commands.
package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/domain"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/investigation"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/observability"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

var ErrUnavailable = errors.New("job storage unavailable")

type Store interface {
	FindJob(context.Context, string, string) (domain.Job, error)
	EnqueueJob(context.Context, string, string, string, string, int, time.Duration) (domain.Job, bool, error)
	GetJob(context.Context, string, string) (domain.JobRecord, error)
	GetJobState(context.Context, string, string) (domain.Job, error)
	ClaimJob(context.Context, int, int, time.Duration) (domain.Claim, error)
	RenewJob(context.Context, domain.Claim, time.Duration) error
	SaveProgress(context.Context, domain.Claim, domain.InvestigationRecord, string) error
	CompleteJob(context.Context, domain.Claim, domain.InvestigationRecord, bool, int) error
	JobCounts(context.Context) (int, int, error)
}

type Runner interface {
	Validate(context.Context, string, investigation.Options) (string, error)
	Execute(context.Context, domain.Investigation, investigation.Progress) (domain.InvestigationRecord, error)
}

type Config struct {
	MaxConcurrent int
	Capacity      int
	Timeout       time.Duration
	Lease         time.Duration
	Poll          time.Duration
}

type Service struct {
	store  Store
	runner Runner
	log    *slog.Logger
	cfg    Config
}

func New(store Store, runner Runner, log *slog.Logger, cfg Config) *Service {
	if cfg.MaxConcurrent <= 0 {
		cfg.MaxConcurrent = 4
	}
	if cfg.Capacity <= 0 {
		cfg.Capacity = 100
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 15 * time.Minute
	}
	if cfg.Lease <= 0 {
		cfg.Lease = 30 * time.Second
	}
	if cfg.Poll <= 0 {
		cfg.Poll = 500 * time.Millisecond
	}
	return &Service{store: store, runner: runner, log: log, cfg: cfg}
}

func (s *Service) Submit(ctx context.Context, incidentID, key string, opts investigation.Options) (domain.JobRecord, error) {
	if !domain.IsUUID(key) {
		return domain.JobRecord{}, fmt.Errorf("%w: Idempotency-Key must be a UUID", investigation.ErrInvalidOptions)
	}
	key = strings.ToLower(key)
	existing, err := s.store.FindJob(ctx, incidentID, key)
	if err == nil {
		if existing.RequestedScenario != opts.Scenario {
			return domain.JobRecord{}, domain.ErrIdempotencyConflict
		}
		record, err := s.Get(ctx, incidentID, existing.ID)
		record.Replayed = true
		s.log.InfoContext(ctx, "job submitted", "job_id", existing.ID, "incident_id", incidentID, "replayed", true)
		return record, err
	}
	if !errors.Is(err, domain.ErrNotFound) {
		return domain.JobRecord{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	scenario, err := s.runner.Validate(ctx, incidentID, opts)
	if err != nil {
		if errors.Is(err, investigation.ErrInvalidOptions) || errors.Is(err, domain.ErrNotFound) {
			return domain.JobRecord{}, err
		}
		return domain.JobRecord{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	job, created, err := s.store.EnqueueJob(ctx, incidentID, key, opts.Scenario, scenario, s.cfg.Capacity, s.cfg.Timeout)
	if err != nil {
		if errors.Is(err, domain.ErrQueueFull) || errors.Is(err, domain.ErrIdempotencyConflict) || errors.Is(err, domain.ErrNotFound) {
			return domain.JobRecord{}, err
		}
		return domain.JobRecord{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	s.log.InfoContext(ctx, "job submitted", "job_id", job.ID, "incident_id", incidentID, "replayed", !created)
	record, err := s.Get(ctx, incidentID, job.ID)
	record.Replayed = !created
	return record, err
}

func (s *Service) Get(ctx context.Context, incidentID, id string) (domain.JobRecord, error) {
	r, err := s.store.GetJob(ctx, incidentID, id)
	if errors.Is(err, domain.ErrNotFound) {
		return r, err
	}
	if err != nil {
		return domain.JobRecord{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	return r.Redacted()
}

func (s *Service) Wait(ctx context.Context, incidentID, id string) (domain.JobRecord, error) {
	for {
		job, err := s.store.GetJobState(ctx, incidentID, id)
		if errors.Is(err, domain.ErrNotFound) {
			return domain.JobRecord{}, err
		}
		if err != nil {
			return domain.JobRecord{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
		}
		if job.Status == domain.InvestigationCompleted || job.Status == domain.InvestigationFailed {
			return s.Get(ctx, incidentID, id)
		}
		if !pause(ctx, s.cfg.Poll) {
			return domain.JobRecord{}, ctx.Err()
		}
	}
}

func pause(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// Run blocks until shutdown and all workers stop. HTTP request contexts never
// own workers; disconnecting a client cannot cancel an accepted job.
func (s *Service) Run(ctx context.Context) {
	var workers sync.WaitGroup
	for range s.cfg.MaxConcurrent {
		workers.Go(func() {
			backoff := s.cfg.Poll
			for ctx.Err() == nil {
				claim, err := s.store.ClaimJob(ctx, s.cfg.MaxConcurrent, s.cfg.Capacity, s.cfg.Lease)
				if err == nil {
					backoff = s.cfg.Poll
					s.runClaim(ctx, claim)
					continue
				}
				if ctx.Err() != nil {
					return
				}
				if !errors.Is(err, domain.ErrNotFound) {
					s.log.ErrorContext(ctx, "claim job", "error", err)
					backoff = min(10*time.Second, backoff*2)
				} else {
					backoff = s.cfg.Poll
				}
				if !pause(ctx, backoff) {
					return
				}
			}
		})
	}
	workers.Go(func() {
		for ctx.Err() == nil {
			queryCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			queued, active, err := s.store.JobCounts(queryCtx)
			cancel()
			if err == nil {
				observability.JobQueueDepth.Set(float64(queued))
				observability.JobsActive.Set(float64(active))
			}
			if !pause(ctx, 5*time.Second) {
				return
			}
		}
	})
	workers.Wait()
}

func (s *Service) runClaim(ctx context.Context, c domain.Claim) {
	workCtx, cancel := context.WithDeadline(ctx, c.Job.DeadlineAt)
	heartbeatDone := make(chan struct{})
	var leaseErr error
	go func() {
		defer close(heartbeatDone)
		for pause(workCtx, s.cfg.Lease/3) {
			renewCtx, stop := context.WithTimeout(workCtx, s.cfg.Lease/6)
			err := s.store.RenewJob(renewCtx, c, s.cfg.Lease)
			stop()
			if err != nil {
				// Normal completion also cancels an in-flight renewal. The final
				// transaction still checks ownership and deadline before saving.
				if workCtx.Err() == nil {
					leaseErr = err
					cancel()
				}
				return
			}
		}
	}()
	defer func() {
		cancel()
		<-heartbeatDone
		if v := recover(); v != nil {
			s.log.ErrorContext(ctx, "job worker panic", "job_id", c.Job.ID, "panic", v)
		}
	}()
	rec, runErr := s.runner.Execute(workCtx, c.Investigation, func(stepCtx context.Context, step domain.InvestigationRecord, stage string) error {
		return s.store.SaveProgress(stepCtx, c, step, stage)
	})
	cancel()
	<-heartbeatDone
	if ctx.Err() != nil || leaseErr != nil || errors.Is(runErr, investigation.ErrProgress) {
		s.log.WarnContext(ctx, "job ownership interrupted", "job_id", c.Job.ID, "error", errors.Join(leaseErr, runErr))
		return
	}
	var status apierrors.APIStatus
	var network net.Error
	retry := errors.Is(runErr, context.DeadlineExceeded) || (errors.As(runErr, &network) && network.Timeout()) || (errors.As(runErr, &status) && status.Status().Code >= 500 && status.Status().Code < 600) || apierrors.IsTooManyRequests(runErr) || apierrors.IsServerTimeout(runErr) || apierrors.IsTimeout(runErr) || apierrors.IsServiceUnavailable(runErr) || apierrors.IsInternalError(runErr)
	saveCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer stop()
	if err := s.store.CompleteJob(saveCtx, c, rec, retry, s.cfg.Capacity); err != nil {
		s.log.ErrorContext(ctx, "complete job", "job_id", c.Job.ID, "error", err)
	}
}
