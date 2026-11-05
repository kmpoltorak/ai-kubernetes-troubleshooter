package diagnostics

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/domain"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/kube"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/redact"
)

// The registry answered that the image reference does not exist.
var imageNotFound = regexp.MustCompile(`(?i)not found|manifest unknown|does not exist`)

const (
	maxMessageBytes = 300
	maxPods         = 20
)

type NamespaceInfo struct {
	Name   string `json:"name"`
	Exists bool   `json:"exists"`
	Phase  string `json:"phase,omitempty"`
}

func getNamespace(ctx context.Context, c kube.Cluster, in Input) (Result, error) {
	subject := "namespace/" + in.Namespace
	ns, err := c.Client.CoreV1().Namespaces().Get(ctx, in.Namespace, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return Result{Subject: subject, Health: domain.Down, Summary: fmt.Sprintf("Namespace %s does not exist.", in.Namespace),
			Signals: []string{SignalNamespaceNotFound}, Data: NamespaceInfo{Name: in.Namespace}}, nil
	}
	if err != nil {
		return Result{}, err
	}
	info := NamespaceInfo{Name: ns.Name, Exists: true, Phase: string(ns.Status.Phase)}
	return Result{Subject: subject, Health: domain.Healthy, Summary: fmt.Sprintf("Namespace %s exists (%s).", ns.Name, info.Phase), Data: info}, nil
}

// ---- Pod spec summary (shared by deployments and pods) ----

type ContainerSpec struct {
	Name           string            `json:"name"`
	Image          string            `json:"image"`
	Requests       map[string]string `json:"requests,omitempty"`
	Limits         map[string]string `json:"limits,omitempty"`
	ReadinessProbe string            `json:"readiness_probe,omitempty"`
	LivenessProbe  string            `json:"liveness_probe,omitempty"`
	StartupProbe   string            `json:"startup_probe,omitempty"`
	// Env lists variable names and where values come from, never values.
	Env []EnvVarMeta `json:"env,omitempty"`
}

type EnvVarMeta struct {
	Name   string `json:"name"`
	Source string `json:"source"`
}

// PodSpecSummary is what the analyzer needs from a pod template.
type PodSpecSummary struct {
	Containers []ContainerSpec `json:"containers"`
	// ConfigMaps and Secrets referenced by volumes, env and envFrom
	// (optional references excluded).
	ConfigMaps []string `json:"configmaps,omitempty"`
	Secrets    []string `json:"secrets,omitempty"`
	// PVCs are the PersistentVolumeClaims mounted as volumes.
	PVCs []string `json:"pvcs,omitempty"`
}

// GetPVCs is nil-safe: a target without a template has no spec.
func (s *PodSpecSummary) GetPVCs() []string {
	if s == nil {
		return nil
	}
	return s.PVCs
}

func summarizeSpec(spec corev1.PodSpec) PodSpecSummary {
	var s PodSpecSummary
	cms, secrets, pvcs := signalSet{}, signalSet{}, signalSet{}
	for _, v := range spec.Volumes {
		if v.PersistentVolumeClaim != nil {
			pvcs.add(v.PersistentVolumeClaim.ClaimName)
		}
		if cm := v.ConfigMap; cm != nil && !isTrue(cm.Optional) {
			cms.add(cm.Name)
		}
		if sec := v.Secret; sec != nil && !isTrue(sec.Optional) {
			secrets.add(sec.SecretName)
		}
		if p := v.Projected; p != nil {
			for _, src := range p.Sources {
				// kube-root-ca.crt is the cluster CA Kubernetes mounts into every pod: not a workload reference.
				if src.ConfigMap != nil && !isTrue(src.ConfigMap.Optional) && src.ConfigMap.Name != "kube-root-ca.crt" {
					cms.add(src.ConfigMap.Name)
				}
				if src.Secret != nil && !isTrue(src.Secret.Optional) {
					secrets.add(src.Secret.Name)
				}
			}
		}
	}
	for _, ctr := range slices.Concat(spec.InitContainers, spec.Containers) {
		cs := ContainerSpec{
			Name: ctr.Name, Image: ctr.Image,
			Requests: quantities(ctr.Resources.Requests), Limits: quantities(ctr.Resources.Limits),
			ReadinessProbe: probe(ctr.ReadinessProbe), LivenessProbe: probe(ctr.LivenessProbe), StartupProbe: probe(ctr.StartupProbe),
		}
		for _, e := range ctr.Env {
			meta := EnvVarMeta{Name: e.Name, Source: "value"}
			if vf := e.ValueFrom; vf != nil {
				switch {
				case vf.ConfigMapKeyRef != nil:
					meta.Source = "configmap:" + vf.ConfigMapKeyRef.Name + "/" + vf.ConfigMapKeyRef.Key
					if !isTrue(vf.ConfigMapKeyRef.Optional) {
						cms.add(vf.ConfigMapKeyRef.Name)
					}
				case vf.SecretKeyRef != nil:
					meta.Source = "secret:" + vf.SecretKeyRef.Name
					if !isTrue(vf.SecretKeyRef.Optional) {
						secrets.add(vf.SecretKeyRef.Name)
					}
				case vf.FieldRef != nil:
					meta.Source = "field:" + vf.FieldRef.FieldPath
				case vf.ResourceFieldRef != nil:
					meta.Source = "resource:" + vf.ResourceFieldRef.Resource
				}
			}
			cs.Env = append(cs.Env, meta)
		}
		for _, ef := range ctr.EnvFrom {
			if ef.ConfigMapRef != nil {
				cs.Env = append(cs.Env, EnvVarMeta{Name: ef.Prefix + "*", Source: "configmap:" + ef.ConfigMapRef.Name})
				if !isTrue(ef.ConfigMapRef.Optional) {
					cms.add(ef.ConfigMapRef.Name)
				}
			}
			if ef.SecretRef != nil {
				cs.Env = append(cs.Env, EnvVarMeta{Name: ef.Prefix + "*", Source: "secret:" + ef.SecretRef.Name})
				if !isTrue(ef.SecretRef.Optional) {
					secrets.add(ef.SecretRef.Name)
				}
			}
		}
		// Only app containers are listed; init containers contribute refs.
		if slices.ContainsFunc(spec.Containers, func(c corev1.Container) bool { return c.Name == ctr.Name }) {
			s.Containers = append(s.Containers, cs)
		}
	}
	s.ConfigMaps, s.Secrets, s.PVCs = cms, secrets, pvcs
	return s
}

func quantities(rl corev1.ResourceList) map[string]string {
	if len(rl) == 0 {
		return nil
	}
	out := map[string]string{}
	for k, v := range rl {
		out[string(k)] = v.String()
	}
	return out
}

func probe(p *corev1.Probe) string {
	if p == nil {
		return ""
	}
	var what string
	switch h := p.ProbeHandler; {
	case h.HTTPGet != nil:
		what = fmt.Sprintf("httpGet %s port %s", h.HTTPGet.Path, h.HTTPGet.Port.String())
	case h.TCPSocket != nil:
		what = "tcpSocket port " + h.TCPSocket.Port.String()
	case h.GRPC != nil:
		what = fmt.Sprintf("grpc port %d", h.GRPC.Port)
	case h.Exec != nil:
		// The command itself is not reported: it may embed credentials.
		what = "exec"
	}
	return fmt.Sprintf("%s delay=%ds period=%ds timeout=%ds failureThreshold=%d",
		what, p.InitialDelaySeconds, p.PeriodSeconds, p.TimeoutSeconds, p.FailureThreshold)
}

func isTrue(b *bool) bool { return b != nil && *b }

// ---- Controllers with replicas: Deployment, StatefulSet, DaemonSet ----

type Condition struct {
	Type    string `json:"type"`
	Status  string `json:"status"`
	Reason  string `json:"reason,omitempty"`
	Message string `json:"message,omitempty"`
}

// WorkloadInfo describes a pod controller: Deployment, StatefulSet, DaemonSet or Job.
type WorkloadInfo struct {
	Name              string            `json:"name"`
	Kind              string            `json:"kind,omitempty"`
	DesiredReplicas   int32             `json:"desired_replicas"`
	ReadyReplicas     int32             `json:"ready_replicas"`
	AvailableReplicas int32             `json:"available_replicas"`
	UpdatedReplicas   int32             `json:"updated_replicas"`
	RolloutComplete   bool              `json:"rollout_complete"`
	Conditions        []Condition       `json:"conditions"`
	Selector          string            `json:"selector"`
	PodLabels         map[string]string `json:"pod_labels"`
	Template          PodSpecSummary    `json:"template"`
	// Job only.
	Active    int32 `json:"active,omitempty"`
	Succeeded int32 `json:"succeeded,omitempty"`
	Failed    int32 `json:"failed,omitempty"`
}

// workloadNotFound is a valid observation: the target does not exist.
func workloadNotFound(kind string, in Input) Result {
	return Result{Subject: strings.ToLower(kind) + "/" + in.Name, Health: domain.Down,
		Summary: fmt.Sprintf("%s %s not found in %s.", kind, in.Name, in.Namespace),
		Signals: []string{SignalWorkloadNotFound}, Data: WorkloadInfo{Name: in.Name, Kind: kind}}
}

// newWorkload fills the fields every controller shares.
func newWorkload(kind, name string, selector *metav1.LabelSelector, tmpl corev1.PodTemplateSpec) (WorkloadInfo, error) {
	sel, err := metav1.LabelSelectorAsSelector(selector)
	if err != nil {
		return WorkloadInfo{}, fmt.Errorf("%s selector: %w", strings.ToLower(kind), err)
	}
	return WorkloadInfo{Name: name, Kind: kind, Selector: sel.String(), PodLabels: tmpl.Labels, Template: summarizeSpec(tmpl.Spec)}, nil
}

func condition(typ, status, reason, message string) Condition {
	return Condition{Type: typ, Status: status, Reason: reason, Message: truncate(redact.Text(message), maxMessageBytes)}
}

// replicaResult rates a controller by ready replicas against the desired count.
func replicaResult(info WorkloadInfo, signals signalSet) Result {
	health, summary := domain.Healthy, fmt.Sprintf("%s %s has %d/%d ready replicas.", info.Kind, info.Name, info.ReadyReplicas, info.DesiredReplicas)
	switch {
	case info.DesiredReplicas == 0 && info.Kind == "DaemonSet":
		health = domain.Degraded
		signals.add(SignalNoEligibleNodes)
		summary = fmt.Sprintf("DaemonSet %s schedules no pods: no node matches its node selector, affinity and tolerations.", info.Name)
	case info.DesiredReplicas == 0:
		health = domain.Degraded
		signals.add(SignalScaledToZero)
		summary = fmt.Sprintf("%s %s is scaled to zero replicas.", info.Kind, info.Name)
	case info.ReadyReplicas == 0:
		health = domain.Down
		signals.add(SignalReplicasUnavailable)
	case info.ReadyReplicas < info.DesiredReplicas:
		health = domain.Degraded
		signals.add(SignalReplicasUnavailable)
	}
	if !info.RolloutComplete && info.DesiredReplicas > 0 {
		summary += " Rollout is not complete."
	}
	return Result{Subject: strings.ToLower(info.Kind) + "/" + info.Name, Health: health, Summary: summary, Signals: signals, Data: info}
}

func getDeployment(ctx context.Context, c kube.Cluster, in Input) (Result, error) {
	d, err := c.Client.AppsV1().Deployments(in.Namespace).Get(ctx, in.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return workloadNotFound("Deployment", in), nil
	}
	if err != nil {
		return Result{}, err
	}
	info, err := newWorkload("Deployment", d.Name, d.Spec.Selector, d.Spec.Template)
	if err != nil {
		return Result{}, err
	}
	info.DesiredReplicas = ptr.Deref(d.Spec.Replicas, 1)
	st := d.Status
	info.ReadyReplicas, info.AvailableReplicas, info.UpdatedReplicas = st.ReadyReplicas, st.AvailableReplicas, st.UpdatedReplicas
	info.RolloutComplete = st.ObservedGeneration >= d.Generation && st.UpdatedReplicas == info.DesiredReplicas &&
		st.Replicas == info.DesiredReplicas && st.AvailableReplicas == info.DesiredReplicas

	var signals signalSet
	for _, cond := range st.Conditions {
		info.Conditions = append(info.Conditions, condition(string(cond.Type), string(cond.Status), cond.Reason, cond.Message))
		if cond.Type == appsv1.DeploymentProgressing && cond.Reason == "ProgressDeadlineExceeded" {
			signals.add(SignalRolloutStuck)
		}
	}
	return replicaResult(info, signals), nil
}

func getStatefulSet(ctx context.Context, c kube.Cluster, in Input) (Result, error) {
	s, err := c.Client.AppsV1().StatefulSets(in.Namespace).Get(ctx, in.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return workloadNotFound("StatefulSet", in), nil
	}
	if err != nil {
		return Result{}, err
	}
	info, err := newWorkload("StatefulSet", s.Name, s.Spec.Selector, s.Spec.Template)
	if err != nil {
		return Result{}, err
	}
	info.DesiredReplicas = ptr.Deref(s.Spec.Replicas, 1)
	st := s.Status
	info.ReadyReplicas, info.AvailableReplicas, info.UpdatedReplicas = st.ReadyReplicas, st.AvailableReplicas, st.UpdatedReplicas
	info.RolloutComplete = st.ObservedGeneration >= s.Generation && st.UpdatedReplicas == info.DesiredReplicas &&
		st.ReadyReplicas == info.DesiredReplicas && (st.UpdateRevision == "" || st.CurrentRevision == st.UpdateRevision)
	for _, cond := range st.Conditions {
		info.Conditions = append(info.Conditions, condition(string(cond.Type), string(cond.Status), cond.Reason, cond.Message))
	}
	return replicaResult(info, nil), nil
}

func getDaemonSet(ctx context.Context, c kube.Cluster, in Input) (Result, error) {
	d, err := c.Client.AppsV1().DaemonSets(in.Namespace).Get(ctx, in.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return workloadNotFound("DaemonSet", in), nil
	}
	if err != nil {
		return Result{}, err
	}
	info, err := newWorkload("DaemonSet", d.Name, d.Spec.Selector, d.Spec.Template)
	if err != nil {
		return Result{}, err
	}
	st := d.Status
	info.DesiredReplicas, info.ReadyReplicas, info.AvailableReplicas, info.UpdatedReplicas = st.DesiredNumberScheduled, st.NumberReady, st.NumberAvailable, st.UpdatedNumberScheduled
	info.RolloutComplete = st.ObservedGeneration >= d.Generation && st.UpdatedNumberScheduled == st.DesiredNumberScheduled &&
		st.NumberAvailable == st.DesiredNumberScheduled
	for _, cond := range st.Conditions {
		info.Conditions = append(info.Conditions, condition(string(cond.Type), string(cond.Status), cond.Reason, cond.Message))
	}
	r := replicaResult(info, nil)
	if st.NumberMisscheduled > 0 {
		r.Summary += fmt.Sprintf(" %d pods run on nodes they should not.", st.NumberMisscheduled)
	}
	return r, nil
}

// ---- Pods ----

type ContainerStatus struct {
	Name                  string            `json:"name"`
	Init                  bool              `json:"init,omitempty"`
	Image                 string            `json:"image"`
	Ready                 bool              `json:"ready"`
	RestartCount          int32             `json:"restart_count"`
	State                 string            `json:"state"`
	Reason                string            `json:"reason,omitempty"`
	Message               string            `json:"message,omitempty"`
	ExitCode              *int32            `json:"exit_code,omitempty"`
	LastTerminationReason string            `json:"last_termination_reason,omitempty"`
	LastExitCode          *int32            `json:"last_exit_code,omitempty"`
	LastFinishedAt        *metav1.Time      `json:"last_finished_at,omitempty"`
	Requests              map[string]string `json:"requests,omitempty"`
	Limits                map[string]string `json:"limits,omitempty"`
}

type PodInfo struct {
	Name       string            `json:"name"`
	Phase      string            `json:"phase"`
	Reason     string            `json:"reason,omitempty"`
	Message    string            `json:"message,omitempty"`
	Node       string            `json:"node,omitempty"`
	Ready      bool              `json:"ready"`
	Restarts   int32             `json:"restarts"`
	Labels     map[string]string `json:"labels,omitempty"`
	Conditions []Condition       `json:"conditions,omitempty"`
	Containers []ContainerStatus `json:"containers"`
	Signals    []string          `json:"signals,omitempty"`
}

type PodsInfo struct {
	Selector string `json:"selector,omitempty"`
	Total    int    `json:"total"`
	Ready    int    `json:"ready"`
	// Completed pods (phase Succeeded) finished their work, e.g. Job pods.
	Completed int       `json:"completed,omitempty"`
	Pods      []PodInfo `json:"pods"`
	// Spec is the pod spec summary of the first pod, used when there is no
	// controller template (pod and service targets).
	Spec *PodSpecSummary `json:"spec,omitempty"`
}

// getPods inspects one pod (Name) or the pods matching LabelSelector.
func getPods(ctx context.Context, c kube.Cluster, in Input) (Result, error) {
	var pods []corev1.Pod
	subject := "pods/" + in.LabelSelector
	if in.Name != "" {
		subject = "pod/" + in.Name
		p, err := c.Client.CoreV1().Pods(in.Namespace).Get(ctx, in.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return Result{Subject: subject, Health: domain.Down, Summary: fmt.Sprintf("Pod %s not found in %s.", in.Name, in.Namespace),
				Signals: []string{SignalWorkloadNotFound}, Data: PodsInfo{}}, nil
		}
		if err != nil {
			return Result{}, err
		}
		pods = []corev1.Pod{*p}
	} else {
		if in.LabelSelector == "" {
			return Result{}, fmt.Errorf("name or label_selector is required")
		}
		list, err := c.Client.CoreV1().Pods(in.Namespace).List(ctx, metav1.ListOptions{LabelSelector: in.LabelSelector})
		if err != nil {
			return Result{}, err
		}
		pods = list.Items
	}

	info := PodsInfo{Selector: in.LabelSelector, Total: len(pods)}
	if len(pods) == 0 {
		return Result{Subject: subject, Health: domain.Down, Summary: fmt.Sprintf("No pods match %s.", in.LabelSelector),
			Signals: []string{SignalNoPods}, Data: info}, nil
	}
	sort.Slice(pods, func(i, j int) bool { return pods[i].Name < pods[j].Name })
	spec := summarizeSpec(pods[0].Spec)
	info.Spec = &spec

	var signals signalSet
	for _, p := range pods {
		pi := podInfo(p)
		signals.add(pi.Signals...)
		switch {
		case pi.Ready:
			info.Ready++
		case pi.Phase == string(corev1.PodSucceeded):
			// A Job pod that finished its work is done, not unready.
			info.Completed++
		}
		info.Pods = append(info.Pods, pi)
	}
	// Pods with problems first, so the detail cap never drops them; later
	// steps read logs and metrics only for the pods kept here.
	healthy := func(p PodInfo) bool {
		return (p.Ready || p.Phase == string(corev1.PodSucceeded)) && len(p.Signals) == 0
	}
	sort.SliceStable(info.Pods, func(i, j int) bool { return !healthy(info.Pods[i]) && healthy(info.Pods[j]) })
	info.Pods = info.Pods[:min(len(info.Pods), maxPods)]

	health := domain.Healthy
	switch {
	case info.Ready+info.Completed == 0:
		health = domain.Down
	case info.Ready+info.Completed < info.Total || len(signals) > 0:
		health = domain.Degraded
	}
	summary := fmt.Sprintf("%d/%d pods ready.", info.Ready, info.Total)
	if info.Completed > 0 {
		summary = fmt.Sprintf("%d/%d pods ready, %d completed.", info.Ready, info.Total, info.Completed)
	}
	if len(signals) > 0 {
		summary += " Findings: " + strings.Join(signals, ", ") + "."
	}
	if len(info.Pods) < info.Total {
		summary += fmt.Sprintf(" Details for %d of %d pods, problems first.", len(info.Pods), info.Total)
	}
	return Result{Subject: subject, Health: health, Summary: summary, Signals: signals, Data: info}, nil
}

func podInfo(p corev1.Pod) PodInfo {
	pi := PodInfo{
		Name: p.Name, Phase: string(p.Status.Phase), Reason: p.Status.Reason,
		Message: truncate(redact.Text(p.Status.Message), maxMessageBytes), Node: p.Spec.NodeName, Labels: p.Labels,
	}
	var signals signalSet
	for _, cond := range p.Status.Conditions {
		pi.Conditions = append(pi.Conditions, Condition{Type: string(cond.Type), Status: string(cond.Status),
			Reason: cond.Reason, Message: truncate(redact.Text(cond.Message), maxMessageBytes)})
		if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
			pi.Ready = true
		}
		if cond.Type == corev1.PodScheduled && cond.Status == corev1.ConditionFalse && cond.Reason == corev1.PodReasonUnschedulable {
			signals.add(SignalUnschedulable)
		}
	}
	if p.Status.Reason == "Evicted" {
		signals.add(SignalEvicted)
	}

	resources := map[string]corev1.ResourceRequirements{}
	for _, ctr := range slices.Concat(p.Spec.InitContainers, p.Spec.Containers) {
		resources[ctr.Name] = ctr.Resources
	}
	statuses := slices.Concat(p.Status.InitContainerStatuses, p.Status.ContainerStatuses)
	for i, cs := range statuses {
		st := ContainerStatus{Name: cs.Name, Init: i < len(p.Status.InitContainerStatuses), Image: cs.Image, Ready: cs.Ready,
			RestartCount: cs.RestartCount, Requests: quantities(resources[cs.Name].Requests), Limits: quantities(resources[cs.Name].Limits)}
		pi.Restarts += cs.RestartCount
		switch s := cs.State; {
		case s.Waiting != nil:
			st.State, st.Reason, st.Message = "waiting", s.Waiting.Reason, truncate(redact.Text(s.Waiting.Message), maxMessageBytes)
			switch s.Waiting.Reason {
			case "CrashLoopBackOff":
				signals.add(SignalCrashLoopBackOff)
			case "ImagePullBackOff", "ErrImagePull", "InvalidImageName":
				signals.add(SignalImagePullError)
				if imageNotFound.MatchString(s.Waiting.Message) {
					signals.add(SignalImageNotFound)
				}
			case "CreateContainerConfigError", "CreateContainerError":
				signals.add(SignalContainerConfigError)
			}
		case s.Terminated != nil:
			st.State, st.Reason = "terminated", s.Terminated.Reason
			st.ExitCode = &s.Terminated.ExitCode
			terminationSignals(&signals, s.Terminated.Reason, s.Terminated.ExitCode)
		case s.Running != nil:
			st.State = "running"
			// A running init container is not ready until it completes.
			if !cs.Ready && !st.Init {
				signals.add(SignalPodNotReady)
			}
		}
		if t := cs.LastTerminationState.Terminated; t != nil {
			st.LastTerminationReason, st.LastExitCode, st.LastFinishedAt = t.Reason, &t.ExitCode, &t.FinishedAt
			terminationSignals(&signals, t.Reason, t.ExitCode)
		}
		if cs.RestartCount >= 3 {
			signals.add(SignalFrequentRestarts)
		}
		pi.Containers = append(pi.Containers, st)
	}
	pi.Signals = signals
	return pi
}

func terminationSignals(signals *signalSet, reason string, exitCode int32) {
	switch {
	case reason == "OOMKilled":
		signals.add(SignalOOMKilled)
	case exitCode != 0:
		signals.add(SignalContainerExitError)
	}
}
