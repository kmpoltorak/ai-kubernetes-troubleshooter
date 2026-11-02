package domain

import (
	"errors"
	"time"

	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/redact"
)

var (
	ErrQueueFull           = errors.New("investigation queue is full")
	ErrIdempotencyConflict = errors.New("idempotency key already used with different options")
	ErrLeaseLost           = errors.New("job ownership or deadline expired")
)

type Job struct {
	ID                string              `json:"id"`
	IncidentID        string              `json:"incident_id"`
	Status            InvestigationStatus `json:"status"`
	Stage             string              `json:"stage"`
	Attempts          int                 `json:"attempts"`
	CreatedAt         time.Time           `json:"created_at"`
	DeadlineAt        time.Time           `json:"deadline_at"`
	NextRunAt         time.Time           `json:"next_run_at"`
	CompletedAt       *time.Time          `json:"completed_at,omitempty"`
	Error             string              `json:"error,omitempty"`
	Scenario          string              `json:"scenario,omitempty"`
	RequestedScenario string              `json:"-"`
}

// Claim is private ownership material; it is never an API response.
type Claim struct {
	Job           Job
	Investigation Investigation
	Token         string
	LeaseUntil    time.Time
}

type JobRecord struct {
	Replayed bool                `json:"-"`
	Job      Job                 `json:"job"`
	Result   InvestigationRecord `json:"result"`
}

func (r JobRecord) Redacted() (JobRecord, error) {
	r.Job.Error = redact.Text(r.Job.Error)
	var err error
	r.Result, err = r.Result.Redacted()
	return r, err
}
