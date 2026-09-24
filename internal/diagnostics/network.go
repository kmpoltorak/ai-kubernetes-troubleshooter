package diagnostics

import (
	"context"
	"fmt"
	"strings"

	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/domain"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/kube"
)

type PolicyRulePeer struct {
	Ports []string `json:"ports,omitempty"`
	Peers []string `json:"peers,omitempty"`
}

type PolicyInfo struct {
	Name         string           `json:"name"`
	PodSelector  string           `json:"pod_selector"`
	PolicyTypes  []string         `json:"policy_types"`
	IngressRules []PolicyRulePeer `json:"ingress_rules"`
	EgressRules  []PolicyRulePeer `json:"egress_rules"`
}

type NetworkPoliciesInfo struct {
	Policies []PolicyInfo `json:"policies"`
}

// getNetworkPolicies summarizes the NetworkPolicies that select pods with
// in.Labels. It does not emulate CNI behavior; it reports what is declared.
func getNetworkPolicies(ctx context.Context, c kube.Cluster, in Input) (Result, error) {
	list, err := c.Client.NetworkingV1().NetworkPolicies(in.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return Result{}, err
	}
	subject := "networkpolicies/" + in.Namespace
	var info NetworkPoliciesInfo
	var signals signalSet
	for _, np := range list.Items {
		sel, err := metav1.LabelSelectorAsSelector(&np.Spec.PodSelector)
		if err != nil || !sel.Matches(labels.Set(in.Labels)) {
			continue
		}
		pi := PolicyInfo{Name: np.Name, PodSelector: sel.String()}
		if pi.PodSelector == "" {
			pi.PodSelector = "<all pods>"
		}
		ingress, egress := policyTypes(np)
		for _, r := range np.Spec.Ingress {
			pi.IngressRules = append(pi.IngressRules, PolicyRulePeer{Ports: ports(r.Ports), Peers: peers(r.From)})
		}
		for _, r := range np.Spec.Egress {
			pi.EgressRules = append(pi.EgressRules, PolicyRulePeer{Ports: ports(r.Ports), Peers: peers(r.To)})
		}
		if ingress {
			pi.PolicyTypes = append(pi.PolicyTypes, "Ingress")
			if len(np.Spec.Ingress) == 0 {
				signals.add(SignalIngressDenyAll)
			}
		}
		if egress {
			pi.PolicyTypes = append(pi.PolicyTypes, "Egress")
			if len(np.Spec.Egress) == 0 {
				signals.add(SignalEgressDenyAll)
			} else if !allowsAllEgress(np.Spec.Egress) {
				signals.add(SignalEgressRestricted)
			}
		}
		info.Policies = append(info.Policies, pi)
	}
	health := domain.Healthy
	if len(signals) > 0 {
		health = domain.Degraded
	}
	summary := fmt.Sprintf("%d NetworkPolicies select the workload's pods.", len(info.Policies))
	if len(signals) > 0 {
		summary += " Findings: " + strings.Join(signals, ", ") + "."
	}
	return Result{Subject: subject, Health: health, Summary: summary, Signals: signals, Data: info}, nil
}

// policyTypes applies the API defaulting rule: Ingress always applies when
// types are omitted; Egress applies when omitted only if egress rules exist.
func policyTypes(np networkingv1.NetworkPolicy) (ingress, egress bool) {
	if len(np.Spec.PolicyTypes) == 0 {
		return true, len(np.Spec.Egress) > 0
	}
	for _, t := range np.Spec.PolicyTypes {
		ingress = ingress || t == networkingv1.PolicyTypeIngress
		egress = egress || t == networkingv1.PolicyTypeEgress
	}
	return ingress, egress
}

// allowsAllEgress reports whether some rule has no port and no peer
// restriction, i.e. allows all egress.
func allowsAllEgress(rules []networkingv1.NetworkPolicyEgressRule) bool {
	for _, r := range rules {
		if len(r.Ports) == 0 && len(r.To) == 0 {
			return true
		}
	}
	return false
}

func ports(ps []networkingv1.NetworkPolicyPort) []string {
	var out []string
	for _, p := range ps {
		proto := "TCP"
		if p.Protocol != nil {
			proto = string(*p.Protocol)
		}
		port := "any"
		if p.Port != nil {
			port = p.Port.String()
		}
		out = append(out, proto+"/"+port)
	}
	return out
}

func peers(ps []networkingv1.NetworkPolicyPeer) []string {
	var out []string
	for _, p := range ps {
		var parts []string
		if p.NamespaceSelector != nil {
			s, _ := metav1.LabelSelectorAsSelector(p.NamespaceSelector)
			parts = append(parts, "namespaces("+s.String()+")")
		}
		if p.PodSelector != nil {
			s, _ := metav1.LabelSelectorAsSelector(p.PodSelector)
			parts = append(parts, "pods("+s.String()+")")
		}
		if p.IPBlock != nil {
			parts = append(parts, "cidr("+p.IPBlock.CIDR+")")
		}
		out = append(out, strings.Join(parts, " "))
	}
	return out
}
