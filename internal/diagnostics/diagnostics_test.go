package diagnostics

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes/fake"
	metadatafake "k8s.io/client-go/metadata/fake"
	k8stesting "k8s.io/client-go/testing"
	"k8s.io/utils/ptr"

	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/domain"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/kube"
)

const ns = "payments"

var appLabels = map[string]string{"app": "payment-service"}

func cluster(t *testing.T, objs ...runtime.Object) kube.Cluster {
	t.Helper()
	scheme := metadatafake.NewTestScheme()
	if err := metav1.AddMetaToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	secret := &metav1.PartialObjectMetadata{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{Name: "db-credentials", Namespace: ns, Annotations: map[string]string{"k": "v"}},
	}
	return kube.Cluster{Client: fake.NewClientset(objs...), Metadata: metadatafake.NewSimpleMetadataClient(scheme, secret)}
}

func run(t *testing.T, c kube.Cluster, tool string, in Input) Result {
	t.Helper()
	in.Namespace = ns
	res, err := Run(context.Background(), tool, c, in)
	if err != nil {
		t.Fatalf("%s: %v", tool, err)
	}
	// Every result must be JSON-serializable evidence.
	if _, err := json.Marshal(res.Data); err != nil {
		t.Fatalf("%s: data not serializable: %v", tool, err)
	}
	return res
}

func wantSignals(t *testing.T, r Result, want ...string) {
	t.Helper()
	for _, s := range want {
		if !slices.Contains(r.Signals, s) {
			t.Errorf("signals %v missing %s (summary: %s)", r.Signals, s, r.Summary)
		}
	}
}

func pod(name string, mutate func(*corev1.Pod)) *corev1.Pod {
	p := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: map[string]string{"app": "payment-service", "pod-template-hash": "7c9d8f6b5"}},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{
			Name: "app", Image: "payment-service:1.4.2",
			Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{
				corev1.ResourceMemory: resource.MustParse("256Mi"), corev1.ResourceCPU: resource.MustParse("500m"),
			}},
		}}},
		Status: corev1.PodStatus{
			Phase:      corev1.PodRunning,
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}},
			ContainerStatuses: []corev1.ContainerStatus{{
				Name: "app", Image: "payment-service:1.4.2", Ready: true,
				State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
			}},
		},
	}
	if mutate != nil {
		mutate(p)
	}
	return p
}

func TestRunRejectsUnknownToolAndBadInput(t *testing.T) {
	c := cluster(t)
	if _, err := Run(context.Background(), "exec_in_pod", c, Input{Namespace: ns}); err == nil {
		t.Fatal("unknown tool accepted")
	}
	for _, in := range []Input{
		{Namespace: "Bad NS"},
		{Namespace: ns, Name: "../etc"},
		{Namespace: ns, Names: []string{"ok", "a;b"}},
	} {
		if _, err := Run(context.Background(), GetPods, c, in); err == nil || !strings.Contains(err.Error(), "invalid input") {
			t.Errorf("input %+v: want invalid input error, got %v", in, err)
		}
	}
}

func TestGetNamespace(t *testing.T) {
	c := cluster(t, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}, Status: corev1.NamespaceStatus{Phase: corev1.NamespaceActive}})
	if r := run(t, c, GetNamespace, Input{}); r.Health != domain.Healthy {
		t.Fatalf("existing namespace: %+v", r)
	}
	r, err := Run(context.Background(), GetNamespace, c, Input{Namespace: "orders"})
	if err != nil || r.Health != domain.Down {
		t.Fatalf("missing namespace: %+v %v", r, err)
	}
	wantSignals(t, r, SignalNamespaceNotFound)
}

func TestGetDeployment(t *testing.T) {
	d := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "payment-service", Namespace: ns, Generation: 2},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To[int32](3),
			Selector: &metav1.LabelSelector{MatchLabels: appLabels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: appLabels},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name: "app", Image: "payment-service:1.4.2",
						Env: []corev1.EnvVar{
							{Name: "LOG_LEVEL", Value: "debug"},
							{Name: "DB_PASSWORD", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
								LocalObjectReference: corev1.LocalObjectReference{Name: "db-credentials"}, Key: "password"}}},
						},
						EnvFrom:        []corev1.EnvFromSource{{ConfigMapRef: &corev1.ConfigMapEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "payment-config"}}}},
						ReadinessProbe: &corev1.Probe{PeriodSeconds: 5, FailureThreshold: 3},
					}},
					Volumes: []corev1.Volume{{Name: "opt", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
						LocalObjectReference: corev1.LocalObjectReference{Name: "optional-cm"}, Optional: ptr.To(true)}}}},
				},
			},
		},
		Status: appsv1.DeploymentStatus{ObservedGeneration: 2, Replicas: 3, UpdatedReplicas: 3, ReadyReplicas: 0,
			Conditions: []appsv1.DeploymentCondition{{Type: appsv1.DeploymentProgressing, Status: corev1.ConditionFalse, Reason: "ProgressDeadlineExceeded"}}},
	}
	r := run(t, cluster(t, d), GetDeployment, Input{Name: "payment-service"})
	if r.Health != domain.Down {
		t.Fatalf("health = %s", r.Health)
	}
	wantSignals(t, r, SignalReplicasUnavailable, SignalRolloutStuck)
	info := r.Data.(DeploymentInfo)
	if info.Selector != "app=payment-service" || !slices.Equal(info.Template.ConfigMaps, []string{"payment-config"}) ||
		!slices.Equal(info.Template.Secrets, []string{"db-credentials"}) {
		t.Fatalf("info: %+v", info)
	}
	raw, _ := json.Marshal(info)
	if strings.Contains(string(raw), "debug") || !strings.Contains(string(raw), `"source":"secret:db-credentials"`) {
		t.Fatalf("env values must not be reported, sources must: %s", raw)
	}

	missing := run(t, cluster(t), GetDeployment, Input{Name: "payment-service"})
	wantSignals(t, missing, SignalWorkloadNotFound)
}

func TestGetPods(t *testing.T) {
	crashing := pod("payment-service-7c9d8f6b5-a", func(p *corev1.Pod) {
		p.Status.Conditions[0].Status = corev1.ConditionFalse
		cs := &p.Status.ContainerStatuses[0]
		cs.Ready, cs.RestartCount = false, 8
		cs.State = corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}
		cs.LastTerminationState = corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "Error", ExitCode: 1}}
	})
	oom := pod("payment-service-7c9d8f6b5-b", func(p *corev1.Pod) {
		cs := &p.Status.ContainerStatuses[0]
		cs.RestartCount = 2
		cs.LastTerminationState = corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "OOMKilled", ExitCode: 137}}
	})
	other := pod("orders-1", func(p *corev1.Pod) { p.Labels = map[string]string{"app": "orders"} })
	c := cluster(t, crashing, oom, other)

	r := run(t, c, GetPods, Input{LabelSelector: "app=payment-service"})
	info := r.Data.(PodsInfo)
	if info.Total != 2 || info.Ready != 1 || r.Health != domain.Degraded {
		t.Fatalf("total=%d ready=%d health=%s", info.Total, info.Ready, r.Health)
	}
	wantSignals(t, r, SignalCrashLoopBackOff, SignalContainerExitError, SignalFrequentRestarts, SignalOOMKilled)
	if lim := info.Pods[1].Containers[0].Limits["memory"]; lim != "256Mi" {
		t.Fatalf("memory limit = %q", lim)
	}

	byName := run(t, c, GetPods, Input{Name: "payment-service-7c9d8f6b5-a"})
	if byName.Data.(PodsInfo).Spec == nil || byName.Health != domain.Down {
		t.Fatalf("pod by name: %+v", byName)
	}

	none := run(t, c, GetPods, Input{LabelSelector: "app=nothing"})
	wantSignals(t, none, SignalNoPods)
	missing := run(t, c, GetPods, Input{Name: "gone"})
	wantSignals(t, missing, SignalWorkloadNotFound)

	pending := pod("payment-service-7c9d8f6b5-c", func(p *corev1.Pod) {
		p.Status.Phase = corev1.PodPending
		p.Status.ContainerStatuses = nil
		p.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodScheduled, Status: corev1.ConditionFalse,
			Reason: corev1.PodReasonUnschedulable, Message: "0/3 nodes are available: 3 Insufficient memory."}}
	})
	unsched := run(t, cluster(t, pending), GetPods, Input{LabelSelector: "app=payment-service"})
	wantSignals(t, unsched, SignalUnschedulable)
}

func TestGetEvents(t *testing.T) {
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	ev := func(name, obj, reason, typ, msg string, at time.Duration) *corev1.Event {
		kind := "Pod"
		if strings.HasPrefix(obj, "rs:") {
			kind, obj = "ReplicaSet", obj[3:]
		}
		return &corev1.Event{
			ObjectMeta:     metav1.ObjectMeta{Name: name, Namespace: ns},
			InvolvedObject: corev1.ObjectReference{Kind: kind, Name: obj},
			Reason:         reason, Type: typ, Message: msg, Count: 3,
			LastTimestamp: metav1.NewTime(base.Add(at)),
		}
	}
	c := cluster(t,
		ev("e2", "payment-service-7c9d8f6b5-a", "Unhealthy", "Warning", "Readiness probe failed: HTTP probe failed with statuscode: 503", 2*time.Minute),
		ev("e1", "payment-service-7c9d8f6b5-a", "BackOff", "Warning", "Back-off restarting failed container app", time.Minute),
		ev("e3", "orders-1", "FailedScheduling", "Warning", "unrelated", 0),
		ev("e4", "payment-service-7c9d8f6b5-a", "Pulled", "Normal", "token=abc123secret pulled", 0),
		ev("e5", "rs:payment-service-7c9d8f6b5", "SuccessfulCreate", "Normal", "Created pod", -time.Minute),
		// A different Deployment whose name extends the target's.
		ev("e6", "payment-service-worker-6b4f5c7d8-xyz", "FailedScheduling", "Warning", "unrelated", 0),
		ev("e7", "rs:payment-service-worker", "FailedCreate", "Warning", "unrelated", 0),
	)
	r := run(t, c, GetEvents, Input{Names: []string{"payment-service", "payment-service-7c9d8f6b5-a"}})
	info := r.Data.(EventsInfo)
	if info.Total != 4 || info.Warnings != 2 {
		t.Fatalf("total=%d warnings=%d", info.Total, info.Warnings)
	}
	if info.Events[0].Reason != "SuccessfulCreate" || info.Events[1].Reason != "Pulled" || info.Events[3].Reason != "Unhealthy" {
		t.Fatalf("not chronological: %+v", info.Events)
	}
	if strings.Contains(info.Events[1].Message, "abc123secret") {
		t.Fatalf("event message not redacted: %s", info.Events[1].Message)
	}
	wantSignals(t, r, SignalBackOff, SignalReadinessProbeFailed)
	if slices.Contains(r.Signals, SignalFailedScheduling) {
		t.Fatal("unrelated event included")
	}
}

func TestGetPodLogs(t *testing.T) {
	c := cluster(t, pod("payment-service-7c9d8f6b5-a", nil))
	fakeClient := c.Client.(*fake.Clientset)
	fakeClient.PrependReactor("get", "pods/log", func(a k8stesting.Action) (bool, runtime.Object, error) {
		opts := a.(k8stesting.GenericAction).GetValue().(*corev1.PodLogOptions)
		if opts.TailLines == nil || opts.LimitBytes == nil {
			t.Error("log request must set tail and byte limits")
		}
		logs := "starting payment-service\nconnecting to postgres://app:hunter2@db:5432/payments\n"
		if opts.Previous {
			logs += "error: dial tcp: lookup db.payments.svc.cluster.local on 10.96.0.10:53: no such host\n"
		}
		return true, &runtime.Unknown{Raw: []byte(logs)}, nil
	})
	r := run(t, c, GetPodLogs, Input{Name: "payment-service-7c9d8f6b5-a", Names: []string{"app"}})
	info := r.Data.(LogsInfo)
	if len(info.Streams) != 2 || !info.Streams[1].Previous || len(info.Streams[1].ErrorLines) != 1 {
		t.Fatalf("streams: %+v", info.Streams)
	}
	raw, _ := json.Marshal(info)
	if strings.Contains(string(raw), "hunter2") {
		t.Fatalf("credentials leaked: %s", raw)
	}
	wantSignals(t, r, SignalDNSResolutionError)
}

func TestParseLogsBounds(t *testing.T) {
	var b strings.Builder
	for range 100 {
		b.WriteString(strings.Repeat("x", 1000) + "\n")
	}
	s, err := parseLogs(strings.NewReader(b.String()), "app", false)
	if err != nil {
		t.Fatal(err)
	}
	if !s.Truncated || len(s.Lines) != logKeepLines || len(s.Lines[0]) > logMaxLineBytes+len("…") {
		t.Fatalf("truncated=%t lines=%d len=%d", s.Truncated, len(s.Lines), len(s.Lines[0]))
	}
}

func TestLogPatterns(t *testing.T) {
	cases := map[string]string{
		"dial tcp: lookup redis on 10.96.0.10:53: server misbehaving":            SignalDNSResolutionError,
		"dial tcp 10.0.0.12:5432: connect: connection refused":                   SignalConnectionRefused,
		"dial tcp 10.0.0.12:5432: i/o timeout":                                   SignalConnectionTimeout,
		"FATAL: missing required configuration: DATABASE_URL":                    SignalConfigurationError,
		"java.lang.OutOfMemoryError: Java heap space":                            SignalOutOfMemory,
		"environment variable PAYMENT_API_URL is not set, invalid configuration": SignalConfigurationError,
	}
	for line, want := range cases {
		matched := false
		for _, p := range logPatterns {
			if p.signal == want && p.re.MatchString(line) {
				matched = true
			}
		}
		if !matched {
			t.Errorf("%q: want %s", line, want)
		}
	}
}

func service(selector map[string]string) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "payment-service", Namespace: ns},
		Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP, Selector: selector,
			Ports: []corev1.ServicePort{{Name: "http", Port: 80, Protocol: corev1.ProtocolTCP}}},
	}
}

func TestGetService(t *testing.T) {
	slice := &discoveryv1.EndpointSlice{
		ObjectMeta:  metav1.ObjectMeta{Name: "payment-service-abc", Namespace: ns, Labels: map[string]string{discoveryv1.LabelServiceName: "payment-service"}},
		AddressType: discoveryv1.AddressTypeIPv4,
		Endpoints: []discoveryv1.Endpoint{
			{Addresses: []string{"10.1.0.4"}, Conditions: discoveryv1.EndpointConditions{Ready: ptr.To(true)}},
			{Addresses: []string{"10.1.0.5"}, Conditions: discoveryv1.EndpointConditions{Ready: ptr.To(false)}},
		},
	}
	healthy := run(t, cluster(t, service(appLabels), slice, pod("payment-service-7c9d8f6b5-a", nil)), GetService,
		Input{Name: "payment-service", Labels: appLabels})
	si := healthy.Data.(ServicesInfo).Services
	if healthy.Health != domain.Healthy || len(si) != 1 || si[0].ReadyEndpoints != 1 || si[0].NotReadyEndpoints != 1 || si[0].MatchingPods != 1 {
		t.Fatalf("healthy service: %+v", healthy)
	}

	mismatch := run(t, cluster(t, service(map[string]string{"app": "payments"}), pod("payment-service-7c9d8f6b5-a", nil)),
		GetService, Input{Name: "payment-service"})
	if mismatch.Health != domain.Down {
		t.Fatalf("health = %s", mismatch.Health)
	}
	wantSignals(t, mismatch, SignalSelectorMismatch, SignalNoReadyEndpoints)
	sample := mismatch.Data.(ServicesInfo).NamespacePodLabels
	if len(sample) != 1 || sample[0]["app"] != "payment-service" || sample[0]["pod-template-hash"] != "" {
		t.Fatalf("pod label sample: %v", sample)
	}

	missing := run(t, cluster(t), GetService, Input{Name: "payment-service"})
	wantSignals(t, missing, SignalServiceNotFound)

	none := run(t, cluster(t), GetService, Input{Name: "payment-service", Labels: appLabels})
	if none.Health != domain.Healthy || len(none.Signals) != 0 {
		t.Fatalf("workload without service: %+v", none)
	}
}

func TestGetConfigMapAndSecretMetadata(t *testing.T) {
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "payment-config", Namespace: ns},
		Data: map[string]string{"app.yaml": "db: postgres", "LOG_LEVEL": "info"}}
	c := cluster(t, cm)

	r := run(t, c, GetConfigMap, Input{Name: "payment-config"})
	info := r.Data.(ConfigMapInfo)
	raw, _ := json.Marshal(info)
	if !info.Exists || len(info.Keys) != 2 || strings.Contains(string(raw), "postgres") {
		t.Fatalf("configmap: %s", raw)
	}
	wantSignals(t, run(t, c, GetConfigMap, Input{Name: "missing-config"}), SignalConfigMapMissing)

	sec := run(t, c, GetSecretMetadata, Input{Name: "db-credentials"})
	raw, _ = json.Marshal(sec.Data)
	if !sec.Data.(SecretMetadata).Exists || strings.Contains(string(raw), "annotations") {
		t.Fatalf("secret metadata: %s", raw)
	}
	wantSignals(t, run(t, c, GetSecretMetadata, Input{Name: "missing-secret"}), SignalSecretMissing)
}

func TestGetNetworkPolicies(t *testing.T) {
	dns := intstrPort(53)
	restrict := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "payment-egress", Namespace: ns},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: appLabels},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeEgress},
			Egress:      []networkingv1.NetworkPolicyEgressRule{{Ports: []networkingv1.NetworkPolicyPort{{Port: &dns}}}},
		},
	}
	otherApp := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "orders-deny", Namespace: ns},
		Spec: networkingv1.NetworkPolicySpec{PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"app": "orders"}},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeEgress}},
	}
	denyIngress := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "default-deny", Namespace: ns},
		Spec:       networkingv1.NetworkPolicySpec{},
	}
	r := run(t, cluster(t, restrict, otherApp, denyIngress), GetNetworkPolicies, Input{Labels: appLabels})
	if n := len(r.Data.(NetworkPoliciesInfo).Policies); n != 2 {
		t.Fatalf("policies = %d, want 2", n)
	}
	wantSignals(t, r, SignalEgressRestricted, SignalIngressDenyAll)
	if slices.Contains(r.Signals, SignalEgressDenyAll) {
		t.Fatal("policy for another app must not apply")
	}
}

func TestGetResourceUsage(t *testing.T) {
	c := cluster(t, pod("payment-service-7c9d8f6b5-a", nil))
	c.PodMetrics = func(context.Context, string) ([]kube.PodUsage, error) {
		return []kube.PodUsage{
			{Pod: "payment-service-7c9d8f6b5-a", Containers: []kube.ContainerUsage{{Name: "app", CPUMilli: 120, MemoryBytes: 250 << 20}}},
			{Pod: "orders-1", Containers: []kube.ContainerUsage{{Name: "app", MemoryBytes: 1}}},
		}, nil
	}
	r := run(t, c, GetResourceUsage, Input{Names: []string{"payment-service-7c9d8f6b5-a"}})
	u := r.Data.(UsageInfo).Containers
	if len(u) != 1 || u[0].MemoryPercentLimit != 97.6 || u[0].CPUPercentOfLimit != 24 {
		t.Fatalf("usage: %+v", u)
	}
	wantSignals(t, r, SignalHighMemoryUsage)

	c.PodMetrics = nil
	if _, err := Run(context.Background(), GetResourceUsage, c, Input{Namespace: ns, Names: []string{"x"}}); err == nil {
		t.Fatal("want error without metrics API")
	}
}

func intstrPort(p int) intstr.IntOrString { return intstr.FromInt(p) }

// A PEM block spans lines; redaction must not run line by line.
func TestParseLogsRedactsMultilinePrivateKey(t *testing.T) {
	for name, logs := range map[string]string{
		"complete":             "start\n-----BEGIN RSA PRIVATE KEY-----\nMIIEpAIBAAKCAQEASYNTHETICDATA\n-----END RSA PRIVATE KEY-----\nready\n",
		"crlf":                 "-----BEGIN PRIVATE KEY-----\r\nMIIEpAIBAAKCAQEASYNTHETICDATA\r\n-----END PRIVATE KEY-----\r\n",
		"no end marker":        "-----BEGIN EC PRIVATE KEY-----\nMIIEpAIBAAKCAQEASYNTHETICDATA\n",
		"sample starts inside": "MIIEpAIBAAKCAQEASYNTHETICDATA\n-----END RSA PRIVATE KEY-----\nready\n",
	} {
		s, err := parseLogs(strings.NewReader(logs), "app", false)
		if err != nil {
			t.Fatal(err)
		}
		if joined := strings.Join(s.Lines, "\n"); strings.Contains(joined, "SYNTHETICDATA") || !strings.Contains(joined, "[REDACTED PRIVATE KEY]") {
			t.Errorf("%s: key material kept: %q", name, s.Lines)
		}
	}
}

// Signals come from every line read, not only the kept tail.
func TestGetPodLogsSignalsBeforeTail(t *testing.T) {
	c := cluster(t, pod("payment-service-7c9d8f6b5-a", nil))
	c.Client.(*fake.Clientset).PrependReactor("get", "pods/log", func(k8stesting.Action) (bool, runtime.Object, error) {
		logs := "error: lookup db: no such host\n-----BEGIN RSA PRIVATE KEY-----\nMIIEpAIBAAKCAQEASYNTHETICDATA\n-----END RSA PRIVATE KEY-----\n" +
			strings.Repeat("info: heartbeat\n", logKeepLines+5)
		return true, &runtime.Unknown{Raw: []byte(logs)}, nil
	})
	r := run(t, c, GetPodLogs, Input{Name: "payment-service-7c9d8f6b5-a", Names: []string{"app"}})
	wantSignals(t, r, SignalDNSResolutionError)
	if raw, _ := json.Marshal(r.Data); strings.Contains(string(raw), "SYNTHETICDATA") {
		t.Fatalf("key material leaked: %s", raw)
	}
}

func TestGetPodsInitContainers(t *testing.T) {
	for reason, sig := range map[string]string{"CrashLoopBackOff": SignalCrashLoopBackOff, "ImagePullBackOff": SignalImagePullError, "OOMKilled": SignalOOMKilled} {
		p := pod("payment-service-7c9d8f6b5-a", func(p *corev1.Pod) {
			p.Status.Phase = corev1.PodPending
			p.Status.Conditions[0].Status = corev1.ConditionFalse
			p.Status.ContainerStatuses = nil
			st := corev1.ContainerStatus{Name: "migrate", RestartCount: 5}
			if reason == "OOMKilled" {
				st.State.Terminated = &corev1.ContainerStateTerminated{Reason: reason, ExitCode: 137}
			} else {
				st.State.Waiting = &corev1.ContainerStateWaiting{Reason: reason}
			}
			p.Status.InitContainerStatuses = []corev1.ContainerStatus{st}
		})
		r := run(t, cluster(t, p), GetPods, Input{LabelSelector: "app=payment-service"})
		wantSignals(t, r, sig, SignalFrequentRestarts)
		ctrs := r.Data.(PodsInfo).Pods[0].Containers
		if len(ctrs) != 1 || ctrs[0].Name != "migrate" || !ctrs[0].Init {
			t.Errorf("%s: containers %+v", reason, ctrs)
		}
	}
}

// The detail cap keeps pods with problems even when they sort last.
func TestGetPodsKeepsProblemPods(t *testing.T) {
	var objs []runtime.Object
	for i := range maxPods + 1 {
		objs = append(objs, pod(fmt.Sprintf("demo-%02d", i), nil))
	}
	objs[maxPods] = pod(fmt.Sprintf("demo-%02d", maxPods), func(p *corev1.Pod) {
		p.Status.Conditions[0].Status = corev1.ConditionFalse
		p.Status.ContainerStatuses[0].State = corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}
	})
	r := run(t, cluster(t, objs...), GetPods, Input{LabelSelector: "app=payment-service"})
	info := r.Data.(PodsInfo)
	if info.Total != maxPods+1 || len(info.Pods) != maxPods || info.Pods[0].Name != "demo-20" {
		t.Fatalf("total=%d kept=%d first=%s", info.Total, len(info.Pods), info.Pods[0].Name)
	}
	if !strings.Contains(r.Summary, "Details for 20 of 21 pods") {
		t.Errorf("summary does not report the cap: %s", r.Summary)
	}
}
