package investigation

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/diagnostics"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/domain"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/kube"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/llm"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/simulation"
)

type changedAnalysis func(*domain.Analysis)

func (changedAnalysis) Name() string { return "test/changed" }
func (change changedAnalysis) Analyze(ctx context.Context, in llm.AnalysisInput) (domain.Analysis, error) {
	a, err := (llm.RulesProvider{}).Analyze(ctx, in)
	if err == nil {
		change(&a)
	}
	return a, err
}

func TestReportRejectsEvidenceFromEarlierAttempt(t *testing.T) {
	inc := incident(domain.ResourceDeployment, "payment-service")
	store := newMemStore(inc)
	previous, err := newEngine(store, sim(t), llm.RulesProvider{}).Investigate(context.Background(), inc.ID, Options{Scenario: "oom_killed"})
	if err != nil {
		t.Fatal(err)
	}
	foreign := previous.Report.Analysis.Evidence[0]
	malicious := changedAnalysis(func(a *domain.Analysis) { a.Evidence[0] = foreign })
	rec, err := newEngine(store, sim(t), malicious).Investigate(context.Background(), inc.ID, Options{Scenario: "oom_killed"})
	if !errors.Is(err, llm.ErrInvalidOutput) || rec.Report != nil || rec.Investigation.Status != domain.InvestigationFailed || !strings.Contains(rec.Investigation.Error, "not collected in this attempt") || len(rec.Evidence) == 0 {
		t.Fatalf("foreign citation admitted or evidence lost: status=%s report=%v error=%v", rec.Investigation.Status, rec.Report, err)
	}
	if len(store.finished) != 2 || store.finished[1].Report != nil {
		t.Fatal("failed attempt not preserved separately")
	}
}

func TestReportAddsMissingDataAndBoundsDespiteAnalyzerOmission(t *testing.T) {
	inc := incident(domain.ResourceDeployment, "payment-service")
	clusters := clustersFunc(func(target domain.Target, _ string) (kube.Cluster, string, error) {
		c, err := simulation.Build("healthy", target)
		c.PodMetrics = nil
		return c, "healthy", err
	})
	provider := changedAnalysis(func(a *domain.Analysis) { a.MissingData = nil; a.Limitations = nil })
	rec, err := newEngine(newMemStore(inc), clusters, provider).Investigate(context.Background(), inc.ID, Options{})
	if err != nil {
		t.Fatal(err)
	}
	a := rec.Report.Analysis
	if len(a.MissingData) == 0 || !strings.Contains(strings.Join(a.MissingData, " "), diagnostics.GetResourceUsage) || len(a.Limitations) < 5 || a.Verdict != "no_fault_observed" {
		t.Fatalf("missing application context: %+v", a)
	}
	raw, _ := json.Marshal(a)
	if strings.Contains(string(raw), "confidence") || !strings.Contains(string(raw), "CNI enforcement") {
		t.Fatalf("invalid v2: %s", raw)
	}
}

func TestReportRejectsNoFaultClaimWithUnhealthyEvidence(t *testing.T) {
	inc := incident(domain.ResourceDeployment, "payment-service")
	provider := changedAnalysis(func(a *domain.Analysis) { a.Verdict = "no_fault_observed"; a.Support = "limited" })
	rec, err := newEngine(newMemStore(inc), sim(t), provider).Investigate(context.Background(), inc.ID, Options{Scenario: "oom_killed"})
	if !errors.Is(err, llm.ErrInvalidOutput) || rec.Report != nil || !strings.Contains(rec.Investigation.Error, "unhealthy diagnostics") {
		t.Fatalf("false no-fault claim admitted: %+v %v", rec, err)
	}
}

// Pilot decision: a healthy result recommends nothing, whatever the analyzer wrote.
func TestHealthyReportCarriesNoRecommendations(t *testing.T) {
	inc := incident(domain.ResourceDeployment, "payment-service")
	chatty := changedAnalysis(func(a *domain.Analysis) {
		a.RecommendedActions = []string{"check application metrics", "re-run the investigation"}
	})
	for name, provider := range map[string]llm.Provider{"rules": llm.RulesProvider{}, "analyzer that recommends anyway": chatty} {
		t.Run(name, func(t *testing.T) {
			rec, err := newEngine(newMemStore(inc), sim(t), provider).Investigate(context.Background(), inc.ID, Options{Scenario: "healthy"})
			if err != nil {
				t.Fatal(err)
			}
			a := rec.Report.Analysis
			if a.Verdict != "no_fault_observed" || len(a.RecommendedActions) != 0 {
				t.Fatalf("verdict %s with recommendations %v", a.Verdict, a.RecommendedActions)
			}
			for _, r := range rec.Report.RemediationActions {
				if r.Kind == domain.RemediationRecommendation {
					t.Fatalf("stored recommendation for a healthy result: %+v", r)
				}
			}
		})
	}
	rec, err := newEngine(newMemStore(inc), sim(t), llm.RulesProvider{}).Investigate(context.Background(), inc.ID, Options{Scenario: "oom_killed"})
	if err != nil || len(rec.Report.Analysis.RecommendedActions) == 0 {
		t.Fatalf("a fault must keep its recommendations: %v %+v", err, rec.Report)
	}
}
