package kube

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"

	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/domain"
)

func TestParsePodMetrics(t *testing.T) {
	raw := []byte(`{"kind":"PodMetricsList","items":[{"metadata":{"name":"payment-service-a"},
		"containers":[{"name":"app","usage":{"cpu":"125m","memory":"250Mi"}}]}]}`)
	got, err := parsePodMetrics(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Pod != "payment-service-a" || got[0].Containers[0].CPUMilli != 125 ||
		got[0].Containers[0].MemoryBytes != 250<<20 {
		t.Fatalf("got %+v", got)
	}
	if _, err := parsePodMetrics([]byte(`not json`)); err == nil {
		t.Fatal("want decode error")
	}
}

func TestLiveRoutesByCluster(t *testing.T) {
	lab, prod := Cluster{Client: fake.NewClientset()}, Cluster{Client: fake.NewClientset()}
	live := Live{"lab": lab, "prod": prod}
	if _, _, err := live.For(domain.Target{Cluster: "lab"}, "oom_killed"); err == nil {
		t.Fatal("scenario accepted in live mode")
	}
	for name, want := range map[string]Cluster{"lab": lab, "prod": prod} {
		got, s, err := live.For(domain.Target{Cluster: name}, "")
		if err != nil || s != "" || got.Client != want.Client {
			t.Fatalf("%s: routed to the wrong cluster (%v)", name, err)
		}
	}
	if _, _, err := live.For(domain.Target{Cluster: "staging"}, ""); err == nil {
		t.Fatal("unknown cluster accepted")
	}
	if _, err := live.ListNames(context.Background(), "staging", "payments", domain.ResourceDeployment); err == nil {
		t.Fatal("listing in an unknown cluster")
	}
}

func TestLiveReadyWhileAnyClusterAnswers(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"gitVersion":"v1.37.0"}`))
	}))
	t.Cleanup(up.Close)
	down := httptest.NewServer(http.NotFoundHandler())
	down.Close() // connection refused
	connect := func(url string) Cluster {
		c, err := kubernetes.NewForConfig(&rest.Config{Host: url})
		if err != nil {
			t.Fatal(err)
		}
		return Cluster{Client: c}
	}
	ctx := context.Background()
	live := Live{"lab": connect(up.URL), "prod": connect(down.URL)}
	if pings := live.Ping(ctx); len(pings) != 2 || pings["lab"] != nil || pings["prod"] == nil {
		t.Fatalf("pings: %v", pings)
	}
	if err := live.Ready(ctx); err != nil {
		t.Fatalf("one cluster down made the installation unready: %v", err)
	}
	if err := (Live{"prod": connect(down.URL)}).Ready(ctx); err == nil || !strings.Contains(err.Error(), "cluster prod") {
		t.Fatalf("all clusters down: %v", err)
	}
	// A cluster that never answers must not hold readiness until the deadline.
	release := make(chan struct{})
	hang := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(hang.Close)
	t.Cleanup(func() { close(release) })
	deadline, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	start := time.Now()
	if err := (Live{"lab": connect(up.URL), "edge": connect(hang.URL)}).Ready(deadline); err != nil || time.Since(start) > time.Second {
		t.Fatalf("hanging cluster delayed readiness: %v after %s", err, time.Since(start))
	}
}

func TestListNamesPerKindAndNamespace(t *testing.T) {
	meta := func(ns, name string) metav1.ObjectMeta { return metav1.ObjectMeta{Namespace: ns, Name: name} }
	c := Cluster{Client: fake.NewClientset(
		&appsv1.Deployment{ObjectMeta: meta("payments", "ledger")}, &appsv1.Deployment{ObjectMeta: meta("payments", "checkout")},
		&appsv1.Deployment{ObjectMeta: meta("other", "hidden")},
		&corev1.Service{ObjectMeta: meta("payments", "checkout-svc")}, &corev1.Pod{ObjectMeta: meta("payments", "ledger-abc")},
		&appsv1.StatefulSet{ObjectMeta: meta("payments", "db")}, &appsv1.DaemonSet{ObjectMeta: meta("payments", "agent")},
		&batchv1.Job{ObjectMeta: meta("payments", "migrate")}, &batchv1.CronJob{ObjectMeta: meta("payments", "nightly")},
		&networkingv1.Ingress{ObjectMeta: meta("payments", "web")}, &corev1.PersistentVolumeClaim{ObjectMeta: meta("payments", "data")},
		&autoscalingv2.HorizontalPodAutoscaler{ObjectMeta: meta("payments", "api")},
	)}
	want := map[domain.ResourceType][]string{
		domain.ResourceDeployment:  {"checkout", "ledger"},
		domain.ResourceService:     {"checkout-svc"},
		domain.ResourcePod:         {"ledger-abc"},
		domain.ResourceStatefulSet: {"db"},
		domain.ResourceDaemonSet:   {"agent"},
		domain.ResourceJob:         {"migrate"},
		domain.ResourceCronJob:     {"nightly"},
		domain.ResourceIngress:     {"web"},
		domain.ResourcePVC:         {"data"},
		domain.ResourceHPA:         {"api"},
	}
	for _, kind := range domain.ResourceTypes {
		got, err := c.ListNames(context.Background(), "payments", kind)
		if err != nil || !slices.Equal(got, want[kind]) {
			t.Errorf("%s: %v %v, want %v", kind, got, err, want[kind])
		}
	}
	if _, err := c.ListNames(context.Background(), "payments", "secret"); err == nil {
		t.Fatal("an unsupported kind must not be listed")
	}
}

func TestListNamesOfConfiguredCustomResource(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "rollouts"}
	obj := func(name string) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{"apiVersion": "argoproj.io/v1alpha1", "kind": "Rollout", "metadata": map[string]any{"name": name, "namespace": "payments"}}}
	}
	c := Cluster{Dynamic: dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "RolloutList"}, obj("web"), obj("api")),
		CustomResources: CustomResourceMap([]domain.CustomResource{{Group: "argoproj.io", Version: "v1alpha1", Resource: "rollouts"}})}
	if got, err := c.ListNames(context.Background(), "payments", "rollouts.argoproj.io"); err != nil || !slices.Equal(got, []string{"api", "web"}) {
		t.Fatalf("custom names: %v %v", got, err)
	}
	if _, err := c.ListNames(context.Background(), "payments", "widgets.example.com"); err == nil {
		t.Fatal("an unconfigured custom resource was listed")
	}
}

func TestConnectUsesTheNamedKubeconfigAndContext(t *testing.T) {
	path := t.TempDir() + "/kubeconfig"
	kubeconfig := `apiVersion: v1
kind: Config
clusters:
  - {name: a, cluster: {server: "https://192.0.2.1:6443"}}
  - {name: b, cluster: {server: "https://192.0.2.2:6443"}}
users: [{name: u, user: {token: example}}]
contexts:
  - {name: a, context: {cluster: a, user: u}}
  - {name: b, context: {cluster: b, user: u}}
current-context: a
`
	if err := os.WriteFile(path, []byte(kubeconfig), 0o600); err != nil {
		t.Fatal(err)
	}
	for ctx, want := range map[string]string{"": "192.0.2.1", "b": "192.0.2.2"} {
		c, err := Connect(path, ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if host := c.Client.Discovery().RESTClient().Get().URL().Host; host != want+":6443" {
			t.Fatalf("context %q: connected to %s, want %s", ctx, host, want)
		}
	}
	if _, err := Connect(path, "missing", nil); err == nil {
		t.Fatal("unknown context accepted")
	}
}
