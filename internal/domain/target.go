package domain

import (
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
)

type ResourceType string

const (
	ResourceDeployment  ResourceType = "deployment"
	ResourceStatefulSet ResourceType = "statefulset"
	ResourceDaemonSet   ResourceType = "daemonset"
	ResourceJob         ResourceType = "job"
	ResourceCronJob     ResourceType = "cronjob"
	ResourceService     ResourceType = "service"
	ResourcePod         ResourceType = "pod"
	ResourceIngress     ResourceType = "ingress"
	ResourcePVC         ResourceType = "persistentvolumeclaim"
	ResourceHPA         ResourceType = "horizontalpodautoscaler"
)

// ResourceTypes lists every kind an incident can target, in the order the form offers them.
var ResourceTypes = []ResourceType{ResourceDeployment, ResourceStatefulSet, ResourceDaemonSet, ResourceJob, ResourceCronJob, ResourceService, ResourcePod, ResourceIngress, ResourcePVC, ResourceHPA}

var customKind = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)+$`)

// Custom reports whether the kind has the shape of a custom resource, "resource.group"
// as kubectl names it (rollouts.argoproj.io). Whether it is configured is the policy's call.
func (rt ResourceType) Custom() bool {
	return len(rt) <= 253 && customKind.MatchString(string(rt)) && !slices.Contains(ResourceTypes, rt)
}

// CustomResource is an operator-configured kind read through the dynamic client.
type CustomResource struct {
	Group, Version, Resource string
}

// Kind is the name incidents use for the resource.
func (c CustomResource) Kind() ResourceType { return ResourceType(c.Resource + "." + c.Group) }

// Target identifies the workload an incident is about: the cluster
// (ClusterTarget) and the namespaced resource (ResourceTarget).
type Target struct {
	Cluster      string       `json:"cluster"`
	Namespace    string       `json:"namespace"`
	ResourceType ResourceType `json:"resource_type"`
	ResourceName string       `json:"resource_name"`
}

// Validate checks every field is well formed. It is the gate in front of
// every Kubernetes API call built from user input.
func (t Target) Validate() error {
	var errs []error
	if err := ValidateObjectName(t.Cluster); err != nil {
		errs = append(errs, fmt.Errorf("cluster %w", err))
	}
	if err := ValidateNamespace(t.Namespace); err != nil {
		errs = append(errs, fmt.Errorf("namespace %w", err))
	}
	if !slices.Contains(ResourceTypes, t.ResourceType) && !t.ResourceType.Custom() {
		errs = append(errs, fmt.Errorf("resource_type must be one of %v or a configured custom resource (resource.group)", ResourceTypes))
	}
	if err := ValidateObjectName(t.ResourceName); err != nil {
		errs = append(errs, fmt.Errorf("resource_name %w", err))
	}
	return errors.Join(errs...)
}

// Ref renders the target as "kind/name", the format used in reports.
func (t Target) Ref() string { return string(t.ResourceType) + "/" + t.ResourceName }

// TargetPolicy restricts which clusters and namespaces may be investigated.
type TargetPolicy struct {
	// Clusters maps each configured cluster name to its allowed namespaces;
	// an empty list allows every namespace in that cluster.
	Clusters map[string][]string
	// CustomResources are the custom kinds an incident may target.
	CustomResources []ResourceType
}

// ErrTargetNotAllowed marks targets outside the policy.
var ErrTargetNotAllowed = errors.New("target not allowed")

func (p TargetPolicy) Check(t Target) error {
	allowed, ok := p.Clusters[t.Cluster]
	if !ok {
		return fmt.Errorf("%w: cluster %q is not configured (available: %q)", ErrTargetNotAllowed, t.Cluster, slices.Sorted(maps.Keys(p.Clusters)))
	}
	if len(allowed) > 0 && !slices.Contains(allowed, t.Namespace) {
		return fmt.Errorf("%w: namespace %q is not in the allowed namespaces of cluster %q", ErrTargetNotAllowed, t.Namespace, t.Cluster)
	}
	if t.ResourceType.Custom() && !slices.Contains(p.CustomResources, t.ResourceType) {
		return fmt.Errorf("%w: custom resource %q is not configured (KUBERNETES_CUSTOM_RESOURCES)", ErrTargetNotAllowed, t.ResourceType)
	}
	return nil
}
