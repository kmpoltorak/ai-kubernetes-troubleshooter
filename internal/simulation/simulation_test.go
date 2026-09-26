package simulation

import (
	"context"
	"reflect"
	"slices"
	"testing"

	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/diagnostics"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/domain"
)

var target = domain.Target{Cluster: "local", Namespace: "payments", ResourceType: domain.ResourceDeployment, ResourceName: "payment-service"}

// collect runs every tool the way the investigation plan does and returns
// the union of signals and any tool errors.
func collect(t *testing.T, scenario string, tgt domain.Target) ([]string, map[string]error) {
	t.Helper()
	c, err := Build(scenario, tgt)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	ns := tgt.Namespace
	app := "payment-service"
	pods := []string{app + "-7c9d8f6b5-x2x9q", app + "-7c9d8f6b5-k4m7p"}
	labels := map[string]string{"app": app}
	calls := []struct {
		tool string
		in   diagnostics.Input
	}{
		{diagnostics.GetNamespace, diagnostics.Input{}},
		{diagnostics.GetDeployment, diagnostics.Input{Name: app}},
		{diagnostics.GetPods, diagnostics.Input{LabelSelector: "app=" + app}},
		{diagnostics.GetEvents, diagnostics.Input{Names: append([]string{app}, pods...)}},
		{diagnostics.GetPodLogs, diagnostics.Input{Name: pods[0], Names: []string{"app"}}},
		{diagnostics.GetService, diagnostics.Input{Name: app, Labels: labels}},
		{diagnostics.GetConfigMap, diagnostics.Input{Name: app + "-config"}},
		{diagnostics.GetSecretMetadata, diagnostics.Input{Name: app + "-db"}},
		{diagnostics.GetNetworkPolicies, diagnostics.Input{Labels: labels}},
		{diagnostics.GetResourceUsage, diagnostics.Input{Names: pods}},
	}
	var signals []string
	errs := map[string]error{}
	for _, call := range calls {
		call.in.Namespace = ns
		res, err := diagnostics.Run(ctx, call.tool, c, call.in)
		if err != nil {
			errs[call.tool] = err
			continue
		}
		for _, s := range res.Signals {
			if !slices.Contains(signals, s) {
				signals = append(signals, s)
			}
		}
	}
	return signals, errs
}

func TestScenarioSignals(t *testing.T) {
	tests := []struct {
		scenario   string
		want       []string
		notWant    []string
		toolErrors []string
	}{
		{"healthy", nil, nil, nil},
		{"crash_loop_backoff", []string{"CrashLoopBackOff", "ContainerExitError", "FrequentRestarts", "BackOff", "ConfigurationError", "NoReadyEndpoints"}, []string{"OOMKilled"}, nil},
		{"image_pull_error", []string{"ImagePullError", "FailedPull", "RolloutStuck", "ReplicasUnavailable"}, nil, []string{"get_pod_logs"}},
		{"oom_killed", []string{"OOMKilled", "CrashLoopBackOff", "HighMemoryUsage", "OutOfMemory"}, nil, nil},
		{"readiness_probe_failure", []string{"ReadinessProbeFailed", "PodNotReady", "NoReadyEndpoints"}, []string{"FrequentRestarts", "SelectorMismatch"}, nil},
		{"liveness_probe_failure", []string{"LivenessProbeFailed", "FrequentRestarts", "ContainerExitError"}, nil, nil},
		{"missing_configmap", []string{"ContainerConfigError", "ConfigMapMissing"}, nil, []string{"get_pod_logs"}},
		{"service_selector_mismatch", []string{"SelectorMismatch", "NoReadyEndpoints"}, []string{"PodNotReady"}, nil},
		{"dns_failure", []string{"DNSResolutionError", "ReadinessProbeFailed"}, []string{"EgressRestricted"}, nil},
		{"resource_pressure", []string{"Unschedulable", "FailedScheduling", "ReplicasUnavailable"}, nil, nil},
		{"network_policy_block", []string{"EgressRestricted", "ConnectionTimeout", "ReadinessProbeFailed"}, []string{"DNSResolutionError"}, nil},
	}
	if len(tests) != len(Scenarios) {
		t.Fatalf("test covers %d scenarios, %d defined", len(tests), len(Scenarios))
	}
	for _, tt := range tests {
		t.Run(tt.scenario, func(t *testing.T) {
			signals, errs := collect(t, tt.scenario, target)
			if tt.want == nil && len(signals) > 0 {
				t.Fatalf("healthy scenario produced signals %v", signals)
			}
			for _, s := range tt.want {
				if !slices.Contains(signals, s) {
					t.Errorf("missing signal %s (got %v)", s, signals)
				}
			}
			for _, s := range tt.notWant {
				if slices.Contains(signals, s) {
					t.Errorf("unexpected signal %s", s)
				}
			}
			for tool, err := range errs {
				if !slices.Contains(tt.toolErrors, tool) {
					t.Errorf("%s failed: %v", tool, err)
				}
			}
		})
	}
}

func TestScenarioFollowsTarget(t *testing.T) {
	c, err := Build("healthy", domain.Target{Cluster: "local", Namespace: "orders", ResourceType: domain.ResourcePod, ResourceName: "orders-api-0"})
	if err != nil {
		t.Fatal(err)
	}
	res, err := diagnostics.Run(context.Background(), diagnostics.GetPods, c, diagnostics.Input{Namespace: "orders", Name: "orders-api-0"})
	if err != nil || res.Health != domain.Healthy {
		t.Fatalf("pod target: %+v %v", res, err)
	}
}

func TestScenariosAreDeterministic(t *testing.T) {
	for _, s := range Scenarios {
		a, _ := collect(t, s, target)
		b, _ := collect(t, s, target)
		if !reflect.DeepEqual(a, b) {
			t.Fatalf("%s: signals differ between runs: %v vs %v", s, a, b)
		}
	}
}

func TestClusters(t *testing.T) {
	if _, err := NewClusters("meteor_strike"); err == nil {
		t.Fatal("unknown default accepted")
	}
	c, _ := NewClusters("oom_killed")
	if _, scenario, err := c.For(target, ""); err != nil || scenario != "oom_killed" {
		t.Fatalf("default scenario: %s %v", scenario, err)
	}
	if _, _, err := c.For(target, "nope"); err == nil {
		t.Fatal("unknown scenario accepted")
	}
}
