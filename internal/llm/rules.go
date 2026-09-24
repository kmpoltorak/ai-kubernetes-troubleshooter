package llm

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/diagnostics"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/domain"
)

// RulesProvider is a deterministic analyzer that needs no external model.
// It makes the platform usable offline and is the baseline for evaluation
// tests. Rules are ordered the way an engineer triages: a missing target
// first, then specific causes (image, config, scheduling, memory, network,
// probes), and generic symptoms such as CrashLoopBackOff last, because a
// symptom is only the root cause when nothing more specific explains it.
type RulesProvider struct{}

func (RulesProvider) Name() string { return "rules/rules-v1" }

type finding struct {
	rootCause  string
	summary    string
	severity   domain.Severity
	confidence float64
	signals    []string // evidence carrying any of these is cited
	causes     []string
	actions    []string
	commands   []string
}

type rule struct {
	when  func(has func(...string) bool) bool
	build func(t domain.Target) finding
}

var rules = []rule{
	{
		when: func(has func(...string) bool) bool { return has(diagnostics.SignalNamespaceNotFound) },
		build: func(t domain.Target) finding {
			return finding{
				rootCause: "Namespace does not exist", severity: domain.SeverityMedium, confidence: 0.95,
				summary:  fmt.Sprintf("Namespace %s does not exist in cluster %s, so %s cannot run there.", t.Namespace, t.Cluster, t.Ref()),
				signals:  []string{diagnostics.SignalNamespaceNotFound},
				causes:   []string{"wrong namespace in the incident", "namespace deleted or not yet created", "incident filed against the wrong cluster"},
				actions:  []string{"verify the namespace name and cluster", "check deployment pipelines for the namespace creation step"},
				commands: []string{"kubectl get namespaces"},
			}
		},
	},
	{
		when: func(has func(...string) bool) bool {
			return has(diagnostics.SignalWorkloadNotFound, diagnostics.SignalServiceNotFound)
		},
		build: func(t domain.Target) finding {
			return finding{
				rootCause: "Target resource not found", severity: domain.SeverityMedium, confidence: 0.9,
				summary:  fmt.Sprintf("%s does not exist in namespace %s.", t.Ref(), t.Namespace),
				signals:  []string{diagnostics.SignalWorkloadNotFound, diagnostics.SignalServiceNotFound},
				causes:   []string{"resource deleted or renamed", "typo in the resource name", "deployment not applied yet"},
				actions:  []string{"verify the resource name", "check recent deletions and pipeline runs for the namespace"},
				commands: []string{fmt.Sprintf("kubectl get %ss -n %s", t.ResourceType, t.Namespace)},
			}
		},
	},
	{
		when: func(has func(...string) bool) bool {
			return has(diagnostics.SignalImagePullError, diagnostics.SignalFailedPull)
		},
		build: func(t domain.Target) finding {
			return finding{
				rootCause: "Container image cannot be pulled (ImagePullBackOff)", severity: domain.SeverityHigh, confidence: 0.93,
				summary: fmt.Sprintf("Pods of %s cannot start because the container image cannot be pulled.", t.Ref()),
				signals: []string{diagnostics.SignalImagePullError, diagnostics.SignalFailedPull, diagnostics.SignalRolloutStuck},
				causes:  []string{"image tag does not exist in the registry", "missing or invalid imagePullSecret", "registry unreachable from the nodes"},
				actions: []string{
					"verify the image tag exists in the registry",
					"check imagePullSecrets and registry credentials",
					"roll back to the last working image if the new tag was never published",
				},
				commands: []string{
					fmt.Sprintf("kubectl describe pod <pod> -n %s", t.Namespace),
					fmt.Sprintf("kubectl rollout history deployment/%s -n %s", t.ResourceName, t.Namespace),
				},
			}
		},
	},
	{
		when: func(has func(...string) bool) bool {
			return has(diagnostics.SignalConfigMapMissing, diagnostics.SignalSecretMissing)
		},
		build: func(t domain.Target) finding {
			return finding{
				rootCause: "Referenced ConfigMap or Secret is missing", severity: domain.SeverityHigh, confidence: 0.95,
				summary: fmt.Sprintf("Containers of %s cannot be created because a referenced ConfigMap or Secret does not exist (CreateContainerConfigError).", t.Ref()),
				signals: []string{diagnostics.SignalConfigMapMissing, diagnostics.SignalSecretMissing, diagnostics.SignalContainerConfigError},
				causes:  []string{"ConfigMap or Secret not applied with this release", "resource renamed without updating the pod template", "resource deleted"},
				actions: []string{
					"restore or create the missing ConfigMap/Secret from source control",
					"verify the names referenced in envFrom, env and volumes match existing objects",
				},
				commands: []string{fmt.Sprintf("kubectl get configmaps -n %s", t.Namespace), fmt.Sprintf("kubectl describe pod <pod> -n %s", t.Namespace)},
			}
		},
	},
	{
		when: func(has func(...string) bool) bool { return has(diagnostics.SignalContainerConfigError) },
		build: func(t domain.Target) finding {
			return finding{
				rootCause: "Container configuration error (CreateContainerConfigError)", severity: domain.SeverityHigh, confidence: 0.75,
				summary:  fmt.Sprintf("Containers of %s cannot be created because of an invalid configuration reference.", t.Ref()),
				signals:  []string{diagnostics.SignalContainerConfigError},
				causes:   []string{"missing key in a ConfigMap or Secret", "invalid volume or env reference"},
				actions:  []string{"inspect the pod events for the exact missing reference", "fix the reference or add the missing key"},
				commands: []string{fmt.Sprintf("kubectl describe pod <pod> -n %s", t.Namespace)},
			}
		},
	},
	{
		when: func(has func(...string) bool) bool {
			return has(diagnostics.SignalUnschedulable, diagnostics.SignalFailedScheduling)
		},
		build: func(t domain.Target) finding {
			return finding{
				rootCause: "Insufficient cluster resources: pods cannot be scheduled", severity: domain.SeverityHigh, confidence: 0.9,
				summary: fmt.Sprintf("Some pods of %s stay Pending because no node has enough free resources for their requests.", t.Ref()),
				signals: []string{diagnostics.SignalUnschedulable, diagnostics.SignalFailedScheduling, diagnostics.SignalReplicasUnavailable},
				causes:  []string{"resource requests larger than any node's allocatable capacity", "cluster at capacity", "node selectors, taints or affinity limiting candidate nodes"},
				actions: []string{
					"compare the pod resource requests with node allocatable capacity",
					"reduce requests if they are oversized, or add capacity / enable the cluster autoscaler",
				},
				commands: []string{"kubectl top nodes", fmt.Sprintf("kubectl describe pod <pod> -n %s", t.Namespace)},
			}
		},
	},
	{
		when: func(has func(...string) bool) bool { return has(diagnostics.SignalOOMKilled) },
		build: func(t domain.Target) finding {
			return finding{
				rootCause: "Container OOMKilled: memory exhaustion", severity: domain.SeverityHigh, confidence: 0.94,
				summary: fmt.Sprintf("Containers of %s are killed by the kernel for exceeding their memory limit and restart repeatedly.", t.Ref()),
				signals: []string{diagnostics.SignalOOMKilled, diagnostics.SignalHighMemoryUsage, diagnostics.SignalOutOfMemory},
				causes:  []string{"memory limit too low for the workload", "memory leak", "unbounded batch or cache growth"},
				actions: []string{
					"review container memory usage against the limit",
					"increase the memory limit if the usage is legitimate",
					"investigate a possible memory leak or unbounded batch size",
				},
				commands: []string{
					fmt.Sprintf("kubectl top pod -n %s", t.Namespace),
					fmt.Sprintf("kubectl describe pod <pod> -n %s", t.Namespace),
				},
			}
		},
	},
	{
		when: func(has func(...string) bool) bool { return has(diagnostics.SignalDNSResolutionError) },
		build: func(t domain.Target) finding {
			return finding{
				rootCause: "DNS resolution failure for a dependency", severity: domain.SeverityHigh, confidence: 0.85,
				summary: fmt.Sprintf("%s cannot resolve the hostname of a dependency, so it cannot become ready.", t.Ref()),
				signals: []string{diagnostics.SignalDNSResolutionError, diagnostics.SignalReadinessProbeFailed},
				causes:  []string{"wrong or renamed Service hostname in configuration", "dependency Service missing in the target namespace", "CoreDNS unavailable"},
				actions: []string{
					"verify the dependency hostname in the application configuration",
					"check that the dependency Service exists in the expected namespace",
					"check CoreDNS pods and logs in kube-system",
				},
				commands: []string{
					fmt.Sprintf("kubectl get services -n %s", t.Namespace),
					"kubectl get pods -n kube-system -l k8s-app=kube-dns",
				},
			}
		},
	},
	{
		when: func(has func(...string) bool) bool {
			return has(diagnostics.SignalConnectionTimeout) && has(diagnostics.SignalEgressRestricted, diagnostics.SignalEgressDenyAll)
		},
		build: func(t domain.Target) finding {
			return finding{
				rootCause: "NetworkPolicy blocks egress to a dependency", severity: domain.SeverityHigh, confidence: 0.8,
				summary: fmt.Sprintf("Connections from %s to a dependency time out while a NetworkPolicy restricts its egress.", t.Ref()),
				signals: []string{diagnostics.SignalConnectionTimeout, diagnostics.SignalEgressRestricted, diagnostics.SignalEgressDenyAll},
				causes:  []string{"egress policy does not allow the dependency's port or namespace", "recently added default-deny egress policy"},
				actions: []string{
					"add an egress rule allowing the dependency's namespace/pods and port",
					"review recent NetworkPolicy changes in the namespace",
				},
				commands: []string{fmt.Sprintf("kubectl get networkpolicies -n %s -o yaml", t.Namespace)},
			}
		},
	},
	{
		when: func(has func(...string) bool) bool { return has(diagnostics.SignalLivenessProbeFailed) },
		build: func(t domain.Target) finding {
			return finding{
				rootCause: "Liveness probe failure causes container restarts", severity: domain.SeverityHigh, confidence: 0.85,
				summary: fmt.Sprintf("The kubelet restarts containers of %s because the liveness probe times out.", t.Ref()),
				signals: []string{diagnostics.SignalLivenessProbeFailed, diagnostics.SignalFrequentRestarts},
				causes:  []string{"health endpoint blocked by saturated workers", "probe timeout too aggressive", "application deadlock"},
				actions: []string{
					"make the liveness endpoint independent of request workers",
					"review probe timeoutSeconds and failureThreshold",
					"investigate saturation or deadlock in the previous container logs",
				},
				commands: []string{fmt.Sprintf("kubectl logs <pod> -n %s --previous", t.Namespace)},
			}
		},
	},
	{
		when: func(has func(...string) bool) bool {
			return has(diagnostics.SignalCrashLoopBackOff) || has(diagnostics.SignalContainerExitError) && has(diagnostics.SignalFrequentRestarts)
		},
		build: func(t domain.Target) finding {
			return finding{
				rootCause: "Application startup failure (CrashLoopBackOff)", severity: domain.SeverityCritical, confidence: 0.75,
				summary: fmt.Sprintf("Containers of %s exit with an error during startup and are in CrashLoopBackOff.", t.Ref()),
				signals: []string{diagnostics.SignalCrashLoopBackOff, diagnostics.SignalContainerExitError, diagnostics.SignalBackOff, diagnostics.SignalConfigurationError},
				causes:  []string{"missing or invalid application configuration", "dependency unavailable at startup", "bug introduced by the latest release"},
				actions: []string{
					"read the previous container logs for the startup error",
					"verify required environment variables and configuration",
					"roll back to the previous release if the failure started with a deployment",
				},
				commands: []string{
					fmt.Sprintf("kubectl logs <pod> -n %s --previous", t.Namespace),
					fmt.Sprintf("kubectl rollout history deployment/%s -n %s", t.ResourceName, t.Namespace),
				},
			}
		},
	},
	{
		when: func(has func(...string) bool) bool { return has(diagnostics.SignalSelectorMismatch) },
		build: func(t domain.Target) finding {
			return finding{
				rootCause: "Service selector mismatch: no pods match the selector", severity: domain.SeverityHigh, confidence: 0.92,
				summary:  "The Service selector matches no pods, so it has no endpoints and traffic cannot reach the workload.",
				signals:  []string{diagnostics.SignalSelectorMismatch, diagnostics.SignalNoReadyEndpoints},
				causes:   []string{"selector changed without updating pod labels", "pod labels changed in a new release", "typo in the selector"},
				actions:  []string{"align the Service selector with the pod template labels", "compare the selector with the labels of running pods"},
				commands: []string{fmt.Sprintf("kubectl get pods -n %s --show-labels", t.Namespace), fmt.Sprintf("kubectl describe service <service> -n %s", t.Namespace)},
			}
		},
	},
	{
		when: func(has func(...string) bool) bool {
			return has(diagnostics.SignalReadinessProbeFailed, diagnostics.SignalPodNotReady)
		},
		build: func(t domain.Target) finding {
			return finding{
				rootCause: "Readiness probe failure: pods are running but not ready", severity: domain.SeverityHigh, confidence: 0.8,
				summary: fmt.Sprintf("Pods of %s are running but fail their readiness probe, so they receive no traffic.", t.Ref()),
				signals: []string{diagnostics.SignalReadinessProbeFailed, diagnostics.SignalPodNotReady, diagnostics.SignalNoReadyEndpoints},
				causes:  []string{"a dependency checked by the readiness endpoint is unavailable", "slow warm-up exceeding the probe budget", "wrong probe path or port"},
				actions: []string{
					"check what the readiness endpoint reports in the logs",
					"verify probe path, port and timing against the application",
				},
				commands: []string{fmt.Sprintf("kubectl describe pod <pod> -n %s", t.Namespace), fmt.Sprintf("kubectl logs <pod> -n %s", t.Namespace)},
			}
		},
	},
	{
		when: func(has func(...string) bool) bool { return has(diagnostics.SignalConnectionRefused) },
		build: func(t domain.Target) finding {
			return finding{
				rootCause: "A dependency refuses connections", severity: domain.SeverityMedium, confidence: 0.65,
				summary: fmt.Sprintf("Logs of %s show refused connections to a dependency.", t.Ref()),
				signals: []string{diagnostics.SignalConnectionRefused},
				causes:  []string{"dependency down or restarting", "wrong port in configuration"},
				actions: []string{"check the health of the dependency", "verify the configured host and port"},
			}
		},
	},
	{
		when: func(has func(...string) bool) bool {
			return has(diagnostics.SignalReplicasUnavailable, diagnostics.SignalNoReadyEndpoints, diagnostics.SignalHighMemoryUsage, diagnostics.SignalHighCPUUsage)
		},
		build: func(t domain.Target) finding {
			return finding{
				rootCause: "Workload degraded without a specific cause", severity: domain.SeverityMedium, confidence: 0.5,
				summary: fmt.Sprintf("%s shows degraded signals but the evidence does not identify a specific cause; confidence is low.", t.Ref()),
				signals: []string{diagnostics.SignalReplicasUnavailable, diagnostics.SignalNoReadyEndpoints, diagnostics.SignalHighMemoryUsage, diagnostics.SignalHighCPUUsage},
				causes:  []string{"transient rollout", "resource saturation"},
				actions: []string{"re-run the investigation while the problem is occurring", "review recent changes to the workload"},
			}
		},
	},
}

func (RulesProvider) Analyze(_ context.Context, in AnalysisInput) (domain.Analysis, error) {
	if len(in.Evidence) == 0 {
		return domain.Analysis{}, errors.New("rules: no evidence to analyze")
	}
	var all []string
	for _, e := range in.Evidence {
		all = append(all, e.Signals...)
	}
	has := func(sigs ...string) bool {
		return slices.ContainsFunc(sigs, func(s string) bool { return slices.Contains(all, s) })
	}

	t := in.Incident.Target
	f := finding{
		rootCause: "No Kubernetes-level fault detected", severity: domain.SeverityLow, confidence: 0.6,
		summary: fmt.Sprintf("%s looks healthy: pods are ready, no warning events and no error patterns were found. The problem may be outside Kubernetes or intermittent.", t.Ref()),
		causes:  []string{"application-level error not visible in the sampled logs", "intermittent issue not present during the checks", "problem in an external dependency"},
		actions: []string{"check application metrics and error rates", "re-run the investigation while the problem is occurring"},
	}
	for _, r := range rules {
		if r.when(has) {
			f = r.build(t)
			break
		}
	}
	return f.analysis(t, in.Evidence), nil
}

// analysis cites the evidence carrying the finding's signals (or all
// evidence when none does) and lists concrete affected resources.
func (f finding) analysis(t domain.Target, evidence []domain.Evidence) domain.Analysis {
	a := domain.Analysis{
		Summary: f.summary, RootCause: f.rootCause, Confidence: f.confidence, Severity: f.severity,
		AffectedResources: []string{t.Ref()}, PossibleCauses: f.causes, RecommendedActions: f.actions, SafeCommands: f.commands,
	}
	for _, e := range evidence {
		if len(f.signals) > 0 && !slices.ContainsFunc(e.Signals, func(s string) bool { return slices.Contains(f.signals, s) }) {
			continue
		}
		a.Evidence = append(a.Evidence, domain.EvidenceReference{Source: e.Source, Description: e.Summary})
		if kind, _, _ := strings.Cut(e.Subject, "/"); slices.Contains(concreteKinds, kind) && !slices.Contains(a.AffectedResources, e.Subject) {
			a.AffectedResources = append(a.AffectedResources, e.Subject)
		}
	}
	if len(a.Evidence) == 0 {
		for _, e := range evidence {
			a.Evidence = append(a.Evidence, domain.EvidenceReference{Source: e.Source, Description: e.Summary})
		}
	}
	return a
}

var concreteKinds = []string{"deployment", "pod", "service", "configmap", "secret", "namespace"}
