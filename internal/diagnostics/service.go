package diagnostics

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/domain"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/kube"
)

type ServicePort struct {
	Name       string `json:"name,omitempty"`
	Port       int32  `json:"port"`
	TargetPort string `json:"target_port"`
	Protocol   string `json:"protocol"`
}

type ServiceInfo struct {
	Name              string            `json:"name"`
	Type              string            `json:"type"`
	Selector          map[string]string `json:"selector"`
	Ports             []ServicePort     `json:"ports"`
	MatchingPods      int               `json:"matching_pods"`
	ReadyEndpoints    int               `json:"ready_endpoints"`
	NotReadyEndpoints int               `json:"not_ready_endpoints"`
	Signals           []string          `json:"signals,omitempty"`
}

type ServicesInfo struct {
	Services []ServiceInfo `json:"services"`
	// NamespacePodLabels samples pod label sets in the namespace when a
	// selector matches nothing, so the mismatch is visible.
	NamespacePodLabels []map[string]string `json:"namespace_pod_labels,omitempty"`
}

// getService inspects the Service in.Name and/or every Service whose
// selector matches the workload's pod labels (in.Labels).
func getService(ctx context.Context, c kube.Cluster, in Input) (Result, error) {
	api := c.Client.CoreV1().Services(in.Namespace)
	var svcs []corev1.Service
	if in.Name != "" {
		s, err := api.Get(ctx, in.Name, metav1.GetOptions{})
		switch {
		case apierrors.IsNotFound(err) && len(in.Labels) == 0:
			return Result{Subject: "service/" + in.Name, Health: domain.Down, Summary: fmt.Sprintf("Service %s not found in %s.", in.Name, in.Namespace),
				Signals: []string{SignalServiceNotFound}, Data: ServicesInfo{}}, nil
		case err == nil:
			svcs = append(svcs, *s)
		case !apierrors.IsNotFound(err):
			return Result{}, err
		}
	}
	if len(in.Labels) > 0 {
		list, err := api.List(ctx, metav1.ListOptions{})
		if err != nil {
			return Result{}, err
		}
		for _, s := range list.Items {
			if len(s.Spec.Selector) > 0 && labels.SelectorFromSet(s.Spec.Selector).Matches(labels.Set(in.Labels)) &&
				!slices.ContainsFunc(svcs, func(x corev1.Service) bool { return x.Name == s.Name }) {
				svcs = append(svcs, s)
			}
		}
	}

	subject := "service/" + in.Name
	if in.Name == "" {
		subject = "services/" + labels.SelectorFromSet(in.Labels).String()
	}
	if len(svcs) == 0 {
		return Result{Subject: subject, Health: domain.Healthy, Summary: "No Service selects this workload.", Data: ServicesInfo{}}, nil
	}

	var info ServicesInfo
	var signals signalSet
	health := domain.Healthy
	for _, s := range svcs {
		si, err := inspectService(ctx, c, s)
		if err != nil {
			return Result{}, err
		}
		signals.add(si.Signals...)
		if si.ReadyEndpoints == 0 && len(si.Selector) > 0 {
			health = domain.Down
		}
		info.Services = append(info.Services, si)
	}
	if slices.Contains(signals, SignalSelectorMismatch) {
		sample, err := podLabelSample(ctx, c, in.Namespace)
		if err != nil {
			return Result{}, err
		}
		info.NamespacePodLabels = sample
	}

	parts := make([]string, 0, len(info.Services))
	for _, si := range info.Services {
		parts = append(parts, fmt.Sprintf("%s: %d matching pods, %d ready endpoints", si.Name, si.MatchingPods, si.ReadyEndpoints))
	}
	summary := strings.Join(parts, "; ") + "."
	if len(signals) > 0 {
		summary += " Findings: " + strings.Join(signals, ", ") + "."
	}
	return Result{Subject: subject, Health: health, Summary: summary, Signals: signals, Data: info}, nil
}

func inspectService(ctx context.Context, c kube.Cluster, s corev1.Service) (ServiceInfo, error) {
	si := ServiceInfo{Name: s.Name, Type: string(s.Spec.Type), Selector: s.Spec.Selector}
	for _, p := range s.Spec.Ports {
		si.Ports = append(si.Ports, ServicePort{Name: p.Name, Port: p.Port, TargetPort: p.TargetPort.String(), Protocol: string(p.Protocol)})
	}
	var signals signalSet
	if len(s.Spec.Selector) > 0 {
		pods, err := c.Client.CoreV1().Pods(s.Namespace).List(ctx, metav1.ListOptions{
			LabelSelector: labels.SelectorFromSet(s.Spec.Selector).String(),
		})
		if err != nil {
			return si, err
		}
		si.MatchingPods = len(pods.Items)
		if si.MatchingPods == 0 {
			signals.add(SignalSelectorMismatch)
		}
	}
	eps, err := c.Client.DiscoveryV1().EndpointSlices(s.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: discoveryv1.LabelServiceName + "=" + s.Name,
	})
	if err != nil {
		return si, err
	}
	for _, sl := range eps.Items {
		for _, ep := range sl.Endpoints {
			// A nil Ready condition means "unknown" and is treated as ready.
			if ep.Conditions.Ready == nil || *ep.Conditions.Ready {
				si.ReadyEndpoints++
			} else {
				si.NotReadyEndpoints++
			}
		}
	}
	if si.ReadyEndpoints == 0 && len(s.Spec.Selector) > 0 {
		signals.add(SignalNoReadyEndpoints)
	}
	si.Signals = signals
	return si, nil
}

func podLabelSample(ctx context.Context, c kube.Cluster, namespace string) ([]map[string]string, error) {
	pods, err := c.Client.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{Limit: 50})
	if err != nil {
		return nil, err
	}
	var out []map[string]string
	for _, p := range pods.Items {
		lbl := maps.Clone(p.Labels)
		delete(lbl, "pod-template-hash")
		if len(lbl) > 0 && !slices.ContainsFunc(out, func(m map[string]string) bool { return maps.Equal(m, lbl) }) {
			out = append(out, lbl)
		}
		if len(out) == 5 {
			break
		}
	}
	return out, nil
}
