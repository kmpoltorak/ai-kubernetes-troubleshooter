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

	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/config"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/domain"
)

const validJSON = `{"summary":"s","root_cause":"r","schema_version":2,"verdict":"observed_fault","support":"strong","missing_data":[],"limitations":[],"severity":"high",
"affected_resources":["deployment/payment-service"],"evidence":[{"evidence_id":"ee376713-5c52-48d9-9ded-95113daaab0c","source":"get_pods","description":"d"}],
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
		{"legacy result", `{"confidence":0.7,"summary":"legacy"}`, true},
		{"v2 confidence even zero", strings.Replace(validJSON, `"schema_version":2`, `"schema_version":2,"confidence":0`, 1), true},
		{"case variant confidence", strings.Replace(validJSON, `"schema_version":2`, `"schema_version":2,"Confidence":0`, 1), true},
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
			if err != nil || a.RootCause != "r" || a.SchemaVersion != 2 || a.Support != "strong" {
				t.Fatalf("a=%+v err=%v", a, err)
			}
		})
	}
}

func sampleInput() AnalysisInput {
	return AnalysisInput{
		Incident: domain.Incident{ID: "secret-db-id", Title: "payment-service restarting", Description: "Pods are repeatedly restarting",
			Target: domain.Target{Cluster: "local", Namespace: "payments", ResourceType: domain.ResourceDeployment, ResourceName: "payment-service"}},
		Evidence: []domain.Evidence{{ID: "ee376713-5c52-48d9-9ded-95113daaab0c", Source: "get_pods", Subject: "deployment/payment-service", Health: domain.Down,
			Summary: "0/2 pods ready", Signals: []string{"OOMKilled"}, Data: json.RawMessage(`{"total":2}`)}},
	}
}

func TestUserPromptContainsEvidenceOnly(t *testing.T) {
	p, err := userPrompt(sampleInput())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"payment-service restarting", `"namespace":"payments"`, `"resource_type":"deployment"`,
		`"signals":["OOMKilled"]`, `"id":"ee376713-5c52-48d9-9ded-95113daaab0c"`, `"total":2`, `"health":"down"`, `"subject":"deployment/payment-service"`} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt missing %q:\n%s", want, p)
		}
	}
	if strings.Contains(p, "secret-db-id") {
		t.Error("prompt leaks incident ID")
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
	if _, exists := got["max_completion_tokens"]; exists {
		t.Fatal("ordinary Analyze gained an evaluation-only output cap")
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
	if _, exists := got["options"].(map[string]any)["num_predict"]; exists {
		t.Fatal("ordinary Analyze gained an evaluation-only output cap")
	}
	msgs := got["messages"].([]any)
	if len(msgs) != 2 || !strings.Contains(msgs[0].(map[string]any)["content"].(string), "Kubernetes SRE") {
		t.Fatalf("system prompt not sent: %v", msgs)
	}
}

func healthy(e domain.Evidence) domain.Evidence {
	e.Health = domain.Healthy
	return e
}

func ev(source, subject string, signals ...string) domain.Evidence {
	return domain.Evidence{ID: domain.NewID(), Source: source, Subject: subject, Health: domain.Degraded, Summary: source + " summary",
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
			"readiness", domain.SeverityHigh, ""},
		{"policy block", []domain.Evidence{ev("get_pod_logs", "pod/p", "ConnectionTimeout"), ev("get_network_policies", "networkpolicies/payments", "EgressRestricted")},
			"NetworkPolicy", domain.SeverityHigh, ""},
		{"liveness beats exit error", []domain.Evidence{ev("get_pods", "pods/x", "ContainerExitError", "FrequentRestarts", "LivenessProbeFailed")}, "Liveness", domain.SeverityHigh, ""},
		{"crash loop", []domain.Evidence{ev("get_pods", "pods/x", "CrashLoopBackOff"), ev("get_pod_logs", "pod/p", "ConfigurationError")}, "CrashLoopBackOff", domain.SeverityCritical, ""},
		{"selector mismatch", []domain.Evidence{ev("get_service", "service/payment-service", "SelectorMismatch", "NoReadyEndpoints")}, "selector", domain.SeverityHigh, "service/payment-service"},
		{"scheduling", []domain.Evidence{ev("get_pods", "pods/x", "Unschedulable")}, "cannot be scheduled", domain.SeverityHigh, ""},
		{"healthy", []domain.Evidence{healthy(ev("get_pods", "pods/x")), healthy(ev("get_events", "events/x"))}, "No Kubernetes-level fault", domain.SeverityLow, ""},
		{"degraded without pattern", []domain.Evidence{healthy(ev("get_pods", "pods/x")), ev("get_events", "events/x")}, "Inconclusive", domain.SeverityMedium, ""},
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

			if tt.name == "policy block" && (a.Verdict != "suspected_fault" || a.Support != "limited" || strings.Contains(a.RootCause, "blocks")) {
				t.Fatalf("unproven network cause: %+v", a)
			}
			if err := a.Validate(tt.evidence, nil); err != nil {
				t.Fatalf("rules output fails validation: %v", err)
			}
		})
	}
	if _, err := (RulesProvider{}).Analyze(context.Background(), AnalysisInput{}); err == nil {
		t.Fatal("no-evidence input accepted")
	}
}

// A failed core diagnostic must not yield a healthy verdict; a failed
// optional one (metrics-server absent) may.
func TestRulesProviderFailedDiagnostics(t *testing.T) {
	in := AnalysisInput{
		Incident: domain.Incident{Target: domain.Target{Namespace: "payments", ResourceType: domain.ResourceDeployment, ResourceName: "payment-service"}},
		Evidence: []domain.Evidence{healthy(ev("get_namespace", "namespace/payments"))},
	}
	for tool, want := range map[string]string{"get_deployment": "Inconclusive", "get_pods": "Inconclusive", "get_resource_usage": "No Kubernetes-level fault"} {
		in.Failed = []domain.ToolExecution{{ToolName: tool, Status: domain.ToolFailed, Error: "forbidden"}}
		a, err := RulesProvider{}.Analyze(context.Background(), in)
		if err != nil || !strings.Contains(a.RootCause, want) {
			t.Errorf("%s failed: got %q (%v), want %q", tool, a.RootCause, err, want)
		}
	}
	in.Failed = []domain.ToolExecution{{ToolName: "get_pods", Error: "forbidden"}}
	if p, _ := userPrompt(in); !strings.Contains(p, `"failed_diagnostics":[{"source":"get_pods","error":"forbidden"}]`) {
		t.Errorf("prompt does not report failed diagnostics:\n%s", p)
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
		if f.rootCause == "" || len(f.actions) == 0 || f.verdict == "" || f.support == "" {
			t.Errorf("rule %d incomplete: %+v", i, f)
		}
	}
}

// The UI header and /api/v1/runtime show the configured model; reports show the provider name.
func TestRulesDefaultModelMatchesProviderName(t *testing.T) {
	cfg, err := config.Load(func(k string) string {
		return map[string]string{"DATABASE_URL": "postgres://u:p@db.example.net/app"}[k]
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := cfg.LLMProvider+"/"+cfg.LLMModel, (RulesProvider{}).Name(); got != want {
		t.Fatalf("configured analyzer %q, reports say %q", got, want)
	}
}

// k3s pilot cases: the report uses what the diagnostics already read.
func TestRulesUseLoggedErrorsAndKnownNames(t *testing.T) {
	analyze := func(target domain.Target, failed []domain.ToolExecution, evidence ...domain.Evidence) domain.Analysis {
		t.Helper()
		a, err := RulesProvider{}.Analyze(context.Background(), AnalysisInput{Incident: domain.Incident{Target: target}, Evidence: evidence, Failed: failed})
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	deploy := func(name string) domain.Target {
		return domain.Target{Cluster: "lab", Namespace: "payments", ResourceType: domain.ResourceDeployment, ResourceName: name}
	}
	logs := ev("get_pod_logs", "pod/crash-worker-cb5c48f48-zpvn2")
	logs.Summary = "Pod crash-worker-cb5c48f48-zpvn2: 2 log streams, 2 error lines. Last error: config error: DATABASE_URL is not set"
	crash := analyze(deploy("crash-worker"), nil, ev("get_pods", "pods/app=crash-worker", "CrashLoopBackOff"), logs)
	if !slices.ContainsFunc(crash.Evidence, func(r domain.EvidenceReference) bool { return r.EvidenceID == logs.ID }) ||
		!strings.Contains(crash.Summary, "Last logged error (an observation, not a confirmed cause): config error: DATABASE_URL is not set") {
		t.Errorf("logged error not cited: %+v", crash)
	}
	if slices.ContainsFunc(crash.RecommendedActions, func(s string) bool { return strings.Contains(s, "read the previous container logs") }) ||
		!slices.Contains(crash.SafeCommands, "kubectl logs crash-worker-cb5c48f48-zpvn2 -n payments --previous") {
		t.Errorf("asks for logs it read or keeps a placeholder: %v %v", crash.RecommendedActions, crash.SafeCommands)
	}

	probe := ev("get_pod_logs", "pod/not-ready-1")
	probe.Summary = `Pod not-ready-1: 1 log streams, 20 error lines. Last error: open() "/usr/share/nginx/html/healthz" failed (2: No such file or directory)`
	ready := analyze(deploy("not-ready"), nil, ev("get_pods", "pods/app=not-ready", "PodNotReady"), probe)
	if !strings.Contains(ready.PossibleCauses[0], "probe path") || len(ready.PossibleCauses) != 3 {
		t.Errorf("a missing probe target must put the probe path first: %v", ready.PossibleCauses)
	}

	config := analyze(deploy("missing-config"), nil, ev("get_pods", "pods/app=missing-config", "ContainerConfigError"), ev("get_configmap", "configmap/payments-settings", "ConfigMapMissing"))
	if config.RootCause != "Referenced ConfigMap payments-settings is missing" {
		t.Errorf("root cause %q", config.RootCause)
	}
	pull := analyze(deploy("bad-image"), []domain.ToolExecution{{ToolName: "get_pod_logs", Input: json.RawMessage(`{"name":"bad-image-67c6b47d84-kjmck"}`), Error: "waiting to start"}},
		ev("get_pods", "pods/app=bad-image", "ImagePullError", "ImageNotFound"))
	if !strings.Contains(pull.Summary, "registry reports that the image reference was not found") ||
		!slices.Contains(pull.SafeCommands, "kubectl describe pod bad-image-67c6b47d84-kjmck -n payments") {
		t.Errorf("image not found: %q %v", pull.Summary, pull.SafeCommands)
	}
	svc := analyze(domain.Target{Cluster: "lab", Namespace: "payments", ResourceType: domain.ResourceService, ResourceName: "checkout"}, nil,
		ev("get_service", "service/checkout", "SelectorMismatch", "NoReadyEndpoints"))
	if !slices.Contains(svc.SafeCommands, "kubectl describe service checkout -n payments") {
		t.Errorf("service placeholder: %v", svc.SafeCommands)
	}
	for _, a := range []domain.Analysis{crash, ready, config, pull, svc} {
		for _, c := range a.SafeCommands {
			if err := domain.ValidateSafeCommand(c); err != nil {
				t.Errorf("unsafe command %q: %v", c, err)
			}
		}
	}
}

// Jobs, CronJobs and DaemonSets fail in their own ways; commands name the right object.
func TestRulesForControllerKinds(t *testing.T) {
	analyze := func(kind domain.ResourceType, name string, evidence ...domain.Evidence) domain.Analysis {
		t.Helper()
		target := domain.Target{Cluster: "lab", Namespace: "payments", ResourceType: kind, ResourceName: name}
		a, err := RulesProvider{}.Analyze(context.Background(), AnalysisInput{Incident: domain.Incident{Target: target}, Evidence: evidence})
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range a.SafeCommands {
			if err := domain.ValidateSafeCommand(c); err != nil || strings.Contains(c, "<job>") {
				t.Errorf("command %q: %v", c, err)
			}
		}
		return a
	}
	failed := analyze(domain.ResourceJob, "migrate", ev("get_job", "job/migrate", "JobFailed"), ev("get_pods", "pods/app=migrate", "ContainerExitError"))
	if failed.RootCause != "Job failed: its pods exited with errors" || !slices.Contains(failed.SafeCommands, "kubectl describe job migrate -n payments") {
		t.Errorf("failed job: %q %v", failed.RootCause, failed.SafeCommands)
	}
	slow := analyze(domain.ResourceCronJob, "nightly", ev("get_cronjob", "cronjob/nightly"), ev("get_job", "job/nightly-29270910", "JobFailed", "JobDeadlineExceeded"))
	if slow.RootCause != "Job exceeded its activeDeadlineSeconds" || !slices.Contains(slow.SafeCommands, "kubectl describe job nightly-29270910 -n payments") {
		t.Errorf("cronjob run past its deadline: %q %v", slow.RootCause, slow.SafeCommands)
	}
	if a := analyze(domain.ResourceCronJob, "nightly", ev("get_cronjob", "cronjob/nightly", "CronJobSuspended")); a.RootCause != "Suspended: no new runs start" ||
		!slices.Contains(a.SafeCommands, "kubectl get cronjob nightly -n payments") {
		t.Errorf("suspended: %q %v", a.RootCause, a.SafeCommands)
	}
	if a := analyze(domain.ResourceDaemonSet, "agent", ev("get_daemonset", "daemonset/agent", "NoEligibleNodes")); a.RootCause != "DaemonSet matches no nodes" {
		t.Errorf("daemonset: %q", a.RootCause)
	}
	crash := analyze(domain.ResourceStatefulSet, "db", ev("get_pods", "pods/app=db", "CrashLoopBackOff"))
	if !slices.Contains(crash.SafeCommands, "kubectl rollout history statefulset/db -n payments") {
		t.Errorf("statefulset rollout history: %v", crash.SafeCommands)
	}
	if pod := analyze(domain.ResourcePod, "db-0", ev("get_pods", "pod/db-0", "CrashLoopBackOff")); slices.ContainsFunc(pod.SafeCommands, func(c string) bool { return strings.Contains(c, "rollout") }) {
		t.Errorf("a pod has no rollout history: %v", pod.SafeCommands)
	}
}

// Ingress, claims, autoscalers and nodes: each finding names the object to inspect.
func TestRulesForIngressStorageAutoscalingAndNodes(t *testing.T) {
	analyze := func(kind domain.ResourceType, name string, evidence ...domain.Evidence) domain.Analysis {
		t.Helper()
		target := domain.Target{Cluster: "lab", Namespace: "payments", ResourceType: kind, ResourceName: name}
		a, err := RulesProvider{}.Analyze(context.Background(), AnalysisInput{Incident: domain.Incident{Target: target}, Evidence: evidence})
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range a.SafeCommands {
			if err := domain.ValidateSafeCommand(c); err != nil {
				t.Errorf("command %q: %v", c, err)
			}
		}
		return a
	}
	for _, tc := range []struct {
		kind     domain.ResourceType
		name     string
		evidence []domain.Evidence
		root     string
		command  string
	}{
		{domain.ResourceIngress, "shop", []domain.Evidence{ev("get_ingress", "ingress/shop", "IngressBackendMissing", "IngressNoAddress")},
			"Ingress routes to a Service that does not exist", "kubectl describe ingress shop -n payments"},
		{domain.ResourceIngress, "shop", []domain.Evidence{ev("get_ingress", "ingress/shop", "IngressNoAddress")},
			"Ingress has no address: no controller admitted it", "kubectl get ingressclass"},
		{domain.ResourceStatefulSet, "db", []domain.Evidence{ev("get_pods", "pods/app=db", "Unschedulable"), ev("get_pvc", "persistentvolumeclaim/data-db-0", "PVCPending")},
			"PersistentVolumeClaim is not bound (Pending)", "kubectl describe pvc data-db-0 -n payments"},
		{domain.ResourceDeployment, "api", []domain.Evidence{ev("get_pods", "pods/app=api", "PodNotReady"), ev("get_nodes", "node/worker-2", "NodeNotReady")},
			"Node running the pods is NotReady", "kubectl describe node worker-2"},
		{domain.ResourceHPA, "api", []domain.Evidence{ev("get_hpa", "horizontalpodautoscaler/api", "HPAMetricsUnavailable")},
			"Autoscaler cannot read its metrics", "kubectl describe hpa api -n payments"},
		// The autoscaler's missing target is explained by its condition, not as a missing autoscaler.
		{domain.ResourceHPA, "api", []domain.Evidence{ev("get_hpa", "horizontalpodautoscaler/api", "HPAUnableToScale"), ev("get_deployment", "deployment/gone", "WorkloadNotFound")},
			"Autoscaler cannot scale its target", "kubectl describe hpa api -n payments"},
	} {
		a := analyze(tc.kind, tc.name, tc.evidence...)
		if a.RootCause != tc.root || !slices.Contains(a.SafeCommands, tc.command) {
			t.Errorf("%s/%s %v: %q %v", tc.kind, tc.name, tc.evidence[0].Signals, a.RootCause, a.SafeCommands)
		}
	}
}
