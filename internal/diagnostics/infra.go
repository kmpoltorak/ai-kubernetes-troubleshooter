package diagnostics

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/domain"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/kube"
)

const (
	maxBackends   = 10
	maxListedPods = 500
)

// ---- Ingress ----

type IngressBackend struct {
	Service string `json:"service"`
	Port    string `json:"port"`
	// Problem is "service not found" or "port not exposed" when the backend cannot route.
	Problem string `json:"problem,omitempty"`
}

type IngressInfo struct {
	Name      string           `json:"name"`
	Class     string           `json:"class,omitempty"`
	Addresses int              `json:"addresses"`
	Hosts     []string         `json:"hosts,omitempty"`
	Backends  []IngressBackend `json:"backends"`
}

// getIngress checks that every backend Service exists and exposes the
// backend port, and that a controller published an address.
func getIngress(ctx context.Context, c kube.Cluster, in Input) (Result, error) {
	ing, err := c.Client.NetworkingV1().Ingresses(in.Namespace).Get(ctx, in.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return workloadNotFound("Ingress", in), nil
	}
	if err != nil {
		return Result{}, err
	}
	info := IngressInfo{Name: ing.Name, Class: ptr.Deref(ing.Spec.IngressClassName, ing.Annotations["kubernetes.io/ingress.class"]),
		Addresses: len(ing.Status.LoadBalancer.Ingress)}
	var backends []networkingv1.IngressServiceBackend
	if b := ing.Spec.DefaultBackend; b != nil && b.Service != nil {
		backends = append(backends, *b.Service)
	}
	for _, r := range ing.Spec.Rules {
		if r.Host != "" && !slices.Contains(info.Hosts, r.Host) {
			info.Hosts = append(info.Hosts, r.Host)
		}
		if r.HTTP == nil {
			continue
		}
		for _, p := range r.HTTP.Paths {
			if p.Backend.Service != nil && !slices.ContainsFunc(backends, func(b networkingv1.IngressServiceBackend) bool { return b == *p.Backend.Service }) {
				backends = append(backends, *p.Backend.Service)
			}
		}
	}

	var signals signalSet
	health := domain.Healthy
	for _, b := range backends[:min(len(backends), maxBackends)] {
		port := b.Port.Name
		if port == "" {
			port = strconv.Itoa(int(b.Port.Number))
		}
		ib := IngressBackend{Service: b.Name, Port: port}
		svc, err := c.Client.CoreV1().Services(in.Namespace).Get(ctx, b.Name, metav1.GetOptions{})
		switch {
		case apierrors.IsNotFound(err):
			ib.Problem = "service not found"
			signals.add(SignalIngressBackendMissing)
		case err != nil:
			return Result{}, err
		case !slices.ContainsFunc(svc.Spec.Ports, func(p corev1.ServicePort) bool {
			// A backend names the Service port either by name or by number.
			return b.Port.Name != "" && p.Name == b.Port.Name || b.Port.Name == "" && p.Port == b.Port.Number
		}):
			ib.Problem = "port not exposed"
			signals.add(SignalIngressPortMismatch)
		}
		if ib.Problem != "" {
			health = domain.Down
		}
		info.Backends = append(info.Backends, ib)
	}

	summary := fmt.Sprintf("Ingress %s routes to %d backend Services", ing.Name, len(info.Backends))
	if info.Class != "" {
		summary += " (class " + info.Class + ")"
	}
	summary += "."
	for _, b := range info.Backends {
		if b.Problem != "" {
			summary += fmt.Sprintf(" Backend %s:%s: %s.", b.Service, b.Port, b.Problem)
		}
	}
	if info.Addresses == 0 {
		signals.add(SignalIngressNoAddress)
		if health == domain.Healthy {
			health = domain.Degraded
		}
		summary += " No address is published: no ingress controller has admitted it yet."
	}
	return Result{Subject: "ingress/" + ing.Name, Health: health, Summary: summary, Signals: signals, Data: info}, nil
}

// FirstRoutableBackend names the first backend Service that exists, if any.
func (i IngressInfo) FirstRoutableBackend() string {
	for _, b := range i.Backends {
		if b.Problem != "service not found" {
			return b.Service
		}
	}
	return ""
}

// ---- PersistentVolumeClaim ----

type PVCInfo struct {
	Name         string      `json:"name"`
	Phase        string      `json:"phase"`
	StorageClass string      `json:"storage_class,omitempty"`
	Requested    string      `json:"requested,omitempty"`
	Capacity     string      `json:"capacity,omitempty"`
	AccessModes  []string    `json:"access_modes,omitempty"`
	UsedBy       []string    `json:"used_by,omitempty"`
	Conditions   []Condition `json:"conditions,omitempty"`
}

func getPVC(ctx context.Context, c kube.Cluster, in Input) (Result, error) {
	subject := "persistentvolumeclaim/" + in.Name
	pvc, err := c.Client.CoreV1().PersistentVolumeClaims(in.Namespace).Get(ctx, in.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return Result{Subject: subject, Health: domain.Down, Summary: fmt.Sprintf("PersistentVolumeClaim %s not found in %s.", in.Name, in.Namespace),
			Signals: []string{SignalPVCMissing}, Data: PVCInfo{Name: in.Name}}, nil
	}
	if err != nil {
		return Result{}, err
	}
	info := PVCInfo{Name: pvc.Name, Phase: string(pvc.Status.Phase), StorageClass: ptr.Deref(pvc.Spec.StorageClassName, "")}
	if q, ok := pvc.Spec.Resources.Requests[corev1.ResourceStorage]; ok {
		info.Requested = q.String()
	}
	if q, ok := pvc.Status.Capacity[corev1.ResourceStorage]; ok {
		info.Capacity = q.String()
	}
	for _, m := range pvc.Spec.AccessModes {
		info.AccessModes = append(info.AccessModes, string(m))
	}
	for _, cond := range pvc.Status.Conditions {
		info.Conditions = append(info.Conditions, condition(string(cond.Type), string(cond.Status), cond.Reason, cond.Message))
	}
	// ponytail: scans up to maxListedPods pods for users; a busier namespace may miss some.
	pods, err := c.Client.CoreV1().Pods(in.Namespace).List(ctx, metav1.ListOptions{Limit: maxListedPods})
	if err != nil {
		return Result{}, fmt.Errorf("list pods using the claim: %w", err)
	}
	for _, p := range pods.Items {
		if slices.ContainsFunc(p.Spec.Volumes, func(v corev1.Volume) bool {
			return v.PersistentVolumeClaim != nil && v.PersistentVolumeClaim.ClaimName == pvc.Name
		}) {
			info.UsedBy = append(info.UsedBy, p.Name)
		}
	}
	slices.Sort(info.UsedBy)

	var signals signalSet
	health, summary := domain.Healthy, fmt.Sprintf("PersistentVolumeClaim %s is %s", pvc.Name, info.Phase)
	switch {
	case pvc.Status.Phase == corev1.ClaimLost:
		health = domain.Down
		signals.add(SignalPVCLost)
		summary += ": its PersistentVolume no longer exists."
	case pvc.Status.Phase == corev1.ClaimPending && len(info.UsedBy) > 0:
		health = domain.Down
		signals.add(SignalPVCPending)
		summary += fmt.Sprintf(" while %d pods need it.", len(info.UsedBy))
	case pvc.Status.Phase == corev1.ClaimPending:
		// WaitForFirstConsumer classes bind only once a pod uses the claim; health is unconfirmed.
		health = domain.Degraded
		summary += "; no pod uses it yet, and a WaitForFirstConsumer class binds only then."
	default:
		summary += fmt.Sprintf(" (%s, used by %d pods).", info.Capacity, len(info.UsedBy))
	}
	if info.StorageClass != "" {
		summary += " StorageClass " + info.StorageClass + "."
	}
	return Result{Subject: subject, Health: health, Summary: summary, Signals: signals, Data: info}, nil
}

// ---- HorizontalPodAutoscaler ----

type HPAInfo struct {
	Name            string      `json:"name"`
	TargetKind      string      `json:"target_kind"`
	TargetName      string      `json:"target_name"`
	MinReplicas     int32       `json:"min_replicas"`
	MaxReplicas     int32       `json:"max_replicas"`
	CurrentReplicas int32       `json:"current_replicas"`
	DesiredReplicas int32       `json:"desired_replicas"`
	Conditions      []Condition `json:"conditions,omitempty"`
}

func getHPA(ctx context.Context, c kube.Cluster, in Input) (Result, error) {
	h, err := c.Client.AutoscalingV2().HorizontalPodAutoscalers(in.Namespace).Get(ctx, in.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return workloadNotFound("HorizontalPodAutoscaler", in), nil
	}
	if err != nil {
		return Result{}, err
	}
	info := HPAInfo{Name: h.Name, TargetKind: h.Spec.ScaleTargetRef.Kind, TargetName: h.Spec.ScaleTargetRef.Name,
		MinReplicas: ptr.Deref(h.Spec.MinReplicas, 1), MaxReplicas: h.Spec.MaxReplicas,
		CurrentReplicas: h.Status.CurrentReplicas, DesiredReplicas: h.Status.DesiredReplicas}
	var signals signalSet
	var notes []string
	for _, cond := range h.Status.Conditions {
		info.Conditions = append(info.Conditions, condition(string(cond.Type), string(cond.Status), cond.Reason, cond.Message))
		switch {
		case cond.Type == autoscalingv2.AbleToScale && cond.Status == corev1.ConditionFalse:
			signals.add(SignalHPAUnableToScale)
			notes = append(notes, "cannot scale: "+cond.Reason)
		case cond.Type == autoscalingv2.ScalingActive && cond.Status == corev1.ConditionFalse:
			signals.add(SignalHPAMetricsUnavailable)
			notes = append(notes, "cannot compute a scale: "+cond.Reason)
		case cond.Type == autoscalingv2.ScalingLimited && cond.Status == corev1.ConditionTrue && cond.Reason == "TooManyReplicas":
			signals.add(SignalHPAAtMaxReplicas)
			notes = append(notes, "held at its maximum")
		}
	}
	health := domain.Healthy
	if len(signals) > 0 {
		health = domain.Degraded
	}
	summary := fmt.Sprintf("HorizontalPodAutoscaler %s scales %s/%s between %d and %d replicas; current %d, desired %d.",
		h.Name, info.TargetKind, info.TargetName, info.MinReplicas, info.MaxReplicas, info.CurrentReplicas, info.DesiredReplicas)
	if len(notes) > 0 {
		summary += " It " + strings.Join(notes, "; ") + "."
	}
	return Result{Subject: "horizontalpodautoscaler/" + h.Name, Health: health, Summary: summary, Signals: signals, Data: info}, nil
}

// ---- Nodes ----

type NodeInfo struct {
	Name           string            `json:"name"`
	Ready          string            `json:"ready"`
	Unschedulable  bool              `json:"unschedulable,omitempty"`
	Pressure       []string          `json:"pressure,omitempty"`
	Taints         []string          `json:"taints,omitempty"`
	Allocatable    map[string]string `json:"allocatable,omitempty"`
	KubeletVersion string            `json:"kubelet_version,omitempty"`
}

type NodesInfo struct {
	Nodes []NodeInfo `json:"nodes"`
}

// getNodes reads the nodes in.Names (those running the target's pods).
// Addresses are not recorded.
func getNodes(ctx context.Context, c kube.Cluster, in Input) (Result, error) {
	if len(in.Names) == 0 {
		return Result{}, fmt.Errorf("names (node names) are required")
	}
	var info NodesInfo
	var signals signalSet
	health, problem := domain.Healthy, ""
	for _, name := range in.Names {
		n, err := c.Client.CoreV1().Nodes().Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			info.Nodes = append(info.Nodes, NodeInfo{Name: name, Ready: "NotFound"})
			signals.add(SignalNodeNotReady)
			health, problem = domain.Down, cmp.Or(problem, name)
			continue
		}
		if err != nil {
			return Result{}, err
		}
		ni := NodeInfo{Name: n.Name, Ready: "Unknown", Unschedulable: n.Spec.Unschedulable,
			Allocatable: quantities(n.Status.Allocatable), KubeletVersion: n.Status.NodeInfo.KubeletVersion}
		for _, t := range n.Spec.Taints {
			ni.Taints = append(ni.Taints, t.Key+":"+string(t.Effect))
		}
		for _, cond := range n.Status.Conditions {
			switch {
			case cond.Type == corev1.NodeReady:
				ni.Ready = string(cond.Status)
			case cond.Status == corev1.ConditionTrue: // MemoryPressure, DiskPressure, PIDPressure, NetworkUnavailable
				ni.Pressure = append(ni.Pressure, string(cond.Type))
			}
		}
		switch {
		case ni.Ready != string(corev1.ConditionTrue):
			signals.add(SignalNodeNotReady)
			health, problem = domain.Down, cmp.Or(problem, n.Name)
		case len(ni.Pressure) > 0:
			signals.add(SignalNodePressure)
			problem = cmp.Or(problem, n.Name)
			if health == domain.Healthy {
				health = domain.Degraded
			}
		}
		info.Nodes = append(info.Nodes, ni)
	}
	parts := make([]string, 0, len(info.Nodes))
	for _, n := range info.Nodes {
		p := n.Name + ": Ready=" + n.Ready
		if len(n.Pressure) > 0 {
			p += ", " + strings.Join(n.Pressure, ", ")
		}
		if n.Unschedulable {
			p += ", cordoned"
		}
		parts = append(parts, p)
	}
	subject := "node/" + cmp.Or(problem, info.Nodes[0].Name)
	return Result{Subject: subject, Health: health, Summary: "Nodes running the pods: " + strings.Join(parts, "; ") + ".", Signals: signals, Data: info}, nil
}
