package storage

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// deletable excludes incidents with work in flight, as purge does: queued or
// running investigations and jobs, and attempts that never completed.
const deletable = `NOT EXISTS (SELECT 1 FROM investigations v WHERE v.incident_id=i.id AND (v.status IN ('queued','running') OR v.completed_at IS NULL))
 AND NOT EXISTS (SELECT 1 FROM investigation_jobs j WHERE j.incident_id=i.id AND (j.status IN ('queued','running') OR j.completed_at IS NULL))`

// DeleteIncidents removes the given incidents and, by cascade, all their
// investigations, evidence, reports and jobs. Incidents with work in flight
// or that do not exist are left out of the result. Locked parents keep a new
// investigation from racing the delete; the second check sees children
// committed before the lock.
func (s *Store) DeleteIncidents(ctx context.Context, ids []string) ([]string, error) {
	var deleted []string
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT i.id FROM incidents i WHERE i.id=ANY($1::uuid[]) AND `+deletable+` FOR UPDATE OF i SKIP LOCKED`, ids)
		if err != nil {
			return err
		}
		locked, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		rows, err = tx.Query(ctx, `DELETE FROM incidents i WHERE i.id=ANY($1::uuid[]) AND `+deletable+` RETURNING i.id::text`, locked)
		if err != nil {
			return err
		}
		deleted, err = pgx.CollectRows(rows, pgx.RowTo[string])
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("delete incidents: %w", err)
	}
	return deleted, nil
}
