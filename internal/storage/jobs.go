package storage

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/domain"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/observability"
)

const jobLockID = 727_003
const jobColumns = `id,incident_id,status,stage,attempts,created_at,deadline_at,next_run_at,completed_at,error,scenario,requested_scenario`

func scanJob(row pgx.Row) (domain.Job, error) {
	var j domain.Job
	err := row.Scan(&j.ID, &j.IncidentID, &j.Status, &j.Stage, &j.Attempts, &j.CreatedAt, &j.DeadlineAt, &j.NextRunAt, &j.CompletedAt, &j.Error, &j.Scenario, &j.RequestedScenario)
	return j, err
}

func (s *Store) FindJob(ctx context.Context, incidentID, key string) (domain.Job, error) {
	j, err := scanJob(s.pool.QueryRow(ctx, `SELECT `+jobColumns+` FROM investigation_jobs WHERE incident_id=$1 AND idempotency_key=$2`, incidentID, key))
	return j, notFound(err)
}

func queuedAttempt(ctx context.Context, tx pgx.Tx, j domain.Job) error {
	_, err := tx.Exec(ctx, `INSERT INTO investigations(incident_id,job_id,execution_attempt,status,scenario) VALUES($1,$2,$3,'queued',$4)`, j.IncidentID, j.ID, j.Attempts, j.Scenario)
	return err
}

func (s *Store) EnqueueJob(ctx context.Context, incidentID, key, requested, scenario string, capacity int, timeout time.Duration) (domain.Job, bool, error) {
	var j domain.Job
	created := false
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, jobLockID); err != nil {
			return err
		}
		existing, err := scanJob(tx.QueryRow(ctx, `SELECT `+jobColumns+` FROM investigation_jobs WHERE incident_id=$1 AND idempotency_key=$2`, incidentID, key))
		if err == nil {
			if existing.RequestedScenario != requested {
				return domain.ErrIdempotencyConflict
			}
			j = existing
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		var parent string
		if err := tx.QueryRow(ctx, `SELECT id FROM incidents WHERE id=$1 FOR KEY SHARE`, incidentID).Scan(&parent); err != nil {
			return notFound(err)
		}
		var waiting int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM investigation_jobs WHERE status='queued'`).Scan(&waiting); err != nil {
			return err
		}
		if waiting >= capacity {
			return domain.ErrQueueFull
		}
		j, err = scanJob(tx.QueryRow(ctx, `INSERT INTO investigation_jobs(incident_id,idempotency_key,requested_scenario,scenario,deadline_at)
   VALUES($1,$2,$3,$4,clock_timestamp()+$5::bigint*interval '1 millisecond') RETURNING `+jobColumns, incidentID, key, requested, scenario, timeout.Milliseconds()))
		if err != nil {
			return err
		}
		created = true
		return queuedAttempt(ctx, tx, j)
	})
	return j, created, err
}

func (s *Store) GetJobState(ctx context.Context, incidentID, id string) (domain.Job, error) {
	job, err := scanJob(s.pool.QueryRow(ctx, `SELECT `+jobColumns+` FROM investigation_jobs WHERE incident_id=$1 AND id=$2`, incidentID, id))
	return job, notFound(err)
}

func (s *Store) GetJob(ctx context.Context, incidentID, id string) (domain.JobRecord, error) {
	var record domain.JobRecord
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		var err error
		record.Job, err = scanJob(tx.QueryRow(ctx, `SELECT `+jobColumns+` FROM investigation_jobs WHERE incident_id=$1 AND id=$2`, incidentID, id))
		if err != nil {
			return notFound(err)
		}
		var attemptID string
		if err := tx.QueryRow(ctx, `SELECT id FROM investigations WHERE job_id=$1 AND execution_attempt=$2`, id, record.Job.Attempts).Scan(&attemptID); err != nil {
			return err
		}
		record.Result, err = readInvestigation(ctx, tx, incidentID, attemptID)
		return err
	})
	return record, err
}

// retryJob runs under the queue lock. A full queue ends the failed job rather
// than silently exceeding the configured pending capacity during recovery.
func retryJob(ctx context.Context, tx pgx.Tx, j domain.Job, reason string, retry bool, capacity int, now time.Time) (bool, error) {
	if _, err := tx.Exec(ctx, `UPDATE investigations SET status='failed',error=$3,completed_at=$4 WHERE job_id=$1 AND execution_attempt=$2 AND status IN ('queued','running')`, j.ID, j.Attempts, reason, now); err != nil {
		return false, err
	}
	if retry && j.Attempts < 3 && now.Before(j.DeadlineAt) {
		var waiting int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM investigation_jobs WHERE status='queued'`).Scan(&waiting); err != nil {
			return false, err
		}
		if waiting < capacity {
			delay := time.Second * time.Duration(1<<(j.Attempts-1))
			j.Attempts++
			if _, err := tx.Exec(ctx, `UPDATE investigation_jobs SET status='queued',stage='queued',attempts=$2,next_run_at=$3,lease_token=NULL,lease_until=NULL,error='' WHERE id=$1`, j.ID, j.Attempts, now.Add(delay)); err != nil {
				return false, err
			}
			return true, queuedAttempt(ctx, tx, j)
		}
		reason = "retry queue full: " + reason
	}
	_, err := tx.Exec(ctx, `UPDATE investigation_jobs SET status='failed',stage='failed',error=$2,completed_at=$3,lease_token=NULL,lease_until=NULL WHERE id=$1`, j.ID, reason, now)
	return false, err
}

func (s *Store) ClaimJob(ctx context.Context, maxConcurrent, capacity int, lease time.Duration) (domain.Claim, error) {
	var claim domain.Claim
	recovered, failed := 0, 0
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, jobLockID); err != nil {
			return err
		}
		var now time.Time
		if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT `+jobColumns+` FROM investigation_jobs WHERE status IN ('queued','running') AND (deadline_at<=$1 OR (status='running' AND lease_until<=$1)) ORDER BY created_at,id FOR UPDATE`, now)
		if err != nil {
			return err
		}
		expired, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (domain.Job, error) { return scanJob(row) })
		if err != nil {
			return err
		}
		for _, j := range expired {
			reason, retry := "worker lease expired", j.Stage != "analyzing"
			if !now.Before(j.DeadlineAt) {
				reason, retry = "job deadline exceeded", false
			} else if j.Stage == "analyzing" {
				reason = "analysis outcome unknown after interruption; start a new job to retry"
			}
			again, err := retryJob(ctx, tx, j, reason, retry, capacity, now)
			if err != nil {
				return err
			}
			if again {
				recovered++
			} else {
				failed++
			}
		}
		var active int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM investigation_jobs WHERE status='running' AND lease_until>clock_timestamp() AND deadline_at>clock_timestamp()`).Scan(&active); err != nil {
			return err
		}
		if active >= maxConcurrent {
			return nil
		}
		j, err := scanJob(tx.QueryRow(ctx, `SELECT `+jobColumns+` FROM investigation_jobs WHERE status='queued' AND next_run_at<=clock_timestamp() AND deadline_at>clock_timestamp() ORDER BY created_at,id LIMIT 1 FOR UPDATE SKIP LOCKED`))
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		claim.Job, claim.Token = j, domain.NewID()
		claim.Job.Status, claim.Job.Stage = domain.InvestigationRunning, "diagnostics"
		if err := tx.QueryRow(ctx, `UPDATE investigation_jobs SET status='running',stage='diagnostics',lease_token=$2,lease_until=LEAST(deadline_at,clock_timestamp()+$3::bigint*interval '1 millisecond') WHERE id=$1 RETURNING lease_until`, j.ID, claim.Token, lease.Milliseconds()).Scan(&claim.LeaseUntil); err != nil {
			return err
		}
		claim.Investigation, err = scanInvestigation(tx.QueryRow(ctx, `UPDATE investigations SET status='running',started_at=clock_timestamp() WHERE job_id=$1 AND execution_attempt=$2 RETURNING `+investigationColumns, j.ID, j.Attempts))
		return err
	})
	if err != nil {
		return domain.Claim{}, err
	}
	if recovered+failed > 0 {
		observability.InvestigationsTotal.WithLabelValues("failed").Add(float64(recovered + failed))
	}
	if recovered > 0 {
		observability.JobRecoveriesTotal.WithLabelValues("retried").Add(float64(recovered))
	}
	if failed > 0 {
		observability.JobRecoveriesTotal.WithLabelValues("failed").Add(float64(failed))
		observability.JobsFinishedTotal.WithLabelValues("failed").Add(float64(failed))
	}
	if claim.Token == "" {
		return claim, domain.ErrNotFound
	}
	observability.JobWaitDuration.Observe(time.Since(claim.Job.CreatedAt).Seconds())
	return claim, nil
}

func ownedJob(ctx context.Context, tx pgx.Tx, c domain.Claim) error {
	var id string
	err := tx.QueryRow(ctx, `SELECT id FROM investigation_jobs WHERE id=$1 AND lease_token=$2 AND attempts=$3 AND status='running' AND lease_until>clock_timestamp() AND deadline_at>clock_timestamp() FOR UPDATE`, c.Job.ID, c.Token, c.Job.Attempts).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrLeaseLost
	}
	return err
}

func (s *Store) RenewJob(ctx context.Context, c domain.Claim, lease time.Duration) error {
	tag, err := s.pool.Exec(ctx, `UPDATE investigation_jobs SET lease_until=LEAST(deadline_at,clock_timestamp()+$4::bigint*interval '1 millisecond') WHERE id=$1 AND lease_token=$2 AND attempts=$3 AND status='running' AND lease_until>clock_timestamp() AND deadline_at>clock_timestamp()`, c.Job.ID, c.Token, c.Job.Attempts, lease.Milliseconds())
	if err == nil && tag.RowsAffected() != 1 {
		return domain.ErrLeaseLost
	}
	return err
}

func (s *Store) SaveProgress(ctx context.Context, c domain.Claim, rec domain.InvestigationRecord, stage string) error {
	if stage != "diagnostics" && stage != "analyzing" {
		return errors.New("invalid job stage")
	}
	if rec.Investigation.ID != c.Investigation.ID || rec.Investigation.IncidentID != c.Job.IncidentID || rec.Investigation.JobID != c.Job.ID {
		return domain.ErrLeaseLost
	}
	rec, err := rec.Redacted()
	if err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := ownedJob(ctx, tx, c); err != nil {
			return err
		}
		for _, te := range rec.ToolExecutions {
			if _, err := tx.Exec(ctx, `INSERT INTO tool_executions(id,investigation_id,tool_name,status,input,output,error,duration_ms,started_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, te.ID, c.Investigation.ID, te.ToolName, te.Status, te.Input, nullableJSON(te.Output), te.Error, te.DurationMs, te.StartedAt); err != nil {
				return err
			}
		}
		for _, ev := range rec.Evidence {
			if _, err := tx.Exec(ctx, `INSERT INTO evidence(id,investigation_id,tool_execution_id,position,source,subject,health,summary,signals,data) VALUES($1,$2,$3,(SELECT count(*) FROM evidence WHERE investigation_id=$2),$4,$5,$6,$7,$8,$9)`, ev.ID, c.Investigation.ID, ev.ToolExecutionID, ev.Source, ev.Subject, ev.Health, ev.Summary, nonNil(ev.Signals), ev.Data); err != nil {
				return err
			}
		}
		tag, err := tx.Exec(ctx, `UPDATE investigation_jobs SET stage=$4 WHERE id=$1 AND lease_token=$2 AND attempts=$3 AND stage='diagnostics' AND lease_until>clock_timestamp() AND deadline_at>clock_timestamp()`, c.Job.ID, c.Token, c.Job.Attempts, stage)
		if err == nil && tag.RowsAffected() != 1 {
			return domain.ErrLeaseLost
		}
		return err
	})
}

func (s *Store) CompleteJob(ctx context.Context, c domain.Claim, rec domain.InvestigationRecord, retry bool, capacity int) error {
	if rec.Investigation.ID != c.Investigation.ID || rec.Investigation.IncidentID != c.Job.IncidentID || rec.Investigation.JobID != c.Job.ID {
		return domain.ErrLeaseLost
	}
	if (rec.Investigation.Status != domain.InvestigationCompleted && rec.Investigation.Status != domain.InvestigationFailed) || (rec.Investigation.Status == domain.InvestigationCompleted) != (rec.Report != nil) {
		return errors.New("invalid completed attempt state")
	}
	rec, err := rec.Redacted()
	if err != nil {
		return err
	}
	again := false
	err = pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(tx pgx.Tx) error {
		// Match enqueue/recovery lock order before taking the job row lock.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, jobLockID); err != nil {
			return err
		}
		if err := ownedJob(ctx, tx, c); err != nil {
			return err
		}
		var stage string
		if err := tx.QueryRow(ctx, `SELECT stage FROM investigation_jobs WHERE id=$1`, c.Job.ID).Scan(&stage); err != nil {
			return err
		}
		if rec.Report != nil {
			if stage != "analyzing" {
				return errors.New("analysis was not started")
			}
			if err := insertReport(ctx, tx, rec.Investigation, rec.Report); err != nil {
				return err
			}
		}
		var now time.Time
		if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE investigations SET status=$2,error=$3,completed_at=$4 WHERE id=$1`, rec.Investigation.ID, rec.Investigation.Status, rec.Investigation.Error, now); err != nil {
			return err
		}
		if err := ownedJob(ctx, tx, c); err != nil {
			return err
		}
		if rec.Investigation.Status == domain.InvestigationFailed {
			var err error
			again, err = retryJob(ctx, tx, c.Job, rec.Investigation.Error, retry && stage != "analyzing", capacity, now)
			return err
		}
		tag, err := tx.Exec(ctx, `UPDATE investigation_jobs SET status='completed',stage='completed',completed_at=$2,error='',lease_token=NULL,lease_until=NULL WHERE id=$1 AND lease_until>clock_timestamp() AND deadline_at>clock_timestamp()`, c.Job.ID, now)
		if err == nil && tag.RowsAffected() != 1 {
			return domain.ErrLeaseLost
		}
		return err
	})
	if err == nil {
		observability.InvestigationsTotal.WithLabelValues(string(rec.Investigation.Status)).Inc()
		observability.InvestigationDuration.Observe(time.Since(rec.Investigation.StartedAt).Seconds())
		if again {
			observability.JobRecoveriesTotal.WithLabelValues("retried").Inc()
		} else {
			observability.JobsFinishedTotal.WithLabelValues(string(rec.Investigation.Status)).Inc()
		}
	}
	return err
}

func (s *Store) JobCounts(ctx context.Context) (queued, active int, err error) {
	err = s.pool.QueryRow(ctx, `SELECT count(*) FILTER(WHERE status='queued'),count(*) FILTER(WHERE status='running' AND lease_until>clock_timestamp() AND deadline_at>clock_timestamp()) FROM investigation_jobs WHERE status IN ('queued','running')`).Scan(&queued, &active)
	return
}
