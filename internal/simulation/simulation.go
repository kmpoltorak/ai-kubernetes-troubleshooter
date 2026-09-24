// Package simulation builds deterministic fake clusters for demo and test
// use. A scenario is a set of Kubernetes objects, container logs and usage
// metrics loaded into client-go fake clients, so the real diagnostic tools
// run unchanged against it.
package simulation

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes/fake"
	metadatafake "k8s.io/client-go/metadata/fake"
	k8stesting "k8s.io/client-go/testing"
	"k8s.io/utils/ptr"

	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/domain"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/kube"
)

// Scenarios supported by simulation mode.
var Scenarios = []string{
	"healthy", "crash_loop_backoff", "image_pull_error", "oom_killed", "readiness_probe_failure",
	"liveness_probe_failure", "missing_configmap", "service_selector_mismatch", "dns_failure",
	"resource_pressure", "network_policy_block",
}

// Now is the fixed reference time of every simulated timestamp.
var Now = time.Date(2026, 9, 1, 8, 30, 0, 0, time.UTC)

// Clusters hands out a simulated cluster per investigation.
type Clusters struct {
	DefaultScenario string
}

func NewClusters(defaultScenario string) (Clusters, error) {
	if !slices.Contains(Scenarios, defaultScenario) {
		return Clusters{}, fmt.Errorf("unknown simulation scenario %q (valid: %v)", defaultScenario, Scenarios)
	}
	return Clusters{DefaultScenario: defaultScenario}, nil
}

// For builds the scenario (or the default) around the incident target and
// returns the effective scenario name.
func (c Clusters) For(target domain.Target, scenario string) (kube.Cluster, string, error) {
	if scenario == "" {
		scenario = c.DefaultScenario
	}
	cl, err := Build(scenario, target)
	return cl, scenario, err
}

// world is the mutable scenario state before it is loaded into fakes.
type world struct {
	ns, app   string
	labels    map[string]string
	deploy    *appsv1.Deployment
	pods      []*corev1.Pod
	service   *corev1.Service
	configMap *corev1.ConfigMap
	policies  []*networkingv1.NetworkPolicy
	events    []*corev1.Event
	// logs by previous=false/true; the fake log API does not see pod names,
	// so all pods of the workload share them.
	logs, previousLogs string
	logErr             error
	memoryBytes        int64
	cpuMilli           int64
}

// Build returns the fake cluster for scenario, named after target: the
// workload is target.ResourceName in target.Namespace. For a pod target the
// first pod carries the target's name.
func Build(scenario string, target domain.Target) (kube.Cluster, error) {
	if !slices.Contains(Scenarios, scenario) {
		return kube.Cluster{}, fmt.Errorf("unknown simulation scenario %q (valid: %v)", scenario, Scenarios)
	}
	w := healthy(target)
	switch scenario {
	case "crash_loop_backoff":
		crashLoop(w)
	case "image_pull_error":
		imagePullError(w)
	case "oom_killed":
		oomKilled(w)
	case "readiness_probe_failure":
		readinessFailure(w)
	case "liveness_probe_failure":
		livenessFailure(w)
	case "missing_configmap":
		missingConfigMap(w)
	case "service_selector_mismatch":
		selectorMismatch(w)
	case "dns_failure":
		dnsFailure(w)
	case "resource_pressure":
		resourcePressure(w)
	case "network_policy_block":
		networkPolicyBlock(w)
	}
	return w.cluster()
}

func healthy(t domain.Target) *world {
	app := t.ResourceName
	w := &world{ns: t.Namespace, app: app, labels: map[string]string{"app": app}, memoryBytes: 118 << 20, cpuMilli: 85}
	image := "registry.example.com/" + app + ":1.4.2"
	container := corev1.Container{
		Name: "app", Image: image,
		Ports: []corev1.ContainerPort{{Name: "http", ContainerPort: 8080}},
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("250m"), corev1.ResourceMemory: resource.MustParse("128Mi")},
			Limits:   corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("500m"), corev1.ResourceMemory: resource.MustParse("256Mi")},
		},
		ReadinessProbe: httpProbe("/ready", 5),
		LivenessProbe:  httpProbe("/healthz", 10),
		EnvFrom:        []corev1.EnvFromSource{{ConfigMapRef: &corev1.ConfigMapEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: app + "-config"}}}},
		Env: []corev1.EnvVar{{Name: "DB_PASSWORD", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: app + "-db"}, Key: "password"}}}},
	}
	w.deploy = &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: app, Namespace: w.ns, Generation: 4, CreationTimestamp: metav1.NewTime(Now.Add(-72 * time.Hour))},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To[int32](2),
			Selector: &metav1.LabelSelector{MatchLabels: w.labels},
			Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: w.labels}, Spec: corev1.PodSpec{Containers: []corev1.Container{container}}},
		},
		Status: appsv1.DeploymentStatus{ObservedGeneration: 4, Replicas: 2, UpdatedReplicas: 2, ReadyReplicas: 2, AvailableReplicas: 2,
			Conditions: []appsv1.DeploymentCondition{
				{Type: appsv1.DeploymentAvailable, Status: corev1.ConditionTrue, Reason: "MinimumReplicasAvailable"},
				{Type: appsv1.DeploymentProgressing, Status: corev1.ConditionTrue, Reason: "NewReplicaSetAvailable"},
			}},
	}
	names := []string{app + "-7c9d8f6b5-x2x9q", app + "-7c9d8f6b5-k4m7p"}
	if t.ResourceType == domain.ResourcePod {
		names[0] = t.ResourceName
	}
	for i, name := range names {
		w.pods = append(w.pods, &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: w.ns, CreationTimestamp: metav1.NewTime(Now.Add(-2 * time.Hour)),
				Labels: map[string]string{"app": app, "pod-template-hash": "7c9d8f6b5"}},
			Spec: corev1.PodSpec{NodeName: fmt.Sprintf("worker-%d", i+1), Containers: []corev1.Container{container}},
			Status: corev1.PodStatus{
				Phase: corev1.PodRunning, PodIP: fmt.Sprintf("10.1.0.%d", 4+i),
				Conditions: []corev1.PodCondition{
					{Type: corev1.PodScheduled, Status: corev1.ConditionTrue},
					{Type: corev1.ContainersReady, Status: corev1.ConditionTrue},
					{Type: corev1.PodReady, Status: corev1.ConditionTrue},
				},
				ContainerStatuses: []corev1.ContainerStatus{{
					Name: "app", Image: image, Ready: true, Started: ptr.To(true),
					State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(Now.Add(-2 * time.Hour))}},
				}},
			},
		})
	}
	w.service = &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: app, Namespace: w.ns},
		Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP, ClusterIP: "10.96.14.7", Selector: w.labels,
			Ports: []corev1.ServicePort{{Name: "http", Port: 80, TargetPort: intstr.FromString("http"), Protocol: corev1.ProtocolTCP}}},
	}
	w.configMap = &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: app + "-config", Namespace: w.ns},
		Data:       map[string]string{"PAYMENT_PROVIDER_URL": "https://provider.example.com", "DATABASE_HOST": "postgres." + w.ns + ".svc.cluster.local", "LOG_LEVEL": "info"},
	}
	w.policies = []*networkingv1.NetworkPolicy{{
		ObjectMeta: metav1.ObjectMeta{Name: "allow-ingress-controller", Namespace: w.ns},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: w.labels},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
			Ingress: []networkingv1.NetworkPolicyIngressRule{{From: []networkingv1.NetworkPolicyPeer{{
				NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"kubernetes.io/metadata.name": "ingress-nginx"}}}}}},
		},
	}}
	w.logs = "level=info msg=\"starting " + app + "\" version=1.4.2\n" +
		"level=info msg=\"connected to database\" host=postgres." + w.ns + ".svc.cluster.local\n" +
		"level=info msg=\"listening\" addr=:8080\n" +
		"level=info msg=\"request\" method=GET path=/ready status=200 duration_ms=2\n"
	w.event("Normal", "ScalingReplicaSet", "Deployment", app, "Scaled up replica set "+app+"-7c9d8f6b5 to 2", -2*time.Hour, 1)
	for _, p := range w.pods {
		w.event("Normal", "Scheduled", "Pod", p.Name, "Successfully assigned "+w.ns+"/"+p.Name+" to "+p.Spec.NodeName, -2*time.Hour, 1)
		w.event("Normal", "Started", "Pod", p.Name, "Started container app", -2*time.Hour+10*time.Second, 1)
	}
	return w
}

// ---- scenario mutations ----

func crashLoop(w *world) {
	for _, p := range w.pods {
		notReady(p)
		cs := &p.Status.ContainerStatuses[0]
		cs.RestartCount = 8
		cs.State = waiting("CrashLoopBackOff", "back-off 5m0s restarting failed container=app pod="+p.Name)
		cs.LastTerminationState = terminated("Error", 1, -3*time.Minute)
		w.event("Warning", "BackOff", "Pod", p.Name, "Back-off restarting failed container app in pod "+p.Name, -time.Minute, 34)
	}
	w.setReady(0)
	w.logs = ""
	w.previousLogs = "level=info msg=\"starting " + w.app + "\" version=1.4.3\n" +
		"level=error msg=\"FATAL: missing required configuration: PAYMENT_API_TOKEN environment variable is not set\"\n" +
		"level=info msg=\"shutting down\" exit_code=1\n"
}

func imagePullError(w *world) {
	image := "registry.example.com/" + w.app + ":1.4.3"
	for _, p := range w.pods {
		p.Status.Phase = corev1.PodPending
		notReady(p)
		cs := &p.Status.ContainerStatuses[0]
		cs.Image, cs.Started = image, ptr.To(false)
		cs.State = waiting("ImagePullBackOff", `Back-off pulling image "`+image+`"`)
		w.event("Warning", "Failed", "Pod", p.Name, `Failed to pull image "`+image+`": rpc error: code = NotFound desc = manifest unknown`, -4*time.Minute, 5)
		w.event("Warning", "BackOff", "Pod", p.Name, `Back-off pulling image "`+image+`"`, -time.Minute, 21)
	}
	w.deploy.Spec.Template.Spec.Containers[0].Image = image
	w.setReady(0)
	w.deploy.Status.Conditions[1] = appsv1.DeploymentCondition{Type: appsv1.DeploymentProgressing, Status: corev1.ConditionFalse,
		Reason: "ProgressDeadlineExceeded", Message: `ReplicaSet "` + w.app + `-5d8b7c9f4" has timed out progressing.`}
	w.logErr = errors.New(`container "app" in pod is waiting to start: trying and failing to pull image`)
}

func oomKilled(w *world) {
	for i, p := range w.pods {
		cs := &p.Status.ContainerStatuses[0]
		cs.RestartCount = 5
		cs.LastTerminationState = terminated("OOMKilled", 137, -4*time.Minute)
		if i == 1 {
			notReady(p)
			cs.State = waiting("CrashLoopBackOff", "back-off 2m40s restarting failed container=app pod="+p.Name)
			w.event("Warning", "BackOff", "Pod", p.Name, "Back-off restarting failed container app in pod "+p.Name, -2*time.Minute, 9)
		}
	}
	w.setReady(1)
	w.memoryBytes = 251 << 20
	w.previousLogs = w.logs + "level=warn msg=\"batch settlement started\" items=184000\n" +
		"fatal error: runtime: out of memory\n"
}

func readinessFailure(w *world) {
	for _, p := range w.pods {
		notReady(p)
		w.event("Warning", "Unhealthy", "Pod", p.Name, "Readiness probe failed: HTTP probe failed with statuscode: 503", -30*time.Second, 120)
	}
	w.setReady(0)
	w.logs = "level=info msg=\"starting " + w.app + "\" version=1.4.3\n" +
		"level=info msg=\"listening\" addr=:8080\n" +
		"level=warn msg=\"readiness check failed\" check=fraud-cache status=warming\n" +
		"level=info msg=\"request\" method=GET path=/ready status=503 duration_ms=1\n"
}

func livenessFailure(w *world) {
	for _, p := range w.pods {
		notReady(p)
		cs := &p.Status.ContainerStatuses[0]
		cs.RestartCount = 6
		cs.LastTerminationState = terminated("Error", 137, -2*time.Minute)
		w.event("Warning", "Unhealthy", "Pod", p.Name, `Liveness probe failed: Get "http://10.1.0.4:8080/healthz": context deadline exceeded (Client.Timeout exceeded while awaiting headers)`, -2*time.Minute, 18)
		w.event("Normal", "Killing", "Pod", p.Name, "Container app failed liveness probe, will be restarted", -2*time.Minute, 6)
	}
	w.setReady(0)
	w.previousLogs = w.logs + "level=warn msg=\"worker pool saturated\" busy=64 queued=1200\n" +
		"level=warn msg=\"health handler blocked waiting for worker\" waited_ms=4800\n"
}

func missingConfigMap(w *world) {
	for _, p := range w.pods {
		p.Status.Phase = corev1.PodPending
		notReady(p)
		msg := `configmap "` + w.configMap.Name + `" not found`
		p.Status.ContainerStatuses[0].State = waiting("CreateContainerConfigError", msg)
		w.event("Warning", "Failed", "Pod", p.Name, "Error: "+msg, -time.Minute, 14)
	}
	w.configMap = nil
	w.setReady(0)
	w.logErr = errors.New(`container "app" in pod is waiting to start: CreateContainerConfigError`)
}

func selectorMismatch(w *world) {
	w.service.Spec.Selector = map[string]string{"app": w.app + "-api"}
}

func dnsFailure(w *world) {
	for _, p := range w.pods {
		notReady(p)
		w.event("Warning", "Unhealthy", "Pod", p.Name, "Readiness probe failed: HTTP probe failed with statuscode: 503", -time.Minute, 40)
	}
	w.setReady(0)
	w.logs = "level=info msg=\"starting " + w.app + "\" version=1.4.2\n" +
		"level=error msg=\"database connection failed\" error=\"dial tcp: lookup postgres-primary." + w.ns + ".svc.cluster.local on 10.96.0.10:53: no such host\"\n" +
		"level=info msg=\"request\" method=GET path=/ready status=503 duration_ms=1\n"
}

func resourcePressure(w *world) {
	c := &w.deploy.Spec.Template.Spec.Containers[0]
	c.Resources.Requests[corev1.ResourceMemory] = resource.MustParse("6Gi")
	c.Resources.Limits[corev1.ResourceMemory] = resource.MustParse("6Gi")
	p := w.pods[1]
	p.Spec.NodeName = ""
	p.Spec.Containers = []corev1.Container{*c}
	p.Status = corev1.PodStatus{Phase: corev1.PodPending, Conditions: []corev1.PodCondition{{
		Type: corev1.PodScheduled, Status: corev1.ConditionFalse, Reason: corev1.PodReasonUnschedulable,
		Message: "0/3 nodes are available: 3 Insufficient memory. preemption: 0/3 nodes are available: 3 No preemption victims found for incoming pod.",
	}}}
	w.event("Warning", "FailedScheduling", "Pod", p.Name, p.Status.Conditions[0].Message, -time.Minute, 27)
	w.setReady(1)
}

func networkPolicyBlock(w *world) {
	w.policies = append(w.policies, &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "restrict-egress", Namespace: w.ns},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: w.labels},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeEgress},
			Egress: []networkingv1.NetworkPolicyEgressRule{{
				Ports: []networkingv1.NetworkPolicyPort{{Protocol: ptr.To(corev1.ProtocolUDP), Port: ptr.To(intstr.FromInt32(53))}},
			}},
		},
	})
	for _, p := range w.pods {
		notReady(p)
		w.event("Warning", "Unhealthy", "Pod", p.Name, "Readiness probe failed: HTTP probe failed with statuscode: 503", -time.Minute, 25)
	}
	w.setReady(0)
	w.logs = "level=info msg=\"starting " + w.app + "\" version=1.4.2\n" +
		"level=error msg=\"database connection failed\" error=\"dial tcp 10.96.22.31:5432: i/o timeout\"\n" +
		"level=info msg=\"request\" method=GET path=/ready status=503 duration_ms=1\n"
}

// ---- helpers ----

func httpProbe(path string, period int32) *corev1.Probe {
	return &corev1.Probe{
		ProbeHandler:  corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: path, Port: intstr.FromString("http")}},
		PeriodSeconds: period, TimeoutSeconds: 1, FailureThreshold: 3,
	}
}

func waiting(reason, msg string) corev1.ContainerState {
	return corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: reason, Message: msg}}
}

func terminated(reason string, code int32, ago time.Duration) corev1.ContainerState {
	return corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
		Reason: reason, ExitCode: code, StartedAt: metav1.NewTime(Now.Add(ago - 40*time.Second)), FinishedAt: metav1.NewTime(Now.Add(ago)),
	}}
}

func notReady(p *corev1.Pod) {
	for i := range p.Status.Conditions {
		if t := p.Status.Conditions[i].Type; t == corev1.PodReady || t == corev1.ContainersReady {
			p.Status.Conditions[i].Status = corev1.ConditionFalse
			p.Status.Conditions[i].Reason = "ContainersNotReady"
		}
	}
	p.Status.ContainerStatuses[0].Ready = false
}

func (w *world) setReady(n int32) {
	st := &w.deploy.Status
	st.ReadyReplicas, st.AvailableReplicas, st.UnavailableReplicas = n, n, st.Replicas-n
	if n < st.Replicas {
		st.Conditions[0] = appsv1.DeploymentCondition{Type: appsv1.DeploymentAvailable, Status: corev1.ConditionFalse, Reason: "MinimumReplicasUnavailable"}
	}
}

// endpointSlice mirrors the EndpointSlice controller: one endpoint per pod
// with an IP that matches the Service selector, ready when the pod is.
func (w *world) endpointSlice() *discoveryv1.EndpointSlice {
	slice := &discoveryv1.EndpointSlice{
		ObjectMeta:  metav1.ObjectMeta{Name: w.app + "-8xk2p", Namespace: w.ns, Labels: map[string]string{discoveryv1.LabelServiceName: w.app}},
		AddressType: discoveryv1.AddressTypeIPv4,
		Ports:       []discoveryv1.EndpointPort{{Name: ptr.To("http"), Port: ptr.To[int32](8080)}},
	}
	sel := labels.SelectorFromSet(w.service.Spec.Selector)
	for _, p := range w.pods {
		if p.Status.PodIP == "" || !sel.Matches(labels.Set(p.Labels)) {
			continue
		}
		slice.Endpoints = append(slice.Endpoints, discoveryv1.Endpoint{
			Addresses:  []string{p.Status.PodIP},
			Conditions: discoveryv1.EndpointConditions{Ready: ptr.To(p.Status.ContainerStatuses[0].Ready)},
			TargetRef:  &corev1.ObjectReference{Kind: "Pod", Name: p.Name, Namespace: w.ns},
		})
	}
	return slice
}

func (w *world) event(typ, reason, kind, name, msg string, ago time.Duration, count int32) {
	w.events = append(w.events, &corev1.Event{
		ObjectMeta:     metav1.ObjectMeta{Name: fmt.Sprintf("%s.%03d", name, len(w.events)), Namespace: w.ns},
		InvolvedObject: corev1.ObjectReference{Kind: kind, Name: name, Namespace: w.ns},
		Type:           typ, Reason: reason, Message: msg, Count: count,
		FirstTimestamp: metav1.NewTime(Now.Add(ago - time.Duration(count)*time.Second)),
		LastTimestamp:  metav1.NewTime(Now.Add(ago)),
	})
}

func (w *world) cluster() (kube.Cluster, error) {
	objs := []runtime.Object{
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: w.ns}, Status: corev1.NamespaceStatus{Phase: corev1.NamespaceActive}},
		w.deploy, w.service, w.endpointSlice(),
	}
	if w.configMap != nil {
		objs = append(objs, w.configMap)
	}
	for _, p := range w.pods {
		objs = append(objs, p)
	}
	for _, e := range w.events {
		objs = append(objs, e)
	}
	for _, np := range w.policies {
		objs = append(objs, np)
	}
	client := fake.NewClientset(objs...)
	client.PrependReactor("get", "pods/log", func(a k8stesting.Action) (bool, runtime.Object, error) {
		if w.logErr != nil {
			return true, nil, w.logErr
		}
		opts, _ := a.(k8stesting.GenericAction).GetValue().(*corev1.PodLogOptions)
		if opts != nil && opts.Previous {
			if w.previousLogs == "" {
				return true, nil, errors.New("previous terminated container \"app\" not found")
			}
			return true, &runtime.Unknown{Raw: []byte(w.previousLogs)}, nil
		}
		return true, &runtime.Unknown{Raw: []byte(w.logs)}, nil
	})

	scheme := metadatafake.NewTestScheme()
	if err := metav1.AddMetaToScheme(scheme); err != nil {
		return kube.Cluster{}, err
	}
	secret := &metav1.PartialObjectMetadata{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{Name: w.app + "-db", Namespace: w.ns, CreationTimestamp: metav1.NewTime(Now.Add(-720 * time.Hour))},
	}

	var usage []kube.PodUsage
	for _, p := range w.pods {
		if p.Status.Phase == corev1.PodRunning {
			usage = append(usage, kube.PodUsage{Pod: p.Name, Containers: []kube.ContainerUsage{{Name: "app", CPUMilli: w.cpuMilli, MemoryBytes: w.memoryBytes}}})
		}
	}
	return kube.Cluster{
		Client:   client,
		Metadata: metadatafake.NewSimpleMetadataClient(scheme, secret),
		PodMetrics: func(ctx context.Context, _ string) ([]kube.PodUsage, error) {
			return usage, ctx.Err()
		},
	}, nil
}
