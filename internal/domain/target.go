package domain

import (
	"errors"
	"fmt"
	"slices"
)

type ResourceType string

const (
	ResourceDeployment ResourceType = "deployment"
	ResourceService    ResourceType = "service"
	ResourcePod        ResourceType = "pod"
)

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
	switch t.ResourceType {
	case ResourceDeployment, ResourceService, ResourcePod:
	default:
		errs = append(errs, errors.New("resource_type must be one of deployment, service, pod"))
	}
	if err := ValidateObjectName(t.ResourceName); err != nil {
		errs = append(errs, fmt.Errorf("resource_name %w", err))
	}
	return errors.Join(errs...)
}

// Ref renders the target as "kind/name", the format used in reports.
func (t Target) Ref() string { return string(t.ResourceType) + "/" + t.ResourceName }

// TargetPolicy restricts which cluster and namespaces may be investigated.
type TargetPolicy struct {
	ClusterName string
	// AllowedNamespaces is empty when every namespace is allowed.
	AllowedNamespaces []string
}

// ErrTargetNotAllowed marks targets outside the policy.
var ErrTargetNotAllowed = errors.New("target not allowed")

func (p TargetPolicy) Check(t Target) error {
	if t.Cluster != p.ClusterName {
		return fmt.Errorf("%w: cluster %q is not configured (available: %q)", ErrTargetNotAllowed, t.Cluster, p.ClusterName)
	}
	if len(p.AllowedNamespaces) > 0 && !slices.Contains(p.AllowedNamespaces, t.Namespace) {
		return fmt.Errorf("%w: namespace %q is not in the allowed namespaces", ErrTargetNotAllowed, t.Namespace)
	}
	return nil
}
