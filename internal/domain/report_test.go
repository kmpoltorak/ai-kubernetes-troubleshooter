package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestReportV2EvidenceAndVerdictBoundaries(t *testing.T) {
	collected := []Evidence{{ID: "ee376713-5c52-48d9-9ded-95113daaab0c", Source: "get_pods", Health: Down}, {ID: NewID(), Source: "get_pods", Health: Healthy}}
	for _, tc := range []struct {
		name   string
		change func(*Analysis)
		want   string
	}{
		{"legacy cannot be newly admitted", func(a *Analysis) { a.SchemaVersion = 0 }, "schema_version"},
		{"unknown version", func(a *Analysis) { a.SchemaVersion = 3 }, "schema_version"},
		{"empty ID", func(a *Analysis) { a.Evidence[0].EvidenceID = "" }, "evidence_id"},
		{"other attempt same tool", func(a *Analysis) { a.Evidence[0].EvidenceID = NewID() }, "not collected in this attempt"},
		{"mismatched source", func(a *Analysis) { a.Evidence[0].Source = "get_events" }, "does not match"},
		{"two items same tool exact ID matters", func(a *Analysis) { a.Evidence[0].EvidenceID = collected[1].ID }, "unhealthy observation"},
		{"invented verdict", func(a *Analysis) { a.Verdict = "certain" }, "invalid verdict"},
		{"invented support", func(a *Analysis) { a.Support = "100%" }, "invalid support"},
		{"strong hypothesis", func(a *Analysis) { a.Verdict = "suspected_fault" }, "contradicts verdict"},
		{"strong absence", func(a *Analysis) { a.Verdict = "no_fault_observed" }, "contradicts verdict"},
		{"inconclusive support", func(a *Analysis) { a.Verdict = "inconclusive"; a.Support = "limited" }, "contradicts verdict"},
		{"unhealthy absence", func(a *Analysis) { a.Verdict = "no_fault_observed"; a.Support = "limited" }, "unhealthy diagnostics"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := validAnalysis()
			tc.change(&a)
			if err := a.Validate(collected, nil); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("wanted %s, got %v", tc.want, err)
			}
		})
	}
	a := validAnalysis()
	a.Verdict = "no_fault_observed"
	a.Support = "limited"
	collected[0].Health = Healthy
	if err := a.Validate(collected, nil); err != nil {
		t.Fatal(err)
	}
	if err := a.Validate(collected, []ToolExecution{{ToolName: "get_pod_logs", Error: "forbidden"}}); err == nil {
		t.Fatal("missing logs became proof of absence")
	}
	if err := a.Validate(collected, []ToolExecution{{ToolName: "get_resource_usage", Error: "not installed"}}); err != nil {
		t.Fatalf("optional metric failure invalidated bounded observation: %v", err)
	}
}

func TestAnalysisJSONKeepsLegacyAndOmitsV2Score(t *testing.T) {
	for _, score := range []float64{0, 0.94} {
		a := Analysis{Confidence: score, Summary: "old"}
		raw, err := json.Marshal(a)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			t.Fatal(err)
		}
		if _, ok := fields["confidence"]; !ok {
			t.Fatalf("legacy score lost: %s", raw)
		}
		if _, ok := fields["schema_version"]; ok {
			t.Fatalf("legacy relabelled: %s", raw)
		}
		var decoded Analysis
		if err := json.Unmarshal(raw, &decoded); err != nil || decoded.Confidence != score {
			t.Fatalf("legacy changed: %+v %v", decoded, err)
		}
	}
	a := validAnalysis()
	raw, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "confidence") || !strings.Contains(string(raw), `"schema_version":2`) || !strings.Contains(string(raw), `"missing_data":[]`) || !strings.Contains(string(raw), `"limitations":[]`) {
		t.Fatalf("bad v2 wire: %s", raw)
	}
}
