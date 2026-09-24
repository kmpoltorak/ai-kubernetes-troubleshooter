// Package manifests holds tests for the Kubernetes manifests in this
// directory.
package manifests

import (
	"os"
	"slices"
	"strings"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
	"sigs.k8s.io/yaml"
)

type object struct {
	Kind     string                `json:"kind"`
	Metadata struct{ Name string } `json:"metadata"`
	Rules    []rbacv1.PolicyRule   `json:"rules"`
	RoleRef  rbacv1.RoleRef        `json:"roleRef"`
}

func load(t *testing.T, path string) []object {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out []object
	for _, doc := range strings.Split(string(raw), "\n---\n") {
		var o object
		if err := yaml.Unmarshal([]byte(doc), &o); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		out = append(out, o)
	}
	return out
}

func TestRBACIsReadOnlyAndLeastPrivilege(t *testing.T) {
	forbiddenResources := []string{"*", "pods/exec", "pods/attach", "pods/portforward", "pods/proxy", "nodes/proxy", "serviceaccounts/token"}
	for _, file := range []string{"rbac.yaml", "optional/secret-metadata-rbac.yaml"} {
		for _, o := range load(t, file) {
			for _, r := range o.Rules {
				for _, v := range r.Verbs {
					if v != "get" && v != "list" {
						t.Errorf("%s %s: verb %q is not read-only", file, o.Metadata.Name, v)
					}
				}
				for _, res := range r.Resources {
					if slices.Contains(forbiddenResources, res) {
						t.Errorf("%s %s: forbidden resource %q", file, o.Metadata.Name, res)
					}
					if res == "secrets" && file == "rbac.yaml" {
						t.Errorf("%s: secrets must only be granted by the optional manifest", file)
					}
					if res == "secrets" && slices.Contains(r.Verbs, "list") {
						t.Errorf("%s: listing secrets is never needed", file)
					}
				}
				if slices.Contains(r.APIGroups, "*") || len(r.NonResourceURLs) > 0 {
					t.Errorf("%s %s: wildcard group or non-resource URL", file, o.Metadata.Name)
				}
			}
			if o.Kind == "ClusterRoleBinding" && o.RoleRef.Name != "troubleshooter-namespace-reader" {
				t.Errorf("%s: only the namespace reader may be bound cluster-wide, got %s", file, o.RoleRef.Name)
			}
			if o.RoleRef.Name == "cluster-admin" || o.RoleRef.Name == "admin" || o.RoleRef.Name == "edit" {
				t.Errorf("%s: binds built-in role %s", file, o.RoleRef.Name)
			}
		}
	}
}
