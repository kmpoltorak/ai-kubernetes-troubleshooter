package domain

import (
	"fmt"
	"slices"

	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/redact"
)

// Redacted returns a copy with masked free-form fields. The target must remain
// exact: changing it could investigate a different Kubernetes resource.
func (i Incident) Redacted() Incident {
	i.Title, i.Description = redact.Text(i.Title), redact.Text(i.Description)
	return i
}

func (i Investigation) Redacted() Investigation {
	i.Error = redact.Text(i.Error)
	return i
}

func (a Analysis) Redacted() Analysis {
	a.Summary, a.RootCause = redact.Text(a.Summary), redact.Text(a.RootCause)
	a.Evidence = slices.Clone(a.Evidence)
	for i := range a.Evidence {
		a.Evidence[i].Description = redact.Text(a.Evidence[i].Description)
	}
	a.AffectedResources = redactedStrings(a.AffectedResources)
	a.PossibleCauses = redactedStrings(a.PossibleCauses)
	a.RecommendedActions = redactedStrings(a.RecommendedActions)
	a.SafeCommands = redactedStrings(a.SafeCommands)
	return a
}

func redactedStrings(values []string) []string {
	values = slices.Clone(values)
	for i := range values {
		values[i] = redact.Text(values[i])
	}
	return values
}

// Redacted does not mutate a stored record or its slices, even in memory.
func (r InvestigationRecord) Redacted() (InvestigationRecord, error) {
	r.Investigation = r.Investigation.Redacted()
	r.ToolExecutions = slices.Clone(r.ToolExecutions)
	for i := range r.ToolExecutions {
		te := &r.ToolExecutions[i]
		te.Error = redact.Text(te.Error)
		var err error
		if te.Input, err = redact.JSON(te.Input); err != nil {
			return InvestigationRecord{}, fmt.Errorf("redact tool input: %w", err)
		}
		if te.Output, err = redact.JSON(te.Output); err != nil {
			return InvestigationRecord{}, fmt.Errorf("redact tool output: %w", err)
		}
	}
	r.Evidence = slices.Clone(r.Evidence)
	for i := range r.Evidence {
		ev := &r.Evidence[i]
		ev.Subject, ev.Summary = redact.Text(ev.Subject), redact.Text(ev.Summary)
		var err error
		if ev.Data, err = redact.JSON(ev.Data); err != nil {
			return InvestigationRecord{}, fmt.Errorf("redact evidence: %w", err)
		}
	}
	if r.Report != nil {
		report := *r.Report
		report.Analysis = report.Analysis.Redacted()
		report.RemediationActions = slices.Clone(report.RemediationActions)
		for i := range report.RemediationActions {
			a := &report.RemediationActions[i]
			a.Description, a.Command = redact.Text(a.Description), redact.Text(a.Command)
		}
		r.Report = &report
	}
	return r, nil
}
