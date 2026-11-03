package investigation

import (
	"context"
	"strings"
	"testing"

	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/domain"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/llm"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/simulation"
)

// Expected root cause per scenario: the evaluation baseline for the rules
// analyzer, run end to end through the engine and the real tools.
var expected = map[string]struct {
	keyword          string
	severity         domain.Severity
	verdict, support string
}{
	"healthy":                   {"no kubernetes-level fault", domain.SeverityLow, "no_fault_observed", "limited"},
	"crash_loop_backoff":        {"crashloopbackoff", domain.SeverityCritical, "observed_fault", "strong"},
	"image_pull_error":          {"image", domain.SeverityHigh, "observed_fault", "strong"},
	"oom_killed":                {"oomkilled", domain.SeverityHigh, "observed_fault", "strong"},
	"readiness_probe_failure":   {"readiness", domain.SeverityHigh, "observed_fault", "strong"},
	"liveness_probe_failure":    {"liveness", domain.SeverityHigh, "observed_fault", "strong"},
	"missing_configmap":         {"configmap", domain.SeverityHigh, "observed_fault", "strong"},
	"service_selector_mismatch": {"selector", domain.SeverityHigh, "observed_fault", "strong"},
	"dns_failure":               {"dns", domain.SeverityHigh, "suspected_fault", "limited"},
	"resource_pressure":         {"cannot be scheduled", domain.SeverityHigh, "observed_fault", "strong"},
	"network_policy_block":      {"networkpolicy", domain.SeverityHigh, "suspected_fault", "limited"},
}

func TestEvaluateRulesOnAllScenarios(t *testing.T) {
	if len(expected) != len(simulation.Scenarios) {
		t.Fatalf("expectations cover %d of %d scenarios", len(expected), len(simulation.Scenarios))
	}
	for _, scenario := range simulation.Scenarios {
		t.Run(scenario, func(t *testing.T) {
			inc := incident(domain.ResourceDeployment, "payment-service")
			rec, err := newEngine(newMemStore(inc), sim(t), llm.RulesProvider{}).Investigate(context.Background(), inc.ID, Options{Scenario: scenario})
			if err != nil {
				t.Fatal(err)
			}
			a := rec.Report.Analysis
			want := expected[scenario]
			if !strings.Contains(strings.ToLower(a.RootCause), want.keyword) || a.Severity != want.severity || a.Verdict != want.verdict || a.Support != want.support {
				t.Fatalf("assessment=%q/%s/%s/%s, want %q/%s/%s/%s", a.RootCause, a.Severity, a.Verdict, a.Support, want.keyword, want.severity, want.verdict, want.support)
			}
			assertEvaluationCitations(t, rec)
		})
	}
}
