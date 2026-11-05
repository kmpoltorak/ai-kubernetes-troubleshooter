package diagnostics

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/domain"
)

func ingress(name string, address bool, backends ...networkingv1.IngressServiceBackend) *networkingv1.Ingress {
	ing := &networkingv1.Ingress{ObjectMeta: meta(name), Spec: networkingv1.IngressSpec{IngressClassName: ptr.To("nginx")}}
	for _, b := range backends {
		ing.Spec.Rules = append(ing.Spec.Rules, networkingv1.IngressRule{Host: "shop.example.com", IngressRuleValue: networkingv1.IngressRuleValue{
			HTTP: &networkingv1.HTTPIngressRuleValue{Paths: []networkingv1.HTTPIngressPath{{Path: "/", Backend: networkingv1.IngressBackend{Service: &b}}}}}})
	}
	if address {
		ing.Status.LoadBalancer.Ingress = []networkingv1.IngressLoadBalancerIngress{{IP: "192.0.2.10"}}
	}
	return ing
}

func backend(svc string, port int32, name string) networkingv1.IngressServiceBackend {
	return networkingv1.IngressServiceBackend{Name: svc, Port: networkingv1.ServiceBackendPort{Number: port, Name: name}}
}

func TestGetIngress(t *testing.T) {
	svc := service(appLabels) // payment-service exposing port 80 named "http"
	c := cluster(t, svc,
		ingress("ok-by-number", true, backend("payment-service", 80, "")),
		ingress("ok-by-name", true, backend("payment-service", 0, "http")),
		ingress("missing", true, backend("payment-service", 80, ""), backend("gone", 80, "")),
		ingress("wrong-port", true, backend("payment-service", 8080, "")),
		ingress("unadmitted", false, backend("payment-service", 80, "")),
	)
	for _, name := range []string{"ok-by-number", "ok-by-name"} {
		if r := run(t, c, GetIngress, Input{Name: name}); r.Health != domain.Healthy || len(r.Signals) != 0 {
			t.Errorf("%s: %+v", name, r)
		}
	}
	r := run(t, c, GetIngress, Input{Name: "missing"})
	if r.Health != domain.Down || !strings.Contains(r.Summary, "Backend gone:80: service not found.") || r.Data.(IngressInfo).FirstRoutableBackend() != "payment-service" {
		t.Errorf("missing backend: %+v", r)
	}
	wantSignals(t, r, SignalIngressBackendMissing)
	wantSignals(t, run(t, c, GetIngress, Input{Name: "wrong-port"}), SignalIngressPortMismatch)
	if r := run(t, c, GetIngress, Input{Name: "unadmitted"}); r.Health != domain.Degraded {
		t.Errorf("no address: %+v", r)
	} else {
		wantSignals(t, r, SignalIngressNoAddress)
	}
}

func claim(name string, phase corev1.PersistentVolumeClaimPhase) *corev1.PersistentVolumeClaim {
	return &corev1.PersistentVolumeClaim{ObjectMeta: meta(name), Spec: corev1.PersistentVolumeClaimSpec{StorageClassName: ptr.To("fast")},
		Status: corev1.PersistentVolumeClaimStatus{Phase: phase}}
}

func TestGetPVC(t *testing.T) {
	user := pod("db-0", func(p *corev1.Pod) {
		p.Spec.Volumes = []corev1.Volume{{Name: "data", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "data-db-0"}}}}
	})
	c := cluster(t, user, claim("data-db-0", corev1.ClaimPending), claim("spare", corev1.ClaimPending), claim("old", corev1.ClaimLost), claim("ok", corev1.ClaimBound))
	r := run(t, c, GetPVC, Input{Name: "data-db-0"})
	if r.Health != domain.Down || !slices.Equal(r.Data.(PVCInfo).UsedBy, []string{"db-0"}) {
		t.Fatalf("pending claim with a user: %+v", r)
	}
	wantSignals(t, r, SignalPVCPending)
	// Unused and Pending may be WaitForFirstConsumer: not a fault, not confirmed healthy.
	if r := run(t, c, GetPVC, Input{Name: "spare"}); r.Health != domain.Degraded || len(r.Signals) != 0 {
		t.Errorf("unused pending claim: %+v", r)
	}
	wantSignals(t, run(t, c, GetPVC, Input{Name: "old"}), SignalPVCLost)
	wantSignals(t, run(t, c, GetPVC, Input{Name: "absent"}), SignalPVCMissing)
	if r := run(t, c, GetPVC, Input{Name: "ok"}); r.Health != domain.Healthy {
		t.Errorf("bound claim: %+v", r)
	}
	spec := summarizeSpec(user.Spec)
	if !slices.Equal(spec.PVCs, []string{"data-db-0"}) {
		t.Errorf("claims in spec: %v", spec.PVCs)
	}
}

func TestGetHPA(t *testing.T) {
	hpa := func(name string, conds ...autoscalingv2.HorizontalPodAutoscalerCondition) *autoscalingv2.HorizontalPodAutoscaler {
		return &autoscalingv2.HorizontalPodAutoscaler{ObjectMeta: meta(name), Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
			ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{Kind: "Deployment", Name: "payment-service"}, MaxReplicas: 5},
			Status: autoscalingv2.HorizontalPodAutoscalerStatus{CurrentReplicas: 5, DesiredReplicas: 5, Conditions: conds}}
	}
	cond := func(typ autoscalingv2.HorizontalPodAutoscalerConditionType, status corev1.ConditionStatus, reason string) autoscalingv2.HorizontalPodAutoscalerCondition {
		return autoscalingv2.HorizontalPodAutoscalerCondition{Type: typ, Status: status, Reason: reason}
	}
	c := cluster(t,
		hpa("no-metrics", cond(autoscalingv2.ScalingActive, corev1.ConditionFalse, "FailedGetResourceMetric")),
		hpa("capped", cond(autoscalingv2.ScalingActive, corev1.ConditionTrue, "ValidMetricFound"), cond(autoscalingv2.ScalingLimited, corev1.ConditionTrue, "TooManyReplicas")),
		hpa("stuck", cond(autoscalingv2.AbleToScale, corev1.ConditionFalse, "FailedGetScale")),
		hpa("fine", cond(autoscalingv2.AbleToScale, corev1.ConditionTrue, "ReadyForNewScale")),
	)
	r := run(t, c, GetHPA, Input{Name: "no-metrics"})
	if info := r.Data.(HPAInfo); info.TargetKind != "Deployment" || info.TargetName != "payment-service" || r.Health != domain.Degraded {
		t.Fatalf("hpa: %+v", r)
	}
	wantSignals(t, r, SignalHPAMetricsUnavailable)
	wantSignals(t, run(t, c, GetHPA, Input{Name: "capped"}), SignalHPAAtMaxReplicas)
	wantSignals(t, run(t, c, GetHPA, Input{Name: "stuck"}), SignalHPAUnableToScale)
	if r := run(t, c, GetHPA, Input{Name: "fine"}); r.Health != domain.Healthy || len(r.Signals) != 0 {
		t.Errorf("healthy hpa: %+v", r)
	}
}

func TestGetNodes(t *testing.T) {
	node := func(name string, conds ...corev1.NodeCondition) *corev1.Node {
		return &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name}, Status: corev1.NodeStatus{Conditions: conds,
			Addresses: []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: "192.0.2.21"}}}}
	}
	ready := corev1.NodeCondition{Type: corev1.NodeReady, Status: corev1.ConditionTrue}
	k3s := corev1.NodeCondition{Type: "EtcdIsVoter", Status: corev1.ConditionTrue, Reason: "MemberNotLearner"} // healthy k3s control plane
	c := cluster(t, node("ok", ready, k3s), node("full", ready, corev1.NodeCondition{Type: corev1.NodeDiskPressure, Status: corev1.ConditionTrue}),
		node("dead", corev1.NodeCondition{Type: corev1.NodeReady, Status: corev1.ConditionUnknown}), node("silent")) // no Ready condition reported

	if r := run(t, c, GetNodes, Input{Names: []string{"ok"}}); r.Health != domain.Healthy || strings.Contains(r.Summary+fmt.Sprint(r.Data), "192.0.2.21") {
		t.Errorf("healthy node, addresses not recorded: %+v", r)
	}
	r := run(t, c, GetNodes, Input{Names: []string{"ok", "full"}})
	if r.Health != domain.Degraded || r.Subject != "node/full" || !strings.Contains(r.Summary, "full: Ready=True, DiskPressure") {
		t.Errorf("pressure: %+v", r)
	}
	wantSignals(t, r, SignalNodePressure)
	for _, names := range [][]string{{"ok", "dead"}, {"deleted"}, {"silent"}} {
		r := run(t, c, GetNodes, Input{Names: names})
		if r.Health != domain.Down || r.Subject != "node/"+names[len(names)-1] {
			t.Errorf("%v: %+v", names, r)
		}
		wantSignals(t, r, SignalNodeNotReady)
	}
}
