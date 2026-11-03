package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRecordRedactionDoesNotRewriteSource(t *testing.T) {
	text := "timeout password=synthetic-secret"
	raw := json.RawMessage(`{"token":"synthetic-secret","count":18446744073709551615}`)
	original := InvestigationRecord{
		Investigation:  Investigation{Error: text},
		ToolExecutions: []ToolExecution{{ToolName: "get_pods", Error: text, Input: raw, Output: raw}},
		Evidence:       []Evidence{{Source: "get_pods", Subject: "pod/api", Summary: text, Data: raw}},
		Report:         &Report{Analysis: Analysis{Summary: text, RootCause: text, Evidence: []EvidenceReference{{Source: "get_pods", Description: text}}, MissingData: []string{text}, Limitations: []string{text}, PossibleCauses: []string{text}, RecommendedActions: []string{text}, SafeCommands: []string{text}}, RemediationActions: []RemediationAction{{Description: text, Command: text}}},
	}
	before, _ := json.Marshal(original)
	got, err := original.Redacted()
	if err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(original)
	clean, _ := json.Marshal(got)
	if string(before) != string(after) {
		t.Fatal("modified historical record")
	}
	if strings.Contains(string(clean), "synthetic-secret") || !strings.Contains(string(clean), "18446744073709551615") || got.Evidence[0].Source != "get_pods" || got.Evidence[0].Subject != "pod/api" {
		t.Fatalf("bad result: %s", clean)
	}
	inc := Incident{Title: text, Description: text, Target: Target{Cluster: "local", Namespace: "payments", ResourceName: "api"}}
	safe := inc.Redacted()
	if safe.Target != inc.Target || strings.Contains(safe.Title+safe.Description, "synthetic-secret") {
		t.Fatalf("bad incident: %+v", safe)
	}
}
