// Package diagnostics contains the allowlisted, read-only Kubernetes
// diagnostic tools. Each tool takes a validated Input, calls only Get, List
// or GetLogs, and returns normalized evidence: a health verdict, a one-line
// summary, stable signals and bounded, redacted data.
package diagnostics

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/domain"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/kube"
)

// Tool names form the complete allowlist.
const (
	GetNamespace       = "get_namespace"
	GetDeployment      = "get_deployment"
	GetPods            = "get_pods"
	GetEvents          = "get_events"
	GetPodLogs         = "get_pod_logs"
	GetService         = "get_service"
	GetConfigMap       = "get_configmap"
	GetSecretMetadata  = "get_secret_metadata"
	GetNetworkPolicies = "get_network_policies"
	GetResourceUsage   = "get_resource_usage"
)

// Signals are stable findings the analyzer keys on.
const (
	SignalNamespaceNotFound    = "NamespaceNotFound"
	SignalWorkloadNotFound     = "WorkloadNotFound"
	SignalReplicasUnavailable  = "ReplicasUnavailable"
	SignalRolloutStuck         = "RolloutStuck"
	SignalScaledToZero         = "ScaledToZero"
	SignalNoPods               = "NoPods"
	SignalCrashLoopBackOff     = "CrashLoopBackOff"
	SignalImagePullError       = "ImagePullError"
	SignalContainerConfigError = "ContainerConfigError"
	SignalOOMKilled            = "OOMKilled"
	SignalContainerExitError   = "ContainerExitError"
	SignalFrequentRestarts     = "FrequentRestarts"
	SignalPodNotReady          = "PodNotReady"
	SignalUnschedulable        = "Unschedulable"
	SignalEvicted              = "Evicted"
	SignalBackOff              = "BackOff"
	SignalFailedMount          = "FailedMount"
	SignalFailedScheduling     = "FailedScheduling"
	SignalFailedPull           = "FailedPull"
	SignalReadinessProbeFailed = "ReadinessProbeFailed"
	SignalLivenessProbeFailed  = "LivenessProbeFailed"
	SignalDNSResolutionError   = "DNSResolutionError"
	SignalConnectionRefused    = "ConnectionRefused"
	SignalConnectionTimeout    = "ConnectionTimeout"
	SignalConfigurationError   = "ConfigurationError"
	SignalOutOfMemory          = "OutOfMemory"
	SignalServiceNotFound      = "ServiceNotFound"
	SignalSelectorMismatch     = "SelectorMismatch"
	SignalNoReadyEndpoints     = "NoReadyEndpoints"
	SignalConfigMapMissing     = "ConfigMapMissing"
	SignalSecretMissing        = "SecretMissing"
	SignalEgressDenyAll        = "EgressDenyAll"
	SignalEgressRestricted     = "EgressRestricted"
	SignalIngressDenyAll       = "IngressDenyAll"
	SignalHighMemoryUsage      = "HighMemoryUsage"
	SignalHighCPUUsage         = "HighCPUUsage"
)

// Input is the only way to parameterize a tool. Every name in it is
// validated before any API call.
type Input struct {
	Namespace string `json:"namespace"`
	// Name is the object the tool inspects (deployment, pod, service,
	// configmap or secret).
	Name string `json:"name,omitempty"`
	// Names lists related objects: event subjects, containers, pods.
	Names []string `json:"names,omitempty"`
	// Labels are the workload's pod labels, used to find selecting Services
	// and NetworkPolicies.
	Labels map[string]string `json:"labels,omitempty"`
	// LabelSelector selects pods, e.g. "app=payment-service".
	LabelSelector string `json:"label_selector,omitempty"`
}

// Validate is the last line of defense before input reaches the API.
func (in Input) Validate() error {
	if err := domain.ValidateNamespace(in.Namespace); err != nil {
		return fmt.Errorf("namespace %w", err)
	}
	if in.Name != "" {
		if err := domain.ValidateObjectName(in.Name); err != nil {
			return fmt.Errorf("name %w", err)
		}
	}
	for _, n := range in.Names {
		if err := domain.ValidateObjectName(n); err != nil {
			return fmt.Errorf("names entry %q %w", n, err)
		}
	}
	if len(in.Names) > 50 {
		return errors.New("names must have at most 50 entries")
	}
	return nil
}

// Result is a completed observation. A Down health is a valid result: the
// tool worked and observed a failure. Tools return an error only when they
// could not observe anything.
type Result struct {
	Subject string
	Health  domain.Health
	Summary string
	Signals []string
	Data    any
}

// Tool is one allowlisted diagnostic.
type Tool func(ctx context.Context, c kube.Cluster, in Input) (Result, error)

// Registry is the complete tool allowlist. The investigation engine can run
// nothing else.
var Registry = map[string]Tool{
	GetNamespace:       getNamespace,
	GetDeployment:      getDeployment,
	GetPods:            getPods,
	GetEvents:          getEvents,
	GetPodLogs:         getPodLogs,
	GetService:         getService,
	GetConfigMap:       getConfigMap,
	GetSecretMetadata:  getSecretMetadata,
	GetNetworkPolicies: getNetworkPolicies,
	GetResourceUsage:   getResourceUsage,
}

// Run validates input and executes the named tool.
func Run(ctx context.Context, name string, c kube.Cluster, in Input) (Result, error) {
	tool, ok := Registry[name]
	if !ok {
		return Result{}, fmt.Errorf("tool %q is not allowlisted", name)
	}
	if err := in.Validate(); err != nil {
		return Result{}, fmt.Errorf("invalid input: %w", err)
	}
	return tool(ctx, c, in)
}

// signalSet collects unique signals in first-seen order.
type signalSet []string

func (s *signalSet) add(sig ...string) {
	for _, v := range sig {
		if !slices.Contains(*s, v) {
			*s = append(*s, v)
		}
	}
}

// truncate shortens s to at most n bytes without splitting a UTF-8 rune.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := strings.ToValidUTF8(s[:n], "")
	return cut + "…"
}
