//go:build integration

package storage

import (
	"context"
	"slices"
	"testing"

	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/domain"
)

func TestDeleteIncidentsSkipsWorkInFlight(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	done, rec := purgeFixture(t, s, domain.InvestigationCompleted)
	fresh, _ := purgeFixture(t, s, "")
	running, _ := purgeFixture(t, s, domain.InvestigationRunning)
	job := queuedJob(t, s)
	// A job still queued after its attempt row finished is work in flight too.
	jobOnly := queuedJob(t, s)
	if _, err := s.pool.Exec(ctx, `UPDATE investigations SET status='failed', completed_at=now() WHERE incident_id=$1`, jobOnly.IncidentID); err != nil {
		t.Fatal(err)
	}
	missing := domain.NewID()

	deleted, err := s.DeleteIncidents(ctx, []string{done.ID, fresh.ID, running.ID, job.IncidentID, jobOnly.IncidentID, missing})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(deleted)
	want := []string{done.ID, fresh.ID}
	slices.Sort(want)
	if !slices.Equal(deleted, want) {
		t.Fatalf("deleted %v, want %v (running, queued and missing are skipped)", deleted, want)
	}
	var children int
	if err := s.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM investigations WHERE incident_id=$1) + (SELECT count(*) FROM reports WHERE investigation_id=$2)`,
		done.ID, rec.Investigation.ID).Scan(&children); err != nil || children != 0 {
		t.Fatalf("children left after delete: %d %v", children, err)
	}
	for _, id := range []string{running.ID, job.IncidentID, jobOnly.IncidentID} {
		if _, err := s.GetIncident(ctx, id); err != nil {
			t.Errorf("incident with work in flight was deleted: %s %v", id, err)
		}
	}
	if again, err := s.DeleteIncidents(ctx, []string{done.ID}); err != nil || len(again) != 0 {
		t.Fatalf("second delete: %v %v", again, err)
	}
}
