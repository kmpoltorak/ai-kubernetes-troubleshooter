package llm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/domain"
)

const validJSON = `{"summary":"s","root_cause":"r","confidence":0.7,"severity":"high",
"affected_resources":["deployment/payment-service"],"evidence":[{"source":"get_pods","description":"d"}],
"possible_causes":["c"],"recommended_actions":["a"],"safe_commands":["kubectl get pods -n payments"]}`

func TestDecodeAnalysis(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		wantErr bool
	}{
		{"plain", validJSON, false},
		{"fenced", "```json\n" + validJSON + "\n```", false},
		{"bare fence", "```\n" + validJSON + "\n```", false},
		{"unknown field", `{"summary":"s","hacked":true}`, true},
		{"trailing object", validJSON + `{"x":1}`, true},
		{"prose", "The root cause is DNS.", true},
		{"empty", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, err := decodeAnalysis(tt.in)
			if tt.wantErr {
				if !errors.Is(err, ErrInvalidOutput) {
					t.Fatalf("want ErrInvalidOutput, got %v", err)
				}
				return
			}
			if err != nil || a.RootCause != "r" || a.Confidence != 0.7 {
				t.Fatalf("a=%+v err=%v", a, err)
			}
		})
	}
}

func sampleInput() AnalysisInput {
	return AnalysisInput{
		Incident: domain.Incident{ID: "secret-db-id", Title: "payment-service restarting", Description: "Pods are repeatedly restarting",
			Target: domain.Target{Cluster: "local", Namespace: "payments", ResourceType: domain.ResourceDeployment, ResourceName: "payment-service"}},
		Evidence: []domain.Evidence{{ID: "ev-id", Source: "get_pods", Subject: "deployment/payment-service", Health: domain.Down,
			Summary: "0/2 pods ready", Signals: []string{"OOMKilled"}, Data: json.RawMessage(`{"total":2}`)}},
	}
}

func TestUserPromptContainsEvidenceOnly(t *testing.T) {
	p, err := userPrompt(sampleInput())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"payment-service restarting", `"namespace":"payments"`, `"resource_type":"deployment"`,
		`"signals":["OOMKilled"]`, `"total":2`, `"health":"down"`, `"subject":"deployment/payment-service"`} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt missing %q:\n%s", want, p)
		}
	}
	if strings.Contains(p, "secret-db-id") || strings.Contains(p, "ev-id") {
		t.Error("prompt leaks internal IDs")
	}
}

func TestOpenAIProvider(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" || r.Header.Get("Authorization") != "Bearer sk-test" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		content, _ := json.Marshal(validJSON)
		_, _ = w.Write([]byte(`{"choices":[{"finish_reason":"stop","message":{"content":` + string(content) + `}}]}`))
	}))
	defer srv.Close()

	p := NewOpenAIProvider(srv.URL, "sk-test", "gpt-test", 5*time.Second)
	a, err := p.Analyze(context.Background(), sampleInput())
	if err != nil || a.RootCause != "r" {
		t.Fatalf("a=%+v err=%v", a, err)
	}
	rf := got["response_format"].(map[string]any)
	js := rf["json_schema"].(map[string]any)
	if rf["type"] != "json_schema" || js["strict"] != true || got["model"] != "gpt-test" {
		t.Fatalf("request not using strict schema: %v", got)
	}
	if p.Name() != "openai/gpt-test" {
		t.Fatalf("name = %s", p.Name())
	}
}

func TestOpenAIProviderErrors(t *testing.T) {
	tests := []struct {
		name        string
		status      int
		body        string
		wantInvalid bool
	}{
		{"server error", 500, `{"error":{"message":"boom"}}`, false},
		{"unauthorized", 401, `{"error":{"message":"bad key"}}`, false},
		{"no choices", 200, `{"choices":[]}`, true},
		{"refusal", 200, `{"choices":[{"message":{"refusal":"I can't"}}]}`, true},
		{"truncated", 200, `{"choices":[{"finish_reason":"length","message":{"content":"{"}}]}`, true},
		{"prose content", 200, `{"choices":[{"message":{"content":"It is DNS."}}]}`, true},
		{"broken envelope", 200, `not json`, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer srv.Close()
			_, err := NewOpenAIProvider(srv.URL, "k", "m", time.Second).Analyze(context.Background(), sampleInput())
			if err == nil {
				t.Fatal("expected error")
			}
			if errors.Is(err, ErrInvalidOutput) != tt.wantInvalid {
				t.Fatalf("ErrInvalidOutput=%t, want %t: %v", errors.Is(err, ErrInvalidOutput), tt.wantInvalid, err)
			}
		})
	}
}

func TestProviderTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	defer srv.Close()
	start := time.Now()
	_, err := NewOllamaProvider(srv.URL, "m", 100*time.Millisecond).Analyze(context.Background(), sampleInput())
	if err == nil || time.Since(start) > time.Second {
		t.Fatalf("timeout not enforced: err=%v elapsed=%s", err, time.Since(start))
	}
}

func TestOllamaProvider(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		content, _ := json.Marshal("```json\n" + validJSON + "\n```")
		_, _ = w.Write([]byte(`{"message":{"role":"assistant","content":` + string(content) + `},"done":true}`))
	}))
	defer srv.Close()

	a, err := NewOllamaProvider(srv.URL, "llama3.1", time.Second).Analyze(context.Background(), sampleInput())
	if err != nil || a.Severity != domain.SeverityHigh {
		t.Fatalf("a=%+v err=%v", a, err)
	}
	if got["stream"] != false || got["format"] == nil {
		t.Fatalf("request missing stream=false or format schema: %v", got)
	}
	msgs := got["messages"].([]any)
	if len(msgs) != 2 || !strings.Contains(msgs[0].(map[string]any)["content"].(string), "Kubernetes SRE") {
		t.Fatalf("system prompt not sent: %v", msgs)
	}
}

func ev(source, subject string, signals ...string) domain.Evidence {
	return domain.Evidence{Source: source, Subject: subject, Health: domain.Degraded, Summary: source + " summary",
		Signals: signals, Data: json.RawMessage("{}")}
}

func TestRulesProvider(t *testing.T) {
	tests := []struct {
		name      string
		evidence  []domain.Evidence
		rootCause string
		severity  domain.Severity
		affected  string
	}{
		{"namespace missing", []domain.Evidence{ev("get_namespace", "namespace/payments", "NamespaceNotFound")}, "Namespace does not exist", domain.SeverityMedium, ""},
		{"image pull beats crash loop", []domain.Evidence{ev("get_pods", "pods/app=x", "ImagePullError", "CrashLoopBackOff")}, "image", domain.SeverityHigh, ""},
		{"missing configmap", []domain.Evidence{ev("get_pods", "pods/app=x", "ContainerConfigError"), ev("get_configmap", "configmap/payment-service-config", "ConfigMapMissing")},
			"ConfigMap", domain.SeverityHigh, "configmap/payment-service-config"},
		{"oom beats crash loop", []domain.Evidence{ev("get_pods", "pods/app=x", "CrashLoopBackOff", "OOMKilled")}, "OOMKilled", domain.SeverityHigh, ""},
		{"dns beats readiness", []domain.Evidence{ev("get_events", "events/x", "ReadinessProbeFailed"), ev("get_pod_logs", "pod/payment-service-a", "DNSResolutionError")},
			"DNS", domain.SeverityHigh, "pod/payment-service-a"},
		{"timeout alone is not a policy block", []domain.Evidence{ev("get_pod_logs", "pod/p", "ConnectionTimeout"), ev("get_events", "events/x", "ReadinessProbeFailed")},
			"Readiness", domain.SeverityHigh, ""},
		{"policy block", []domain.Evidence{ev("get_pod_logs", "pod/p", "ConnectionTimeout"), ev("get_network_policies", "networkpolicies/payments", "EgressRestricted")},
			"NetworkPolicy", domain.SeverityHigh, ""},
		{"liveness beats exit error", []domain.Evidence{ev("get_pods", "pods/x", "ContainerExitError", "FrequentRestarts", "LivenessProbeFailed")}, "Liveness", domain.SeverityHigh, ""},
		{"crash loop", []domain.Evidence{ev("get_pods", "pods/x", "CrashLoopBackOff"), ev("get_pod_logs", "pod/p", "ConfigurationError")}, "CrashLoopBackOff", domain.SeverityCritical, ""},
		{"selector mismatch", []domain.Evidence{ev("get_service", "service/payment-service", "SelectorMismatch", "NoReadyEndpoints")}, "selector", domain.SeverityHigh, "service/payment-service"},
		{"scheduling", []domain.Evidence{ev("get_pods", "pods/x", "Unschedulable")}, "Insufficient cluster resources", domain.SeverityHigh, ""},
		{"healthy", []domain.Evidence{ev("get_pods", "pods/x"), ev("get_events", "events/x")}, "No Kubernetes-level fault", domain.SeverityLow, ""},
	}
	target := domain.Target{Cluster: "local", Namespace: "payments", ResourceType: domain.ResourceDeployment, ResourceName: "payment-service"}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, err := RulesProvider{}.Analyze(context.Background(), AnalysisInput{Incident: domain.Incident{Target: target}, Evidence: tt.evidence})
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(a.RootCause, tt.rootCause) || a.Severity != tt.severity {
				t.Fatalf("got %q/%s, want %q/%s", a.RootCause, a.Severity, tt.rootCause, tt.severity)
			}
			if tt.affected != "" && !slices.Contains(a.AffectedResources, tt.affected) {
				t.Fatalf("affected %v missing %s", a.AffectedResources, tt.affected)
			}
			var sources []string
			for _, e := range tt.evidence {
				sources = append(sources, e.Source)
			}
			if err := a.Validate(sources); err != nil {
				t.Fatalf("rules output fails validation: %v", err)
			}
		})
	}
	if _, err := (RulesProvider{}).Analyze(context.Background(), AnalysisInput{}); err == nil {
		t.Fatal("no-evidence input accepted")
	}
}

// Every rule's commands must pass the same validation as model output.
func TestRuleCommandsAreSafe(t *testing.T) {
	target := domain.Target{Cluster: "local", Namespace: "payments", ResourceType: domain.ResourceService, ResourceName: "payment-service"}
	for i, r := range rules {
		f := r.build(target)
		for _, c := range f.commands {
			if err := domain.ValidateSafeCommand(c); err != nil {
				t.Errorf("rule %d command %q: %v", i, c, err)
			}
		}
		if f.rootCause == "" || len(f.actions) == 0 || f.confidence <= 0 || f.confidence > 1 {
			t.Errorf("rule %d incomplete: %+v", i, f)
		}
	}
}
