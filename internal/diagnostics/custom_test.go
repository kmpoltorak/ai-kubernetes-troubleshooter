package diagnostics

import (
	"encoding/json"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/domain"
)

var rollouts = schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "rollouts"}

func rollout(name string, generation int64, status map[string]any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1", "kind": "Rollout",
		"metadata": map[string]any{"name": name, "namespace": ns, "generation": generation},
		"spec": map[string]any{"selector": map[string]any{"matchLabels": map[string]any{"app": "payment-service"}},
			"analysis": map[string]any{"args": []any{map[string]any{"name": "token", "value": "spec-value-not-recorded"}}}},
		"status": status,
	}}
}

func cond(typ, status, reason, msg string) map[string]any {
	return map[string]any{"type": typ, "status": status, "reason": reason, "message": msg}
}

func TestGetCustomResource(t *testing.T) {
	c := cluster(t)
	c.CustomResources = map[domain.ResourceType]schema.GroupVersionResource{"rollouts.argoproj.io": rollouts}
	c.Dynamic = dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{rollouts: "RolloutList"},
		rollout("ok", 2, map[string]any{"observedGeneration": int64(2), "conditions": []any{cond("Available", "True", "AvailableReason", "")}}),
		rollout("stuck", 2, map[string]any{"observedGeneration": int64(2), "conditions": []any{cond("Progressing", "False", "ProgressDeadlineExceeded", "ReplicaSet web-5d9 has timed out progressing")}}),
		rollout("aborted", 2, map[string]any{"observedGeneration": int64(2), "phase": "Degraded"}),
		rollout("lagging", 5, map[string]any{"observedGeneration": int64(4), "conditions": []any{cond("Available", "True", "", "")}}),
		rollout("silent", 1, nil),
	)
	in := func(name string) Input { return Input{Name: name, Resource: "rollouts.argoproj.io"} }

	r := run(t, c, GetCustomResource, in("ok"))
	info := r.Data.(CustomResourceInfo)
	raw, _ := json.Marshal(info)
	if r.Health != domain.Healthy || info.Selector != "app=payment-service" || r.Subject != "rollouts.argoproj.io/ok" || strings.Contains(string(raw), "spec-value-not-recorded") {
		t.Fatalf("healthy rollout, spec values kept out: %+v %s", r, raw)
	}
	r = run(t, c, GetCustomResource, in("stuck"))
	if r.Health != domain.Down || !strings.Contains(r.Summary, "Progressing=False (ProgressDeadlineExceeded: ReplicaSet web-5d9 has timed out progressing)") {
		t.Errorf("stuck: %+v", r)
	}
	wantSignals(t, r, SignalCustomResourceNotReady)
	if r := run(t, c, GetCustomResource, in("aborted")); r.Health != domain.Degraded {
		t.Errorf("degraded phase: %+v", r)
	} else {
		wantSignals(t, r, SignalCustomResourceDegraded)
	}
	// Unreconciled or silent resources are not called healthy, but not faulty either.
	for _, name := range []string{"lagging", "silent"} {
		if r := run(t, c, GetCustomResource, in(name)); r.Health != domain.Degraded || len(r.Signals) != 0 {
			t.Errorf("%s: %+v", name, r)
		}
	}
	wantSignals(t, run(t, c, GetCustomResource, in("absent")), SignalWorkloadNotFound)
	if _, err := Run(t.Context(), GetCustomResource, c, Input{Namespace: ns, Name: "ok", Resource: "widgets.example.com"}); err == nil {
		t.Error("an unconfigured custom resource was read")
	}
	if _, err := Run(t.Context(), GetCustomResource, c, Input{Namespace: ns, Name: "ok", Resource: "secrets"}); err == nil {
		t.Error("a built-in kind passed as a custom resource")
	}
}
