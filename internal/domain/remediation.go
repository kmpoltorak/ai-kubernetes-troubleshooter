package domain

type RemediationKind string

const (
	// RemediationRecommendation is a change an engineer may make; it always
	// requires human approval.
	RemediationRecommendation RemediationKind = "recommendation"
	// RemediationDiagnosticCommand is a validated read-only kubectl command.
	RemediationDiagnosticCommand RemediationKind = "diagnostic_command"
)

// RemediationAction is a proposed next step derived from a report. The
// application never executes it.
type RemediationAction struct {
	ID               string          `json:"id"`
	Position         int             `json:"position"`
	Kind             RemediationKind `json:"kind"`
	Description      string          `json:"description"`
	Command          string          `json:"command,omitempty"`
	RequiresApproval bool            `json:"requires_approval"`
	Status           string          `json:"status"`
}

// RemediationActions derives the ordered action list from a validated
// analysis: recommendations first, then read-only commands.
func RemediationActions(a Analysis) []RemediationAction {
	var out []RemediationAction
	add := func(kind RemediationKind, desc, cmd string) {
		out = append(out, RemediationAction{
			ID: NewID(), Position: len(out) + 1, Kind: kind, Description: desc, Command: cmd,
			RequiresApproval: kind == RemediationRecommendation, Status: "proposed",
		})
	}
	for _, r := range a.RecommendedActions {
		add(RemediationRecommendation, r, "")
	}
	for _, c := range a.SafeCommands {
		add(RemediationDiagnosticCommand, "Read-only diagnostic command", c)
	}
	return out
}
