package domain

import (
	"encoding/json"
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
	SchemaVersion      int                 `json:"schema_version,omitempty"`
	Verdict            string              `json:"verdict,omitempty"`
	Support            string              `json:"support,omitempty"`
	MissingData        []string            `json:"missing_data,omitempty"`
	Limitations        []string            `json:"limitations,omitempty"`
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
	EvidenceID  string `json:"evidence_id,omitempty"`
	Source      string `json:"source"`
	Description string `json:"description"`
}

// Legacy JSON retains confidence, including zero; v2 never publishes a score.
func (a Analysis) MarshalJSON() ([]byte, error) {
	type wire Analysis
	if a.SchemaVersion != 2 {
		return json.Marshal(wire(a))
	}
	return json.Marshal(struct {
		wire
		Confidence        *float64 `json:"confidence,omitempty"`
		MissingData       []string `json:"missing_data"`
		Limitations       []string `json:"limitations"`
		AffectedResources []string `json:"affected_resources"`
		PossibleCauses    []string `json:"possible_causes"`
		SafeCommands      []string `json:"safe_commands"`
	}{wire: wire(a), MissingData: append([]string{}, a.MissingData...), Limitations: append([]string{}, a.Limitations...),
		AffectedResources: append([]string{}, a.AffectedResources...), PossibleCauses: append([]string{}, a.PossibleCauses...), SafeCommands: append([]string{}, a.SafeCommands...)})
}

// HealthUnconfirmed checks observations, not the truth of an analyzer's prose.
func HealthUnconfirmed(evidence []Evidence, failed []ToolExecution) bool {
	for _, te := range failed {
		switch te.ToolName {
		// The target and its pods. References (configmaps, secrets, claims), nodes and metrics are optional.
		case "get_namespace", "get_deployment", "get_statefulset", "get_daemonset", "get_job", "get_cronjob", "get_ingress", "get_hpa", "get_custom_resource",
			"get_service", "get_pods", "get_events", "get_pod_logs":
			return true
		}
	}
	return len(evidence) == 0 || slices.ContainsFunc(evidence, func(e Evidence) bool { return e.Health != Healthy })
}

var resourceKind = regexp.MustCompile(`^[a-z]+$`)

// Validate admits new v2 reports only. Historical reports are read, not revalidated.
func (a Analysis) Validate(collected []Evidence, failed []ToolExecution) error {
	var errs []error
	if a.SchemaVersion != 2 {
		errs = append(errs, errors.New("schema_version must be 2"))
	}
	if a.Confidence != 0 {
		errs = append(errs, errors.New("confidence is not supported in v2"))
	}
	switch a.Verdict {
	case "observed_fault", "suspected_fault", "no_fault_observed", "inconclusive":
	default:
		errs = append(errs, errors.New("invalid verdict"))
	}
	switch a.Support {
	case "strong", "limited", "insufficient":
	default:
		errs = append(errs, errors.New("invalid support"))
	}
	if (a.Support == "strong" && a.Verdict != "observed_fault") || (a.Verdict == "inconclusive" && a.Support != "insufficient") {
		errs = append(errs, errors.New("support contradicts verdict"))
	}
	if a.Verdict == "no_fault_observed" && HealthUnconfirmed(collected, failed) {
		errs = append(errs, errors.New("no_fault_observed contradicts incomplete or unhealthy diagnostics"))
	}
	if strings.TrimSpace(a.Summary) == "" {
		errs = append(errs, errors.New("summary is empty"))
	}
	if strings.TrimSpace(a.RootCause) == "" {
		errs = append(errs, errors.New("root_cause is empty"))
	}
	switch a.Severity {
	case SeverityLow, SeverityMedium, SeverityHigh, SeverityCritical:
	default:
		errs = append(errs, fmt.Errorf("severity %q is invalid", a.Severity))
	}
	for _, r := range a.AffectedResources {
		kind, name, ok := strings.Cut(r, "/")
		if !ok || !resourceKind.MatchString(kind) && !ResourceType(kind).Custom() || ValidateObjectName(name) != nil {
			errs = append(errs, fmt.Errorf("affected resource %q must be kind/name", r))
		}
	}
	if len(a.Evidence) == 0 {
		errs = append(errs, errors.New("evidence is empty"))
	}
	observed := false
	for _, e := range a.Evidence {
		index := slices.IndexFunc(collected, func(item Evidence) bool { return item.ID == e.EvidenceID })
		if !IsUUID(e.EvidenceID) || index < 0 {
			errs = append(errs, fmt.Errorf("evidence_id %q was not collected in this attempt", e.EvidenceID))
		} else if collected[index].Source != e.Source {
			errs = append(errs, fmt.Errorf("evidence source %q does not match evidence_id", e.Source))
		} else {
			observed = observed || collected[index].Health != Healthy
		}
		if strings.TrimSpace(e.Description) == "" {
			errs = append(errs, fmt.Errorf("evidence from %q has empty description", e.Source))
		}
	}
	if a.Verdict == "observed_fault" && !observed {
		errs = append(errs, errors.New("observed_fault requires a cited unhealthy observation"))
	}
	// A fault needs a next step; nothing to fix needs no recommendation.
	hasAction := slices.ContainsFunc(a.RecommendedActions, func(s string) bool { return strings.TrimSpace(s) != "" })
	if a.Verdict != "no_fault_observed" && !hasAction {
		errs = append(errs, errors.New("recommended_actions is empty"))
	}
	if a.Verdict == "no_fault_observed" && len(a.RecommendedActions) > 0 {
		errs = append(errs, errors.New("no_fault_observed must not recommend actions"))
	}
	for _, c := range a.SafeCommands {
		if err := ValidateSafeCommand(c); err != nil {
			errs = append(errs, fmt.Errorf("safe command %q: %w", c, err))
		}
	}
	return errors.Join(errs...)
}
