package domain

import (
	"errors"
	"strings"
	"testing"
)

func validAnalysis() Analysis {
	return Analysis{
		Summary:       "Payment service is restarting because of memory exhaustion",
		RootCause:     "Container OOMKilled",
		SchemaVersion: 2, Verdict: "observed_fault", Support: "strong",
		Severity:           SeverityHigh,
		AffectedResources:  []string{"deployment/payment-service"},
		Evidence:           []EvidenceReference{{EvidenceID: "ee376713-5c52-48d9-9ded-95113daaab0c", Source: "get_pods", Description: "last state OOMKilled"}},
		RecommendedActions: []string{"increase memory limit if justified"},
		SafeCommands:       []string{"kubectl describe pod <pod> -n payments"},
	}
}

func TestAnalysisValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Analysis)
		wantErr string
	}{
		{"valid", func(*Analysis) {}, ""},
		{"no commands is fine", func(a *Analysis) { a.SafeCommands = nil }, ""},
		{"empty summary", func(a *Analysis) { a.Summary = " " }, "summary is empty"},
		{"empty root cause", func(a *Analysis) { a.RootCause = "" }, "root_cause is empty"},
		{"confidence above one", func(a *Analysis) { a.Confidence = 1.2 }, "confidence"},
		{"negative confidence", func(a *Analysis) { a.Confidence = -0.1 }, "confidence"},
		{"bad severity", func(a *Analysis) { a.Severity = "urgent" }, "severity"},
		{"bad affected resource", func(a *Analysis) { a.AffectedResources = []string{"payment-service"} }, "kind/name"},
		{"no evidence", func(a *Analysis) { a.Evidence = nil }, "evidence is empty"},
		{"uncollected source", func(a *Analysis) { a.Evidence[0].Source = "get_nodes" }, `"get_nodes" does not match`},
		{"blank evidence description", func(a *Analysis) { a.Evidence[0].Description = "" }, "empty description"},
		{"no actions", func(a *Analysis) { a.RecommendedActions = []string{""} }, "recommended_actions"},
		{"write command", func(a *Analysis) { a.SafeCommands = []string{"kubectl delete pod x"} }, "safe command"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := validAnalysis()
			tt.mutate(&a)
			err := a.Validate([]Evidence{{ID: "ee376713-5c52-48d9-9ded-95113daaab0c", Source: "get_pods", Health: Down}}, nil)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("want error containing %q, got %v", tt.wantErr, err)
			}
		})
	}
}

func TestValidateSafeCommand(t *testing.T) {
	safe := []string{
		"kubectl get pods -n payments",
		"kubectl describe pod <pod> -n payments",
		"kubectl logs payment-service-7c9d8f6b5-x2x9q -n payments --previous",
		"kubectl get endpointslices -n payments -l kubernetes.io/service-name=payment-service",
		"kubectl top pod -n payments",
		"kubectl rollout status deployment/payment-service -n payments",
		"kubectl events -n payments --for=pod/x",
		"kubectl get deployment payment-service -n payments -o yaml",
	}
	unsafe := []string{
		"",
		"ls -la",
		"kubectl",
		"kubectl delete pod x -n payments",
		"kubectl rollout restart deployment/payment-service",
		"kubectl exec -it x -- sh",
		"kubectl apply -f x.yaml",
		"kubectl get pods; rm -rf /",
		"kubectl get pods | grep x",
		"kubectl get pods $(id)",
		"kubectl get pods `id`",
		"kubectl get pods > out.txt",
		"kubectl get secret db -o yaml",
		"kubectl describe secrets",
		"kubectl get secret/db",
		"kubectl get pods,secrets -n payments",
		"kubectl get secrets.v1. -n payments",
		"kubectl get --raw /api/v1/namespaces/x/secrets",
		"kubectl get pods --as=system:admin",
		"kubectl get pods --token abc",
		"kubectl get pods --kubeconfig=/tmp/k",
		"sudo kubectl get pods",
		"kubectl get pods\nkubectl delete deployment demo -n payments",
		"kubectl get pods\r\nkubectl delete deployment demo -n payments",
		"kubectl get pods\nrm -rf /tmp/x",
		"kubectl get pods\rrm",
		"kubectl get pods\tx",
		"kubectl get pods\u0085rm",
		"kubectl get pods\x00",
	}
	for _, c := range safe {
		if err := ValidateSafeCommand(c); err != nil {
			t.Errorf("%q: unexpected error %v", c, err)
		}
	}
	for _, c := range unsafe {
		if err := ValidateSafeCommand(c); err == nil {
			t.Errorf("%q: want error", c)
		}
	}
}

func TestTargetValidate(t *testing.T) {
	valid := Target{Cluster: "local", Namespace: "payments", ResourceType: ResourceDeployment, ResourceName: "payment-service"}
	if err := valid.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if valid.Ref() != "deployment/payment-service" {
		t.Fatalf("ref = %s", valid.Ref())
	}
	bad := Target{Cluster: "", Namespace: "Payments", ResourceType: "secret", ResourceName: "a;b"}
	err := bad.Validate()
	for _, want := range []string{"cluster", "namespace", "resource_type", "resource_name"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("want error mentioning %s, got %v", want, err)
		}
	}
}

func TestRemediationActions(t *testing.T) {
	actions := RemediationActions(validAnalysis())
	if len(actions) != 2 {
		t.Fatalf("got %d actions", len(actions))
	}
	rec, cmd := actions[0], actions[1]
	if rec.Kind != RemediationRecommendation || !rec.RequiresApproval || rec.Position != 1 || rec.Status != "proposed" {
		t.Fatalf("recommendation: %+v", rec)
	}
	if cmd.Kind != RemediationDiagnosticCommand || cmd.RequiresApproval || cmd.Command == "" || cmd.Position != 2 {
		t.Fatalf("command: %+v", cmd)
	}
}

func TestTargetPolicy(t *testing.T) {
	tgt := Target{Cluster: "local", Namespace: "payments", ResourceType: ResourcePod, ResourceName: "p"}
	if err := (TargetPolicy{Clusters: map[string][]string{"local": nil}}).Check(tgt); err != nil {
		t.Fatalf("any namespace: %v", err)
	}
	if err := (TargetPolicy{Clusters: map[string][]string{"local": {"payments"}}}).Check(tgt); err != nil {
		t.Fatalf("allowed namespace: %v", err)
	}
	if err := (TargetPolicy{Clusters: map[string][]string{"prod": nil}}).Check(tgt); !errors.Is(err, ErrTargetNotAllowed) {
		t.Fatalf("other cluster: %v", err)
	}
	if err := (TargetPolicy{Clusters: map[string][]string{"local": {"orders"}}}).Check(tgt); !errors.Is(err, ErrTargetNotAllowed) {
		t.Fatalf("namespace outside allowlist: %v", err)
	}
}

// A failed read of the target or its pods means health was not observed; optional reads do not.
func TestHealthUnconfirmedByFailedCoreReads(t *testing.T) {
	healthy := []Evidence{{Health: Healthy}}
	for _, tool := range []string{"get_namespace", "get_deployment", "get_statefulset", "get_daemonset", "get_job", "get_cronjob", "get_ingress", "get_hpa", "get_custom_resource",
		"get_service", "get_pods", "get_events", "get_pod_logs"} {
		if !HealthUnconfirmed(healthy, []ToolExecution{{ToolName: tool}}) {
			t.Errorf("failed %s does not leave health unconfirmed", tool)
		}
	}
	for _, tool := range []string{"get_resource_usage", "get_secret_metadata", "get_configmap", "get_pvc", "get_nodes", "get_network_policies"} {
		if HealthUnconfirmed(healthy, []ToolExecution{{ToolName: tool}}) {
			t.Errorf("optional %s blocks a healthy verdict", tool)
		}
	}
}

// A custom kind passes validation by shape; only the policy admits configured ones.
func TestCustomResourceKinds(t *testing.T) {
	target := Target{Cluster: "local", Namespace: "payments", ResourceType: "rollouts.argoproj.io", ResourceName: "web"}
	if err := target.Validate(); err != nil {
		t.Fatalf("custom kind shape: %v", err)
	}
	policy := TargetPolicy{Clusters: map[string][]string{"local": nil}}
	if err := policy.Check(target); !errors.Is(err, ErrTargetNotAllowed) || !strings.Contains(err.Error(), "KUBERNETES_CUSTOM_RESOURCES") {
		t.Fatalf("unconfigured custom kind admitted: %v", err)
	}
	policy.CustomResources = []ResourceType{"rollouts.argoproj.io"}
	if err := policy.Check(target); err != nil {
		t.Fatalf("configured custom kind rejected: %v", err)
	}
	for _, kind := range []ResourceType{"deployment", "secrets", "Rollouts.argoproj.io", "rollouts..io", "-x.io"} {
		if kind.Custom() {
			t.Errorf("%q counted as a custom kind", kind)
		}
	}
}
