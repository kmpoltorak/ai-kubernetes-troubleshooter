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
	keyword  string
	severity domain.Severity
}{
	"healthy":                   {"no kubernetes-level fault", domain.SeverityLow},
	"crash_loop_backoff":        {"startup failure", domain.SeverityCritical},
	"image_pull_error":          {"image", domain.SeverityHigh},
	"oom_killed":                {"oomkilled", domain.SeverityHigh},
	"readiness_probe_failure":   {"readiness", domain.SeverityHigh},
	"liveness_probe_failure":    {"liveness", domain.SeverityHigh},
	"missing_configmap":         {"configmap", domain.SeverityHigh},
	"service_selector_mismatch": {"selector", domain.SeverityHigh},
	"dns_failure":               {"dns", domain.SeverityHigh},
	"resource_pressure":         {"insufficient cluster resources", domain.SeverityHigh},
	"network_policy_block":      {"networkpolicy", domain.SeverityHigh},
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
			if !strings.Contains(strings.ToLower(a.RootCause), want.keyword) || a.Severity != want.severity {
				t.Fatalf("root cause %q (%s), want %q (%s)", a.RootCause, a.Severity, want.keyword, want.severity)
			}
		})
	}
}
