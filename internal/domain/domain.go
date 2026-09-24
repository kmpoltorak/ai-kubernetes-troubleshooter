// Package domain defines the core entities of the troubleshooter. It has no
// dependencies on infrastructure packages.
package domain

import (
	"encoding/json"
	"errors"
	"time"
)

// ErrNotFound is returned by repositories when an entity does not exist.
var ErrNotFound = errors.New("not found")

type IncidentStatus string

const (
	IncidentOpen     IncidentStatus = "open"
	IncidentAnalyzed IncidentStatus = "analyzed"
)

// Incident is a reported problem with one workload. Target fields are
// flattened into the JSON object.
type Incident struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Target
	Status    IncidentStatus `json:"status"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
}

type InvestigationStatus string

const (
	InvestigationRunning   InvestigationStatus = "running"
	InvestigationCompleted InvestigationStatus = "completed"
	InvestigationFailed    InvestigationStatus = "failed"
)

type Investigation struct {
	ID          string              `json:"id"`
	IncidentID  string              `json:"incident_id"`
	Status      InvestigationStatus `json:"status"`
	Scenario    string              `json:"scenario,omitempty"`
	Error       string              `json:"error,omitempty"`
	StartedAt   time.Time           `json:"started_at"`
	CompletedAt *time.Time          `json:"completed_at,omitempty"`
}

type ToolExecutionStatus string

const (
	// ToolSucceeded means the tool ran and produced an observation (which may
	// itself describe a failing workload).
	ToolSucceeded ToolExecutionStatus = "succeeded"
	// ToolFailed means the tool could not observe anything (API error,
	// forbidden, timeout).
	ToolFailed ToolExecutionStatus = "failed"
)

type ToolExecution struct {
	ID         string              `json:"id"`
	ToolName   string              `json:"tool_name"`
	Status     ToolExecutionStatus `json:"status"`
	Input      json.RawMessage     `json:"input"`
	Output     json.RawMessage     `json:"output,omitempty"`
	Error      string              `json:"error,omitempty"`
	DurationMs int64               `json:"duration_ms"`
	StartedAt  time.Time           `json:"started_at"`
}

// Health is the condition a diagnostic observed.
type Health string

const (
	Healthy  Health = "healthy"
	Degraded Health = "degraded"
	Down     Health = "down"
)

// Evidence is the normalized observation derived from a tool execution. It
// is what the analyzer sees. Signals are stable machine-readable findings
// such as "OOMKilled" or "SelectorMismatch".
type Evidence struct {
	ID              string          `json:"id"`
	ToolExecutionID string          `json:"tool_execution_id"`
	Source          string          `json:"source"`
	Subject         string          `json:"subject"`
	Health          Health          `json:"health"`
	Summary         string          `json:"summary"`
	Signals         []string        `json:"signals"`
	Data            json.RawMessage `json:"data"`
}

// Report is a validated analysis and the remediation actions derived from it.
type Report struct {
	ID                 string              `json:"id"`
	Provider           string              `json:"provider"`
	Analysis           Analysis            `json:"analysis"`
	RemediationActions []RemediationAction `json:"remediation_actions"`
	CreatedAt          time.Time           `json:"created_at"`
}

// InvestigationRecord is an investigation with everything it produced.
// Report is nil for failed investigations.
type InvestigationRecord struct {
	Investigation  Investigation   `json:"investigation"`
	ToolExecutions []ToolExecution `json:"tool_executions"`
	Evidence       []Evidence      `json:"evidence"`
	Report         *Report         `json:"report,omitempty"`
}
