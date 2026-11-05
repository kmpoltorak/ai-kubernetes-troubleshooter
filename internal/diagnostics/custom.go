package diagnostics

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/domain"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/kube"
)

// Condition types whose False status means the resource is not working, and
// those whose True status means it is failing. Controllers use either style.
var (
	positiveConditions = []string{"Ready", "Available", "Healthy", "Synced", "Succeeded", "Progressing", "Reconciled"}
	negativeConditions = []string{"Degraded", "Failed", "Stalled", "Error", "ReconcileError"}
	failedPhases       = []string{"failed", "error", "degraded", "unhealthy"}
)

const maxCustomConditions = 10

type CustomResourceInfo struct {
	Name               string      `json:"name"`
	Kind               string      `json:"kind"`
	Generation         int64       `json:"generation"`
	ObservedGeneration int64       `json:"observed_generation,omitempty"`
	Phase              string      `json:"phase,omitempty"`
	Conditions         []Condition `json:"conditions,omitempty"`
	// Selector and PodLabels lead to the resource's pods, when it names them.
	Selector  string            `json:"selector,omitempty"`
	PodLabels map[string]string `json:"pod_labels,omitempty"`
}

// getCustomResource reads a configured custom resource: metadata, status
// conditions, phase and generation, and the pod selector. No other spec or
// status value is kept, so operator objects cannot leak their configuration.
func getCustomResource(ctx context.Context, c kube.Cluster, in Input) (Result, error) {
	gvr, ok := c.CustomResources[domain.ResourceType(in.Resource)]
	if !ok || c.Dynamic == nil {
		return Result{}, fmt.Errorf("custom resource %q is not configured", in.Resource)
	}
	subject := in.Resource + "/" + in.Name
	u, err := c.Dynamic.Resource(gvr).Namespace(in.Namespace).Get(ctx, in.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return Result{Subject: subject, Health: domain.Down, Summary: fmt.Sprintf("%s %s not found in %s.", in.Resource, in.Name, in.Namespace),
			Signals: []string{SignalWorkloadNotFound}, Data: CustomResourceInfo{Name: in.Name}}, nil
	}
	if err != nil {
		return Result{}, err
	}
	info := CustomResourceInfo{Name: u.GetName(), Kind: u.GetKind(), Generation: u.GetGeneration()}
	info.ObservedGeneration, _, _ = unstructured.NestedInt64(u.Object, "status", "observedGeneration")
	info.Phase, _, _ = unstructured.NestedString(u.Object, "status", "phase")
	if m, found, _ := unstructured.NestedStringMap(u.Object, "spec", "selector", "matchLabels"); found && len(m) > 0 {
		info.PodLabels, info.Selector = m, labels.SelectorFromSet(m).String()
	} else if s, found, _ := unstructured.NestedString(u.Object, "status", "selector"); found {
		// The scale subresource's label selector; parsed so only a valid selector is used.
		if sel, err := labels.Parse(s); err == nil {
			info.Selector = sel.String()
		}
	}

	var signals signalSet
	var failing []string
	conds, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	for _, raw := range conds[:min(len(conds), maxCustomConditions)] {
		m, _ := raw.(map[string]any)
		str := func(k string) string { s, _ := m[k].(string); return s }
		cond := condition(str("type"), str("status"), str("reason"), str("message"))
		info.Conditions = append(info.Conditions, cond)
		switch {
		case slices.Contains(positiveConditions, cond.Type) && cond.Status == "False":
			signals.add(SignalCustomResourceNotReady)
		case slices.Contains(negativeConditions, cond.Type) && cond.Status == "True":
			signals.add(SignalCustomResourceDegraded)
		default:
			continue
		}
		text := cond.Type + "=" + cond.Status
		if cond.Reason != "" || cond.Message != "" {
			text += " (" + strings.TrimSpace(cond.Reason+": "+cond.Message) + ")"
		}
		failing = append(failing, text)
	}
	if slices.Contains(failedPhases, strings.ToLower(info.Phase)) {
		signals.add(SignalCustomResourceDegraded)
		failing = append(failing, "phase "+info.Phase)
	}

	kind := cmp.Or(info.Kind, in.Resource)
	health, summary := domain.Healthy, fmt.Sprintf("%s %s reports no failing condition", kind, info.Name)
	switch {
	case slices.Contains(signals, SignalCustomResourceNotReady):
		health, summary = domain.Down, fmt.Sprintf("%s %s reports %s", kind, info.Name, strings.Join(failing, "; "))
	case len(signals) > 0:
		health, summary = domain.Degraded, fmt.Sprintf("%s %s reports %s", kind, info.Name, strings.Join(failing, "; "))
	case info.ObservedGeneration > 0 && info.ObservedGeneration < info.Generation:
		// Its controller has not processed the latest change: health is unconfirmed.
		health, summary = domain.Degraded, fmt.Sprintf("%s %s: its controller has not reconciled generation %d yet (observed %d)", kind, info.Name, info.Generation, info.ObservedGeneration)
	case len(conds) == 0 && info.Phase == "":
		health, summary = domain.Degraded, fmt.Sprintf("%s %s publishes no status conditions or phase", kind, info.Name)
	}
	return Result{Subject: subject, Health: health, Summary: summary + ".", Signals: signals, Data: info}, nil
}
