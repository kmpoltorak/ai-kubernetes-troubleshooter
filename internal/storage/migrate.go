package storage

import (
	"context"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/migrations"
)

// Arbitrary constant so concurrent replicas never migrate at the same time.
const migrationLockID = 727_002

type migration struct {
	version  int
	up, down string
}

// MigrateUp applies all pending migrations in a single transaction.
// It returns the number of migrations applied.
func (s *Store) MigrateUp(ctx context.Context) (int, error) {
	all, err := loadMigrations()
	if err != nil {
		return 0, err
	}
	applied := 0
	err = s.withMigrationLock(ctx, func(tx pgx.Tx, current int) error {
		for _, m := range all {
			if m.version <= current {
				continue
			}
			if _, err := tx.Exec(ctx, m.up); err != nil {
				return fmt.Errorf("apply migration %04d: %w", m.version, err)
			}
			if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version) VALUES ($1)`, m.version); err != nil {
				return err
			}
			applied++
		}
		return nil
	})
	return applied, err
}

// MigrateDown rolls back the most recently applied migration, if any.
func (s *Store) MigrateDown(ctx context.Context) error {
	all, err := loadMigrations()
	if err != nil {
		return err
	}
	return s.withMigrationLock(ctx, func(tx pgx.Tx, current int) error {
		for _, m := range all {
			if m.version != current {
				continue
			}
			if _, err := tx.Exec(ctx, m.down); err != nil {
				return fmt.Errorf("revert migration %04d: %w", m.version, err)
			}
			_, err := tx.Exec(ctx, `DELETE FROM schema_migrations WHERE version = $1`, m.version)
			return err
		}
		return nil
	})
}

func (s *Store) withMigrationLock(ctx context.Context, fn func(tx pgx.Tx, current int) error) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, migrationLockID); err != nil {
			return fmt.Errorf("acquire migration lock: %w", err)
		}
		if _, err := tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
			version integer PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
			return err
		}
		var current int
		if err := tx.QueryRow(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&current); err != nil {
			return err
		}
		return fn(tx, current)
	})
}

func loadMigrations() ([]migration, error) {
	byVersion := map[int]*migration{}
	err := fs.WalkDir(migrations.FS, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		prefix, _, ok := strings.Cut(name, "_")
		v, convErr := strconv.Atoi(prefix)
		if !ok || convErr != nil {
			return fmt.Errorf("migration %q: name must start with a numeric version", name)
		}
		body, err := fs.ReadFile(migrations.FS, name)
		if err != nil {
			return err
		}
		m := byVersion[v]
		if m == nil {
			m = &migration{version: v}
			byVersion[v] = m
		}
		switch {
		case strings.HasSuffix(name, ".up.sql"):
			m.up = string(body)
		case strings.HasSuffix(name, ".down.sql"):
			m.down = string(body)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	out := make([]migration, 0, len(byVersion))
	for _, m := range byVersion {
		if m.up == "" || m.down == "" {
			return nil, fmt.Errorf("migration %04d: both up and down files are required", m.version)
		}
		out = append(out, *m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	return out, nil
}
