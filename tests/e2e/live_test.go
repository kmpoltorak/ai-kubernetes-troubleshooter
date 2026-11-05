//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	authenticationv1 "k8s.io/api/authentication/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/yaml"

	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/diagnostics"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/domain"
)

func TestLiveCluster(t *testing.T) {
	cfg, client := clusterClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 29*time.Minute)
	defer cancel()
	version, err := client.Discovery().ServerVersion()
	if err != nil || version.GitVersion != "v"+os.Getenv("E2E_KUBERNETES_VERSION") {
		t.Fatal("unexpected Kubernetes version")
	}
	apiURL, err := url.Parse(os.Getenv("E2E_API_URL"))
	if err != nil || apiURL.Scheme != "http" || !net.ParseIP(apiURL.Hostname()).IsLoopback() || apiURL.User != nil || apiURL.RawQuery != "" {
		t.Fatal("API must be the runner's loopback port-forward")
	}
	image := os.Getenv("E2E_FIXTURE_IMAGE")
	if !strings.HasPrefix(image, "akt-e2e-fixture:") {
		t.Fatal("explicit local fixture image required")
	}
	config, err := client.CoreV1().ConfigMaps("troubleshooter").Get(ctx, "troubleshooter-config", metav1.GetOptions{})
	if err != nil || config.Data["SIMULATION_ENABLED"] != "false" || config.Data["AUTH_ENABLED"] != "false" || config.Data["LLM_PROVIDER"] != "rules" || config.Data["KUBERNETES_CLUSTERS_FILE"] != "/etc/troubleshooter/clusters.yaml" || config.Data["KUBERNETES_CUSTOM_RESOURCES"] != "example.com/v1/widgets" {
		t.Fatal("unexpected application configuration")
	}
	deployment, err := client.AppsV1().Deployments("troubleshooter").Get(ctx, "troubleshooter", metav1.GetOptions{})
	if err != nil || deployment.Spec.Replicas == nil || *deployment.Spec.Replicas != 2 || deployment.Status.ReadyReplicas != 2 || deployment.Spec.Template.Spec.ServiceAccountName != "troubleshooter" {
		t.Fatal("expected two ready replicas with the production ServiceAccount")
	}
	expires := int64(3600)
	token, err := client.CoreV1().ServiceAccounts("troubleshooter").CreateToken(ctx, "troubleshooter", &authenticationv1.TokenRequest{Spec: authenticationv1.TokenRequestSpec{ExpirationSeconds: &expires}}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal("could not create ephemeral RBAC probe identity")
	}
	sa, err := kubernetes.NewForConfig(&rest.Config{Host: cfg.Host, TLSClientConfig: rest.TLSClientConfig{CAData: cfg.CAData, CAFile: cfg.CAFile}, BearerToken: token.Status.Token, Timeout: 10 * time.Second, UserAgent: "akt-e2e-rbac-probe"})
	if err != nil {
		t.Fatal("could not create RBAC probe client")
	}
	s := &suite{ctx: ctx, admin: client, sa: sa, apiURL: apiURL.String(), http: &http.Client{Timeout: 15 * time.Second}, image: image}
	t.Run("build_metadata", func(t *testing.T) {
		for _, path := range []string{"/health", "/ready"} {
			var response struct {
				Build struct {
					Version  string  `json:"version"`
					Revision string  `json:"revision"`
					BuiltAt  *string `json:"built_at"`
				} `json:"build"`
			}
			if s.call(t, s.ctx, "GET", path, nil, &response) != 200 {
				t.Fatal("probe unavailable")
			}
			if response.Build.Version != os.Getenv("E2E_BUILD_VERSION") || response.Build.Revision != os.Getenv("E2E_BUILD_REVISION") || response.Build.BuiltAt == nil || *response.Build.BuiltAt != os.Getenv("E2E_BUILD_TIME") {
				t.Fatalf("linked binary metadata does not match image labels on %s", path)
			}
		}
	})
	s.deployment(t, "healthy", nil)
	healthyPod := s.pod(t, "app=healthy", ready)
	eventually(t, ctx, 3*time.Minute, "healthy deployment status", func(ctx context.Context) (bool, error) {
		d, err := client.AppsV1().Deployments("payments").Get(ctx, "healthy", metav1.GetOptions{})
		return err == nil && d.Status.ObservedGeneration == d.Generation && d.Status.ReadyReplicas == 1 && d.Status.AvailableReplicas == 1, err
	})
	_, err = client.CoreV1().Services("payments").Create(ctx, &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "healthy"}, Spec: corev1.ServiceSpec{Selector: map[string]string{"app": "healthy"}, Ports: []corev1.ServicePort{{Port: 80, TargetPort: intstr.FromInt32(8080)}}}}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, ctx, 3*time.Minute, "ready EndpointSlice", func(ctx context.Context) (bool, error) {
		list, err := client.DiscoveryV1().EndpointSlices("payments").List(ctx, metav1.ListOptions{LabelSelector: "kubernetes.io/service-name=healthy"})
		if err != nil {
			return false, err
		}
		for _, item := range list.Items {
			for _, ep := range item.Endpoints {
				if ep.Conditions.Ready != nil && *ep.Conditions.Ready {
					return true, nil
				}
			}
		}
		return false, nil
	})
	t.Run("healthy_targets", func(t *testing.T) {
		for _, target := range []struct {
			kind domain.ResourceType
			name string
		}{{domain.ResourceDeployment, "healthy"}, {domain.ResourcePod, healthyPod.Name}, {domain.ResourceService, "healthy"}} {
			t.Run(string(target.kind), func(t *testing.T) {
				a := report(t, s.investigate(t, target.kind, target.name))
				if a.Verdict != "no_fault_observed" || a.Support != "limited" {
					t.Fatalf("healthy target assessment: %s/%s", a.Verdict, a.Support)
				}
			})
		}
	})
	t.Run("statefulset", func(t *testing.T) {
		labels, replicas := map[string]string{"app": "ledger-db"}, int32(1)
		_, err := client.AppsV1().StatefulSets("payments").Create(ctx, &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: "ledger-db"},
			Spec: appsv1.StatefulSetSpec{Replicas: &replicas, ServiceName: "ledger-db", Selector: &metav1.LabelSelector{MatchLabels: labels},
				Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels}, Spec: podSpec(s.image)}}}, metav1.CreateOptions{})
		if err != nil {
			t.Fatal(err)
		}
		eventually(t, ctx, 3*time.Minute, "ready statefulset", func(ctx context.Context) (bool, error) {
			st, err := client.AppsV1().StatefulSets("payments").Get(ctx, "ledger-db", metav1.GetOptions{})
			return err == nil && st.Status.ObservedGeneration == st.Generation && st.Status.ReadyReplicas == 1 && st.Status.UpdatedReplicas == 1, err
		})
		rec := s.investigate(t, domain.ResourceStatefulSet, "ledger-db")
		if a := report(t, rec); a.Verdict != "no_fault_observed" || !slices.ContainsFunc(rec.ToolExecutions, func(te domain.ToolExecution) bool { return te.ToolName == diagnostics.GetStatefulSet && te.Error == "" }) {
			t.Fatalf("healthy statefulset: %s", a.Verdict)
		}
		// The node running the pod is read through the cluster-wide node reader.
		if !slices.ContainsFunc(rec.Evidence, func(e domain.Evidence) bool { return e.Source == diagnostics.GetNodes && e.Health == domain.Healthy }) {
			t.Fatal("node of a healthy pod not read as healthy")
		}
		var listed struct {
			Names []string `json:"names"`
		}
		if code := s.call(t, ctx, "GET", "/api/v1/targets?cluster=local&namespace=payments&kind=statefulset", nil, &listed); code != 200 || !slices.Contains(listed.Names, "ledger-db") {
			t.Fatalf("statefulset names not listed: %d %v", code, listed.Names)
		}
	})
	t.Run("failed_job", func(t *testing.T) {
		spec := podSpec(s.image)
		spec.RestartPolicy, spec.Containers[0].ReadinessProbe, spec.Containers[0].Args = corev1.RestartPolicyNever, nil, []string{"--no-such-flag"}
		backoff := int32(0)
		if _, err := client.BatchV1().Jobs("payments").Create(ctx, &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "migrate"},
			Spec: batchv1.JobSpec{BackoffLimit: &backoff, Template: corev1.PodTemplateSpec{Spec: spec}}}, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
		eventually(t, ctx, 3*time.Minute, "failed job", func(ctx context.Context) (bool, error) {
			j, err := client.BatchV1().Jobs("payments").Get(ctx, "migrate", metav1.GetOptions{})
			return err == nil && slices.ContainsFunc(j.Status.Conditions, func(c batchv1.JobCondition) bool {
				return c.Type == batchv1.JobFailed && c.Status == corev1.ConditionTrue
			}), err
		})
		rec := s.investigate(t, domain.ResourceJob, "migrate")
		requireSignal(t, rec, diagnostics.SignalJobFailed)
		requireSignal(t, rec, diagnostics.SignalContainerExitError)
		if a := report(t, rec); a.Verdict != "observed_fault" || a.RootCause != "Job failed: its pods exited with errors" {
			t.Fatalf("failed job: %s %q", a.Verdict, a.RootCause)
		}
	})
	t.Run("ingress_missing_backend", func(t *testing.T) {
		path := networkingv1.PathTypePrefix
		route := func(svc string) networkingv1.HTTPIngressPath {
			return networkingv1.HTTPIngressPath{Path: "/" + svc, PathType: &path, Backend: networkingv1.IngressBackend{Service: &networkingv1.IngressServiceBackend{Name: svc, Port: networkingv1.ServiceBackendPort{Number: 80}}}}
		}
		if _, err := client.NetworkingV1().Ingresses("payments").Create(ctx, &networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Name: "shop"},
			Spec: networkingv1.IngressSpec{Rules: []networkingv1.IngressRule{{Host: "shop.example.com", IngressRuleValue: networkingv1.IngressRuleValue{
				HTTP: &networkingv1.HTTPIngressRuleValue{Paths: []networkingv1.HTTPIngressPath{route("healthy"), route("no-such-service")}}}}}}}, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
		rec := s.investigate(t, domain.ResourceIngress, "shop")
		requireSignal(t, rec, diagnostics.SignalIngressBackendMissing)
		requireSignal(t, rec, diagnostics.SignalIngressNoAddress) // kind runs no ingress controller
		if a := report(t, rec); a.RootCause != "Ingress routes to a Service that does not exist" || !slices.ContainsFunc(rec.ToolExecutions, func(te domain.ToolExecution) bool { return te.ToolName == diagnostics.GetPodLogs }) {
			t.Fatalf("ingress: %q; the existing backend's pods must still be read", a.RootCause)
		}
	})
	t.Run("pending_claim", func(t *testing.T) {
		class := "no-such-class"
		if _, err := client.CoreV1().PersistentVolumeClaims("payments").Create(ctx, &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "ledger-data"},
			Spec: corev1.PersistentVolumeClaimSpec{StorageClassName: &class, AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")}}}}, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
		s.deployment(t, "ledger", func(p *corev1.PodSpec) {
			p.Volumes = []corev1.Volume{{Name: "data", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "ledger-data"}}}}
		})
		s.pod(t, "app=ledger", func(p corev1.Pod) bool { return p.Status.Phase == corev1.PodPending })
		rec := s.investigate(t, domain.ResourceDeployment, "ledger")
		requireSignal(t, rec, diagnostics.SignalPVCPending)
		if a := report(t, rec); a.RootCause != "PersistentVolumeClaim is not bound (Pending)" || !slices.Contains(a.SafeCommands, "kubectl describe pvc ledger-data -n payments") {
			t.Fatalf("pending claim: %q %v", a.RootCause, a.SafeCommands)
		}
	})
	t.Run("autoscaler_without_metrics", func(t *testing.T) {
		if _, err := client.AutoscalingV2().HorizontalPodAutoscalers("payments").Create(ctx, &autoscalingv2.HorizontalPodAutoscaler{ObjectMeta: metav1.ObjectMeta{Name: "healthy"},
			Spec: autoscalingv2.HorizontalPodAutoscalerSpec{ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{APIVersion: "apps/v1", Kind: "Deployment", Name: "healthy"}, MaxReplicas: 3,
				Metrics: []autoscalingv2.MetricSpec{{Type: autoscalingv2.ResourceMetricSourceType, Resource: &autoscalingv2.ResourceMetricSource{Name: corev1.ResourceCPU,
					Target: autoscalingv2.MetricTarget{Type: autoscalingv2.UtilizationMetricType, AverageUtilization: ptr.To[int32](80)}}}}}}, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
		eventually(t, ctx, 3*time.Minute, "autoscaler reports missing metrics", func(ctx context.Context) (bool, error) {
			h, err := client.AutoscalingV2().HorizontalPodAutoscalers("payments").Get(ctx, "healthy", metav1.GetOptions{})
			return err == nil && slices.ContainsFunc(h.Status.Conditions, func(c autoscalingv2.HorizontalPodAutoscalerCondition) bool {
				return c.Type == autoscalingv2.ScalingActive && c.Status == corev1.ConditionFalse
			}), err
		})
		rec := s.investigate(t, domain.ResourceHPA, "healthy")
		requireSignal(t, rec, diagnostics.SignalHPAMetricsUnavailable)
		if a := report(t, rec); a.RootCause != "Autoscaler cannot read its metrics" || !slices.ContainsFunc(rec.ToolExecutions, func(te domain.ToolExecution) bool { return te.ToolName == diagnostics.GetDeployment }) {
			t.Fatalf("autoscaler: %q; its target must be diagnosed too", a.RootCause)
		}
	})
	t.Run("custom_resource", func(t *testing.T) {
		dyn, err := dynamic.NewForConfig(cfg)
		if err != nil {
			t.Fatal(err)
		}
		widgets := dyn.Resource(schema.GroupVersionResource{Group: "example.com", Version: "v1", Resource: "widgets"}).Namespace("payments")
		var created *unstructured.Unstructured
		eventually(t, ctx, time.Minute, "widget CRD served", func(ctx context.Context) (bool, error) {
			created, err = widgets.Create(ctx, &unstructured.Unstructured{Object: map[string]any{"apiVersion": "example.com/v1", "kind": "Widget",
				"metadata": map[string]any{"name": "storefront"},
				"spec":     map[string]any{"selector": map[string]any{"matchLabels": map[string]any{"app": "healthy"}}, "apiToken": "e2e-spec-value-not-recorded"}}}, metav1.CreateOptions{})
			return err == nil, nil
		})
		created.Object["status"] = map[string]any{"conditions": []any{map[string]any{"type": "Ready", "status": "False", "reason": "BackendUnavailable", "message": "payment provider unreachable"}}}
		if _, err := widgets.UpdateStatus(ctx, created, metav1.UpdateOptions{}); err != nil {
			t.Fatal(err)
		}
		rec := s.investigate(t, "widgets.example.com", "storefront")
		requireSignal(t, rec, diagnostics.SignalCustomResourceNotReady)
		raw, _ := json.Marshal(rec)
		if a := report(t, rec); a.RootCause != "The resource reports a failing status condition" || strings.Contains(string(raw), "e2e-spec-value-not-recorded") ||
			!slices.ContainsFunc(rec.Evidence, func(e domain.Evidence) bool { return e.Source == diagnostics.GetPods && e.Health == domain.Healthy }) {
			t.Fatalf("custom resource: %q; spec values must stay out and its pods must be read", a.RootCause)
		}
		var listed struct {
			Names []string `json:"names"`
		}
		if code := s.call(t, ctx, "GET", "/api/v1/targets?cluster=local&namespace=payments&kind=widgets.example.com", nil, &listed); code != 200 || !slices.Contains(listed.Names, "storefront") {
			t.Fatalf("custom names not listed: %d %v", code, listed.Names)
		}
	})
	t.Run("missing_configmap", func(t *testing.T) {
		s.deployment(t, "missing-config", func(p *corev1.PodSpec) {
			p.Containers[0].Env = []corev1.EnvVar{{Name: "MODE", ValueFrom: &corev1.EnvVarSource{ConfigMapKeyRef: &corev1.ConfigMapKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "absent-config"}, Key: "mode"}}}}
		})
		s.pod(t, "app=missing-config", func(p corev1.Pod) bool { return waiting(p, "CreateContainerConfigError") })
		rec := s.investigate(t, domain.ResourceDeployment, "missing-config")
		requireSignal(t, rec, diagnostics.SignalConfigMapMissing)
		failedTool(t, rec, diagnostics.GetPodLogs)
		if report(t, rec).Verdict != "observed_fault" {
			t.Fatal("missing config not observed")
		}
	})
	t.Run("readiness_failure", func(t *testing.T) {
		s.deployment(t, "not-ready", func(p *corev1.PodSpec) { p.Containers[0].Args = []string{"--ready=false"} })
		pod := s.pod(t, "app=not-ready", func(p corev1.Pod) bool { return p.Status.Phase == corev1.PodRunning && !ready(p) })
		eventually(t, ctx, 3*time.Minute, "readiness warning event", func(ctx context.Context) (bool, error) {
			events, err := client.CoreV1().Events("payments").List(ctx, metav1.ListOptions{FieldSelector: "involvedObject.name=" + pod.Name})
			if err != nil {
				return false, err
			}
			return slices.ContainsFunc(events.Items, func(e corev1.Event) bool { return strings.Contains(e.Message, "Readiness probe failed") }), nil
		})
		rec := s.investigate(t, domain.ResourceDeployment, "not-ready")
		requireSignal(t, rec, diagnostics.SignalReadinessProbeFailed)
		if report(t, rec).Verdict != "observed_fault" {
			t.Fatal("probe failure not observed")
		}
	})
	t.Run("invalid_image", func(t *testing.T) {
		s.deployment(t, "bad-image", func(p *corev1.PodSpec) { p.Containers[0].Image = "invalid image" })
		s.pod(t, "app=bad-image", func(p corev1.Pod) bool { return waiting(p, "InvalidImageName") })
		rec := s.investigate(t, domain.ResourceDeployment, "bad-image")
		requireSignal(t, rec, diagnostics.SignalImagePullError)
		if report(t, rec).Verdict != "observed_fault" {
			t.Fatal("image failure not observed")
		}
	})
	t.Run("service_selector_mismatch", func(t *testing.T) {
		_, err := client.CoreV1().Services("payments").Create(ctx, &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "mismatch"}, Spec: corev1.ServiceSpec{Selector: map[string]string{"app": "no-such-workload"}, Ports: []corev1.ServicePort{{Port: 80, TargetPort: intstr.FromInt32(8080)}}}}, metav1.CreateOptions{})
		if err != nil {
			t.Fatal(err)
		}
		rec := s.investigate(t, domain.ResourceService, "mismatch")
		requireSignal(t, rec, diagnostics.SignalSelectorMismatch)
		if report(t, rec).Verdict != "observed_fault" {
			t.Fatal("selector mismatch not observed")
		}
	})
	t.Run("namespace_and_read_only_rbac", func(t *testing.T) { s.checkRBAC(t) })
	t.Run("denied_logs", func(t *testing.T) {
		s.withoutPermission(t, "pods/log")
		rec := s.investigate(t, domain.ResourceDeployment, "healthy")
		failedTool(t, rec, diagnostics.GetPodLogs)
		a := report(t, rec)
		if a.Verdict != "inconclusive" || !strings.Contains(strings.Join(a.MissingData, " "), diagnostics.GetPodLogs) {
			t.Fatal("denied logs concealed or health confirmed")
		}
	})
	t.Run("unreadable_target", func(t *testing.T) {
		s.withoutPermission(t, "deployments")
		rec := s.investigate(t, domain.ResourceDeployment, "healthy")
		failedTool(t, rec, diagnostics.GetDeployment)
		if rec.Investigation.Status != domain.InvestigationFailed || rec.Report != nil {
			t.Fatal("unreadable target diagnosed")
		}
	})
	t.Run("optional_metrics", func(t *testing.T) {
		_, err := client.Discovery().RESTClient().Get().AbsPath("/apis/metrics.k8s.io/v1beta1").DoRaw(ctx)
		if !apierrors.IsNotFound(err) {
			t.Fatal("fixture unexpectedly serves metrics API")
		}
		rec := s.investigate(t, domain.ResourceDeployment, "healthy")
		failedTool(t, rec, diagnostics.GetResourceUsage)
		if !strings.Contains(strings.Join(report(t, rec).MissingData, " "), diagnostics.GetResourceUsage) {
			t.Fatal("missing metrics undisclosed")
		}
		var ready struct {
			Checks map[string]string `json:"checks"`
		}
		if s.call(t, s.ctx, "GET", "/ready", nil, &ready) != 200 || ready.Checks["database"] != "ok" || ready.Checks["kubernetes"] != "ok" {
			t.Fatal("optional diagnostic made application unready")
		}
	})
	t.Run("two_cluster_names", func(t *testing.T) {
		var status struct {
			Clusters []struct{ Name, Status string } `json:"clusters"`
		}
		if code := s.call(t, ctx, "GET", "/api/v1/clusters", nil, &status); code != 200 || fmt.Sprint(status.Clusters) != "[{local ok} {mirror ok}]" {
			t.Fatalf("cluster status: %d %v", code, status.Clusters)
		}
		// Each name has its own namespaces: payments is allowed in local only.
		body := map[string]string{"cluster": "mirror", "namespace": "payments", "resource_type": "deployment", "resource_name": "healthy", "description": "wrong cluster"}
		if s.call(t, ctx, "POST", "/api/v1/incidents", body, nil) != 400 || s.call(t, ctx, "GET", "/api/v1/targets?cluster=mirror&namespace=payments&kind=deployment", nil, nil) != 400 {
			t.Fatal("namespace of another cluster admitted")
		}
		// mirror/outside passes the policy and reaches the cluster; RBAC still denies the reads.
		rec := s.investigateIn(t, "mirror", "outside", domain.ResourcePod, "target")
		if rec.Investigation.Status != domain.InvestigationFailed || rec.Report != nil {
			t.Fatal("pod without a RoleBinding diagnosed")
		}
	})
	t.Run("optional_secret_metadata", func(t *testing.T) { s.checkSecretMetadata(t) })
	t.Run("network_policy_enforcement", func(t *testing.T) { s.checkNetworkPolicy(t) })
}

func (s *suite) allowed(ctx context.Context, verb, group, resource, subresource, namespace string) (bool, error) {
	result, err := s.sa.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx, &authorizationv1.SelfSubjectAccessReview{Spec: authorizationv1.SelfSubjectAccessReviewSpec{ResourceAttributes: &authorizationv1.ResourceAttributes{Verb: verb, Group: group, Resource: resource, Subresource: subresource, Namespace: namespace}}}, metav1.CreateOptions{})
	if err != nil {
		return false, err
	}
	return result.Status.Allowed, nil
}

func (s *suite) withoutPermission(t *testing.T, resource string) {
	t.Helper()
	role, err := s.admin.RbacV1().ClusterRoles().Get(s.ctx, "troubleshooter-reader", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	original := role.DeepCopy()
	parts := strings.Split(resource, "/")
	group, sub := "", ""
	if resource == "deployments" {
		group = "apps"
	}
	if len(parts) == 2 {
		sub = parts[1]
	}
	t.Cleanup(func() {
		current, err := s.admin.RbacV1().ClusterRoles().Get(s.ctx, role.Name, metav1.GetOptions{})
		if err != nil {
			t.Error(err)
			return
		}
		current.Rules = original.Rules
		if _, err := s.admin.RbacV1().ClusterRoles().Update(s.ctx, current, metav1.UpdateOptions{}); err != nil {
			t.Error(err)
			return
		}
		eventually(t, s.ctx, 30*time.Second, "restored RBAC", func(ctx context.Context) (bool, error) {
			return s.allowed(ctx, "get", group, parts[0], sub, "payments")
		})
	})
	var rules []rbacv1.PolicyRule
	for _, rule := range role.Rules {
		rule.Resources = slices.DeleteFunc(slices.Clone(rule.Resources), func(r string) bool { return r == resource })
		if len(rule.Resources) > 0 {
			rules = append(rules, rule)
		}
	}
	role.Rules = rules
	if _, err := s.admin.RbacV1().ClusterRoles().Update(s.ctx, role, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	eventually(t, s.ctx, 30*time.Second, "restricted RBAC", func(ctx context.Context) (bool, error) {
		allowed, err := s.allowed(ctx, "get", group, parts[0], sub, "payments")
		return !allowed, err
	})
}

func (s *suite) checkRBAC(t *testing.T) {
	if _, err := s.admin.CoreV1().Namespaces().Create(s.ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "outside"}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}

	if _, err := s.admin.CoreV1().Pods("outside").Create(s.ctx, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "target"}, Spec: podSpec(s.image)}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.sa.CoreV1().Pods("outside").Get(s.ctx, "target", metav1.GetOptions{}); !apierrors.IsForbidden(err) {
		t.Fatal("workload outside RoleBinding readable")
	}
	if _, err := s.sa.CoreV1().Namespaces().Get(s.ctx, "outside", metav1.GetOptions{}); err != nil {
		t.Fatal("documented global namespace metadata grant missing")
	}
	body := map[string]string{"cluster": "local", "namespace": "outside", "resource_type": "pod", "resource_name": "target", "description": "forbidden fixture"}
	if s.call(t, s.ctx, "POST", "/api/v1/incidents", body, nil) != 400 {
		t.Fatal("API admitted namespace outside policy")
	}
	// The incident form's name suggestions list through the same read-only ServiceAccount.
	var listed struct {
		Names []string `json:"names"`
	}
	if code := s.call(t, s.ctx, "GET", "/api/v1/targets?cluster=local&namespace=payments&kind=deployment", nil, &listed); code != 200 || !slices.Contains(listed.Names, "healthy") {
		t.Fatalf("deployment names not listed: %d %v", code, listed.Names)
	}
	if s.call(t, s.ctx, "GET", "/api/v1/targets?cluster=local&namespace=outside&kind=pod", nil, nil) != 400 {
		t.Fatal("API listed names outside policy")
	}
	for _, probe := range []struct{ verb, group, resource, sub string }{{"create", "", "pods", ""}, {"update", "apps", "deployments", ""}, {"patch", "apps", "deployments", ""}, {"delete", "", "pods", ""}, {"create", "", "pods", "exec"}, {"create", "", "pods", "attach"}, {"create", "", "pods", "portforward"}, {"get", "", "secrets", ""}, {"list", "", "secrets", ""}} {
		allowed, err := s.allowed(s.ctx, probe.verb, probe.group, probe.resource, probe.sub, "payments")
		if err != nil || allowed {
			t.Fatalf("unexpected RBAC grant: %s %s/%s", probe.verb, probe.resource, probe.sub)
		}
	}
	if _, err := s.sa.CoreV1().Pods("payments").Create(s.ctx, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "forbidden-create"}, Spec: podSpec(s.image)}, metav1.CreateOptions{}); !apierrors.IsForbidden(err) {
		t.Fatal("actual pod create was not Forbidden")
	}
	if _, err := s.sa.AppsV1().Deployments("payments").Patch(s.ctx, "healthy", types.MergePatchType, []byte(`{"metadata":{"labels":{"e2e-write-probe":"unexpected"}}}`), metav1.PatchOptions{}); !apierrors.IsForbidden(err) {
		t.Fatal("actual workload patch was not Forbidden")
	}
	if _, err := s.admin.CoreV1().Pods("payments").Create(s.ctx, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "delete-probe"}, Spec: podSpec(s.image)}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := s.sa.CoreV1().Pods("payments").Delete(s.ctx, "delete-probe", metav1.DeleteOptions{}); !apierrors.IsForbidden(err) {
		t.Fatal("actual pod delete was not Forbidden")
	}
	if _, err := s.sa.CoreV1().Secrets("payments").Get(s.ctx, "metadata-secret", metav1.GetOptions{}); !apierrors.IsForbidden(err) {
		t.Fatal("base role admits Secret reads")
	}
}

func (s *suite) checkSecretMetadata(t *testing.T) {
	_, err := s.admin.CoreV1().Secrets("payments").Create(s.ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "metadata-secret", Annotations: map[string]string{"fixture": secretAnnotation}}, StringData: map[string]string{"value": secretValue}}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	s.deployment(t, "secret-ref", func(p *corev1.PodSpec) {
		p.Containers[0].Env = []corev1.EnvVar{{Name: "FIXTURE_VALUE", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "metadata-secret"}, Key: "value"}}}}
	})
	s.pod(t, "app=secret-ref", ready)
	rec := s.investigate(t, domain.ResourceDeployment, "secret-ref")
	failedTool(t, rec, diagnostics.GetSecretMetadata)
	if !strings.Contains(strings.Join(report(t, rec).MissingData, " "), diagnostics.GetSecretMetadata) {
		t.Fatal("missing optional grant concealed")
	}
	raw, err := os.ReadFile(os.Getenv("E2E_REPO") + "/deployments/kubernetes/optional/secret-metadata-rbac.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, doc := range strings.Split(string(raw), "\n---\n") {
		var header struct{ Kind string }
		if err := yaml.Unmarshal([]byte(doc), &header); err != nil {
			t.Fatal(err)
		}
		switch header.Kind {
		case "Role":
			var role rbacv1.Role
			if err := yaml.Unmarshal([]byte(doc), &role); err != nil {
				t.Fatal(err)
			}
			if _, err := s.admin.RbacV1().Roles("payments").Create(s.ctx, &role, metav1.CreateOptions{}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := s.admin.RbacV1().Roles("payments").Delete(s.ctx, role.Name, metav1.DeleteOptions{}); err != nil {
					t.Error(err)
				}
			})
		case "RoleBinding":
			var binding rbacv1.RoleBinding
			if err := yaml.Unmarshal([]byte(doc), &binding); err != nil {
				t.Fatal(err)
			}
			if _, err := s.admin.RbacV1().RoleBindings("payments").Create(s.ctx, &binding, metav1.CreateOptions{}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := s.admin.RbacV1().RoleBindings("payments").Delete(s.ctx, binding.Name, metav1.DeleteOptions{}); err != nil {
					t.Error(err)
				}
			})
		default:
			t.Fatal("unexpected optional manifest kind")
		}
	}
	eventually(t, s.ctx, 30*time.Second, "optional Secret grant", func(ctx context.Context) (bool, error) { return s.allowed(ctx, "get", "", "secrets", "", "payments") })
	rec = s.investigate(t, domain.ResourceDeployment, "secret-ref")
	report(t, rec)
	for _, e := range rec.Evidence {
		if e.Source == diagnostics.GetSecretMetadata {
			var metadata map[string]any
			if err := json.Unmarshal(e.Data, &metadata); err != nil {
				t.Fatal(err)
			}
			if len(metadata) != 3 || metadata["name"] != "metadata-secret" || metadata["exists"] != true || metadata["created_at"] == nil {
				t.Fatal("unexpected Secret metadata shape")
			}
			return
		}
	}
	t.Fatal("optional metadata observation missing")
}

func (s *suite) checkNetworkPolicy(t *testing.T) {
	s.deployment(t, "net-server", nil)
	server := s.pod(t, "app=net-server", ready)
	upstream := net.JoinHostPort(server.Status.PodIP, "8080")
	for _, name := range []string{"net-client", "net-control"} {
		s.deployment(t, name, func(p *corev1.PodSpec) { p.Containers[0].Args = []string{"--upstream=" + upstream} })
	}
	source := s.pod(t, "app=net-client", ready)
	control := s.pod(t, "app=net-control", ready)
	outcome := func(pod, pattern string, since time.Time) func(context.Context) (bool, error) {
		return func(ctx context.Context) (bool, error) {
			start := metav1.NewTime(since)
			tail := int64(30)
			raw, err := s.admin.CoreV1().Pods("payments").GetLogs(pod, &corev1.PodLogOptions{SinceTime: &start, TailLines: &tail}).DoRaw(ctx)
			return err == nil && strings.Count(string(raw), pattern) >= 3, err
		}
	}
	since := time.Now()
	for _, pod := range []string{source.Name, control.Name} {
		eventually(t, s.ctx, time.Minute, "baseline TCP probe", outcome(pod, "connection succeeded", since))
	}
	selector := metav1.LabelSelector{MatchLabels: map[string]string{"app": "net-client"}}
	_, err := s.admin.NetworkingV1().NetworkPolicies("payments").Create(s.ctx, &networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: "deny-client"}, Spec: networkingv1.NetworkPolicySpec{PodSelector: selector, PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeEgress}}}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	since = time.Now()
	eventually(t, s.ctx, time.Minute, "enforced egress deny", outcome(source.Name, "i/o timeout", since))
	eventually(t, s.ctx, time.Minute, "control TCP remains available", outcome(control.Name, "connection succeeded", since))
	rec := s.investigate(t, domain.ResourcePod, source.Name)
	for _, signal := range []string{diagnostics.SignalConnectionTimeout, diagnostics.SignalEgressDenyAll} {
		requireSignal(t, rec, signal)
	}
	a := report(t, rec)
	if a.Verdict != "suspected_fault" || a.Support != "limited" || !strings.Contains(strings.ToLower(a.RootCause), "networkpolicy") {
		t.Fatal("policy causality incorrectly presented or hypothesis missing")
	}
	_, err = s.admin.NetworkingV1().NetworkPolicies("payments").Create(s.ctx, &networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: "allow-client"}, Spec: networkingv1.NetworkPolicySpec{PodSelector: selector, PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeEgress}, Egress: []networkingv1.NetworkPolicyEgressRule{{}}}}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	since = time.Now()
	eventually(t, s.ctx, time.Minute, "additive allow restores TCP", outcome(source.Name, "connection succeeded", since))
	rec = s.investigate(t, domain.ResourcePod, source.Name)
	for _, e := range rec.Evidence {
		if slices.Contains(e.Signals, diagnostics.SignalEgressDenyAll) || slices.Contains(e.Signals, diagnostics.SignalEgressRestricted) {
			t.Fatal("deny policy incorrectly overrode additive allow")
		}
	}
	if strings.Contains(strings.ToLower(report(t, rec).RootCause), "networkpolicy") {
		t.Fatal("allow-all retained policy-block headline")
	}
}
