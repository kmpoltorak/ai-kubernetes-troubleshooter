package kube

import (
	"context"
	"slices"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

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

func TestLiveRejectsScenario(t *testing.T) {
	if _, _, err := (Live{}).For(domain.Target{}, "oom_killed"); err == nil {
		t.Fatal("scenario accepted in live mode")
	}
	if _, s, err := (Live{}).For(domain.Target{}, ""); err != nil || s != "" {
		t.Fatalf("live: %q %v", s, err)
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
	)}
	want := map[domain.ResourceType][]string{
		domain.ResourceDeployment:  {"checkout", "ledger"},
		domain.ResourceService:     {"checkout-svc"},
		domain.ResourcePod:         {"ledger-abc"},
		domain.ResourceStatefulSet: {"db"},
		domain.ResourceDaemonSet:   {"agent"},
		domain.ResourceJob:         {"migrate"},
		domain.ResourceCronJob:     {"nightly"},
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
