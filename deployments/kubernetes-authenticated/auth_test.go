package authenticated

import (
	"os"
	"testing"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"sigs.k8s.io/yaml"
)

func TestAuthServiceIsRestrictedToApplicationPods(t *testing.T) {
	raw, err := os.ReadFile("networkpolicy.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var policy networkingv1.NetworkPolicy
	if err := yaml.Unmarshal(raw, &policy); err != nil {
		t.Fatal(err)
	}
	spec := policy.Spec
	if policy.Namespace != "troubleshooter" || len(spec.PodSelector.MatchLabels) != 1 || spec.PodSelector.MatchLabels["app"] != "troubleshooter-auth" ||
		len(spec.PodSelector.MatchExpressions) != 0 || len(spec.PolicyTypes) != 1 || spec.PolicyTypes[0] != networkingv1.PolicyTypeIngress || len(spec.Ingress) != 1 {
		t.Fatalf("auth pods are not exclusively ingress-isolated: %+v", spec)
	}
	rule := spec.Ingress[0]
	if len(rule.From) != 1 || len(rule.Ports) != 1 {
		t.Fatalf("unexpected broad ingress rule: %+v", rule)
	}
	peer, port := rule.From[0], rule.Ports[0]
	if peer.NamespaceSelector != nil || peer.IPBlock != nil || peer.PodSelector == nil || len(peer.PodSelector.MatchLabels) != 1 ||
		peer.PodSelector.MatchLabels["app"] != "troubleshooter" || len(peer.PodSelector.MatchExpressions) != 0 ||
		port.Port == nil || port.Port.IntVal != 4180 || port.EndPort != nil || port.Protocol == nil || *port.Protocol != corev1.ProtocolTCP {
		t.Fatalf("ingress permits more than same-namespace app pods on TCP 4180: %+v", rule)
	}
}
