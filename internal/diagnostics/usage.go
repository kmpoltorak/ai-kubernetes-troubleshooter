package diagnostics

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/domain"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/kube"
)

// Usage at or above this share of the limit is reported.
const highUsagePercent = 90

type ContainerUsage struct {
	Pod                string  `json:"pod"`
	Container          string  `json:"container"`
	CPUMilli           int64   `json:"cpu_millicores"`
	CPULimitMilli      int64   `json:"cpu_limit_millicores,omitempty"`
	CPUPercentOfLimit  float64 `json:"cpu_percent_of_limit,omitempty"`
	MemoryBytes        int64   `json:"memory_bytes"`
	MemoryLimitBytes   int64   `json:"memory_limit_bytes,omitempty"`
	MemoryPercentLimit float64 `json:"memory_percent_of_limit,omitempty"`
}

type UsageInfo struct {
	Containers []ContainerUsage `json:"containers"`
}

// getResourceUsage compares metrics.k8s.io usage of pods in.Names with
// their container limits.
func getResourceUsage(ctx context.Context, c kube.Cluster, in Input) (Result, error) {
	if c.PodMetrics == nil {
		return Result{}, kube.ErrMetricsUnavailable
	}
	if len(in.Names) == 0 {
		return Result{}, errors.New("names (pods) is required")
	}
	usage, err := c.PodMetrics(ctx, in.Namespace)
	if err != nil {
		return Result{}, err
	}
	var info UsageInfo
	var signals signalSet
	for _, pu := range usage {
		if !slices.Contains(in.Names, pu.Pod) {
			continue
		}
		pod, err := c.Client.CoreV1().Pods(in.Namespace).Get(ctx, pu.Pod, metav1.GetOptions{})
		if err != nil {
			return Result{}, err
		}
		for _, cu := range pu.Containers {
			u := ContainerUsage{Pod: pu.Pod, Container: cu.Name, CPUMilli: cu.CPUMilli, MemoryBytes: cu.MemoryBytes}
			for _, ctr := range pod.Spec.Containers {
				if ctr.Name != cu.Name {
					continue
				}
				if l, ok := ctr.Resources.Limits["cpu"]; ok && l.MilliValue() > 0 {
					u.CPULimitMilli = l.MilliValue()
					u.CPUPercentOfLimit = percent(cu.CPUMilli, u.CPULimitMilli)
				}
				if l, ok := ctr.Resources.Limits["memory"]; ok && l.Value() > 0 {
					u.MemoryLimitBytes = l.Value()
					u.MemoryPercentLimit = percent(cu.MemoryBytes, u.MemoryLimitBytes)
				}
			}
			if u.MemoryPercentLimit >= highUsagePercent {
				signals.add(SignalHighMemoryUsage)
			}
			if u.CPUPercentOfLimit >= highUsagePercent {
				signals.add(SignalHighCPUUsage)
			}
			info.Containers = append(info.Containers, u)
		}
	}
	health := domain.Healthy
	if len(signals) > 0 {
		health = domain.Degraded
	}
	summary := fmt.Sprintf("Usage collected for %d containers.", len(info.Containers))
	if len(signals) > 0 {
		summary += " Findings: " + strings.Join(signals, ", ") + "."
	}
	return Result{Subject: "pods/" + in.Namespace, Health: health, Summary: summary, Signals: signals, Data: info}, nil
}

func percent(v, limit int64) float64 {
	return float64(int64(float64(v)/float64(limit)*1000)) / 10 // one decimal
}
