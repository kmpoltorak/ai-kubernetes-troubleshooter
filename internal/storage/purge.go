package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// PurgeOptions always previews unless Confirm matches the current selection.
type PurgeOptions struct {
	Before  time.Time `json:"before"`
	Limit   int       `json:"limit"`
	Confirm string    `json:"-"`
}

func (o PurgeOptions) Validate() error {
	if o.Before.IsZero() || o.Before.After(time.Now()) {
		return errors.New("before must be a non-future RFC3339 timestamp")
	}
	if o.Limit < 1 || o.Limit > 1000 {
		return errors.New("limit must be between 1 and 1000")
	}
	if o.Confirm != "" {
		decoded, err := hex.DecodeString(o.Confirm)
		if err != nil || len(decoded) != sha256.Size {
			return errors.New("confirm must be the SHA-256 token returned by a preview")
		}
	}
	return nil
}

type PurgeCandidate struct {
	Jobs               int       `json:"jobs"`
	ID                 string    `json:"id"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
	Investigations     int       `json:"investigations"`
	ToolExecutions     int       `json:"tool_executions"`
	Evidence           int       `json:"evidence"`
	Reports            int       `json:"reports"`
	RemediationActions int       `json:"remediation_actions"`
}

type PurgeResult struct {
	Before       time.Time        `json:"before"`
	Limit        int              `json:"limit"`
	Candidates   []PurgeCandidate `json:"candidates"`
	Confirmation string           `json:"confirmation"`
	Deleted      bool             `json:"deleted"`
}

var ErrPurgeChanged = errors.New("purge selection changed or confirmation is invalid; run a new preview")

const purgeEligible = `i.created_at < $1 AND i.updated_at < $1 AND NOT EXISTS (
 SELECT 1 FROM investigations v WHERE v.incident_id=i.id AND
 (v.status IN ('queued','running') OR v.completed_at IS NULL OR v.started_at >= $1 OR v.completed_at >= $1)) AND NOT EXISTS (SELECT 1 FROM investigation_jobs j WHERE j.incident_id=i.id AND (j.status IN ('queued','running') OR j.created_at >= $1 OR j.completed_at IS NULL OR j.completed_at >= $1))`

// Purge atomically removes an explicitly confirmed, bounded set of incidents.
// Parent row locks prevent a new investigation's foreign-key check from racing
// deletion. A second statement sees children committed before those locks.
func (s *Store) Purge(ctx context.Context, o PurgeOptions) (PurgeResult, error) {
	result := PurgeResult{Before: o.Before.UTC(), Limit: o.Limit, Candidates: []PurgeCandidate{}}
	if err := o.Validate(); err != nil {
		return result, err
	}
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT i.id FROM incidents i WHERE `+purgeEligible+` ORDER BY i.created_at,i.id LIMIT $2 FOR UPDATE OF i SKIP LOCKED`, o.Before, o.Limit)
		if err != nil {
			return err
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		rows, err = tx.Query(ctx, `SELECT i.id,i.created_at,i.updated_at,
   (SELECT count(*) FROM investigations v WHERE v.incident_id=i.id),
   (SELECT count(*) FROM tool_executions t JOIN investigations v ON v.id=t.investigation_id WHERE v.incident_id=i.id),
   (SELECT count(*) FROM evidence e JOIN investigations v ON v.id=e.investigation_id WHERE v.incident_id=i.id),
   (SELECT count(*) FROM reports r JOIN investigations v ON v.id=r.investigation_id WHERE v.incident_id=i.id),
   (SELECT count(*) FROM remediation_actions a JOIN reports r ON r.id=a.report_id JOIN investigations v ON v.id=r.investigation_id WHERE v.incident_id=i.id),
 (SELECT count(*) FROM investigation_jobs j WHERE j.incident_id=i.id)
   FROM incidents i WHERE i.id=ANY($2::uuid[]) AND `+purgeEligible+` ORDER BY i.created_at,i.id`, o.Before, ids)
		if err != nil {
			return err
		}
		candidates, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (PurgeCandidate, error) {
			var c PurgeCandidate
			err := row.Scan(&c.ID, &c.CreatedAt, &c.UpdatedAt, &c.Investigations, &c.ToolExecutions, &c.Evidence, &c.Reports, &c.RemediationActions, &c.Jobs)
			c.CreatedAt, c.UpdatedAt = c.CreatedAt.UTC(), c.UpdatedAt.UTC()
			return c, err
		})
		if err != nil {
			return err
		}
		result.Candidates = append(result.Candidates, candidates...)
		// This fixed struct and sorted selection give previews a stable token.
		payload, err := json.Marshal(result)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(payload)
		result.Confirmation = hex.EncodeToString(sum[:])
		if o.Confirm == "" {
			return nil
		}
		if o.Confirm != result.Confirmation {
			return ErrPurgeChanged
		}
		ids = ids[:0]
		for _, candidate := range result.Candidates {
			ids = append(ids, candidate.ID)
		}
		tag, err := tx.Exec(ctx, `DELETE FROM incidents WHERE id=ANY($1::uuid[])`, ids)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != int64(len(ids)) {
			return errors.New("purge row count changed; rolled back")
		}
		return nil
	})
	if err != nil {
		return PurgeResult{}, fmt.Errorf("purge: %w", err)
	}
	result.Deleted = o.Confirm != ""
	return result, nil
}
