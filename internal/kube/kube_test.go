package kube

import (
	"testing"

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
