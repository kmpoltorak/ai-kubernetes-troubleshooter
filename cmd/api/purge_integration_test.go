//go:build integration

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/domain"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/storage"
)

type brokenPurgeWriter struct{}

func (brokenPurgeWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestIntegrationPurgeCommand(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	store, err := storage.Connect(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	if _, err := store.MigrateUp(ctx); err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	for _, outputFails := range []bool{false, true} {
		inc := domain.Incident{Title: "password=synthetic-secret", Description: "test", Target: domain.Target{Cluster: "local", Namespace: "payments", ResourceType: domain.ResourcePod, ResourceName: "api"}}
		if err := store.CreateIncident(ctx, &inc); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if _, err := pool.Exec(context.Background(), `DELETE FROM incidents WHERE id=$1`, inc.ID); err != nil {
				t.Error(err)
			}
		})
		// This fixture predates the storage tests' cutoff and is removed here.
		if _, err := pool.Exec(ctx, `UPDATE incidents SET created_at='1899-01-01',updated_at='1899-01-01' WHERE id=$1`, inc.ID); err != nil {
			t.Fatal(err)
		}
		args := []string{"--before", "1900-01-01T00:00:00Z"}
		var out bytes.Buffer
		if err := purgeCommand(args, databaseURL, &out); err != nil {
			t.Fatal(err)
		}
		var preview storage.PurgeResult
		if err := json.Unmarshal(out.Bytes(), &preview); err != nil {
			t.Fatal(err)
		}
		if preview.Deleted || len(preview.Candidates) != 1 || preview.Candidates[0].ID != inc.ID || strings.Contains(out.String(), "synthetic-secret") {
			t.Fatalf("unsafe preview: %s", out.String())
		}
		if _, err := store.GetIncident(ctx, inc.ID); err != nil {
			t.Fatalf("preview deleted row: %v", err)
		}
		if err := purgeCommand(append(args, "--confirm", strings.Repeat("0", 64)), databaseURL, &out); !errors.Is(err, storage.ErrPurgeChanged) {
			t.Fatalf("bad confirmation: %v", err)
		}
		if err := purgeCommand(args, databaseURL, brokenPurgeWriter{}); err == nil || strings.Contains(err.Error(), "committed") {
			t.Fatalf("preview output failure: %v", err)
		}
		if _, err := store.GetIncident(ctx, inc.ID); err != nil {
			t.Fatal("failed preview removed incident")
		}
		args = append(args, "--confirm", preview.Confirmation)
		var writer io.Writer = &out
		if outputFails {
			writer = brokenPurgeWriter{}
		}
		out.Reset()
		err := purgeCommand(args, databaseURL, writer)
		if outputFails {
			if err == nil || !strings.Contains(err.Error(), "purge committed") {
				t.Fatalf("missing commit warning: %v", err)
			}
		} else {
			if err != nil {
				t.Fatal(err)
			}
			var deleted storage.PurgeResult
			if err := json.Unmarshal(out.Bytes(), &deleted); err != nil || !deleted.Deleted {
				t.Fatalf("bad deletion result: %s %v", out.String(), err)
			}
		}
		if _, err := store.GetIncident(ctx, inc.ID); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("incident not deleted: %v", err)
		}
	}
}
