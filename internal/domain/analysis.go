package domain

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

type Severity string

const (
	SeverityLow      Severity = "low"
	SeverityMedium   Severity = "medium"
	SeverityHigh     Severity = "high"
	SeverityCritical Severity = "critical"
)

// Analysis is the structured output of the analyzer.
type Analysis struct {
	Summary            string              `json:"summary"`
	RootCause          string              `json:"root_cause"`
	Confidence         float64             `json:"confidence"`
	Severity           Severity            `json:"severity"`
	AffectedResources  []string            `json:"affected_resources"`
	Evidence           []EvidenceReference `json:"evidence"`
	PossibleCauses     []string            `json:"possible_causes"`
	RecommendedActions []string            `json:"recommended_actions"`
	SafeCommands       []string            `json:"safe_commands"`
}

type EvidenceReference struct {
	Source      string `json:"source"`
	Description string `json:"description"`
}

var resourceKind = regexp.MustCompile(`^[a-z]+$`)

// Validate checks the analysis is complete, only cites evidence sources that
// were actually collected, and only suggests read-only commands, so a model
// cannot reference diagnostics that never ran or smuggle in a write action.
func (a Analysis) Validate(collectedSources []string) error {
	var errs []error
	if strings.TrimSpace(a.Summary) == "" {
		errs = append(errs, errors.New("summary is empty"))
	}
	if strings.TrimSpace(a.RootCause) == "" {
		errs = append(errs, errors.New("root_cause is empty"))
	}
	if a.Confidence < 0 || a.Confidence > 1 {
		errs = append(errs, fmt.Errorf("confidence %.2f is outside [0,1]", a.Confidence))
	}
	switch a.Severity {
	case SeverityLow, SeverityMedium, SeverityHigh, SeverityCritical:
	default:
		errs = append(errs, fmt.Errorf("severity %q is invalid", a.Severity))
	}
	for _, r := range a.AffectedResources {
		kind, name, ok := strings.Cut(r, "/")
		if !ok || !resourceKind.MatchString(kind) || ValidateObjectName(name) != nil {
			errs = append(errs, fmt.Errorf("affected resource %q must be kind/name", r))
		}
	}
	if len(a.Evidence) == 0 {
		errs = append(errs, errors.New("evidence is empty"))
	}
	for _, e := range a.Evidence {
		if !slices.Contains(collectedSources, e.Source) {
			errs = append(errs, fmt.Errorf("evidence source %q was not collected", e.Source))
		}
		if strings.TrimSpace(e.Description) == "" {
			errs = append(errs, fmt.Errorf("evidence from %q has empty description", e.Source))
		}
	}
	if !slices.ContainsFunc(a.RecommendedActions, func(s string) bool { return strings.TrimSpace(s) != "" }) {
		errs = append(errs, errors.New("recommended_actions is empty"))
	}
	for _, c := range a.SafeCommands {
		if err := ValidateSafeCommand(c); err != nil {
			errs = append(errs, fmt.Errorf("safe command %q: %w", c, err))
		}
	}
	return errors.Join(errs...)
}
