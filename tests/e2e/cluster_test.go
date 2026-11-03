//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/domain"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/redact"
)

const secretValue = "SYNTHETIC_SECRET_VALUE_DO_NOT_RECORD"
const secretAnnotation = "SYNTHETIC_SECRET_ANNOTATION_DO_NOT_RECORD"

type suite struct {
	ctx    context.Context
	admin  kubernetes.Interface
	sa     kubernetes.Interface
	apiURL string
	http   *http.Client
	image  string
}

// An explicit loopback kind context is required even for diagnostic collection.
func clusterClient(t *testing.T) (*rest.Config, kubernetes.Interface) {
	t.Helper()
	path, name, contextName := os.Getenv("E2E_KUBECONFIG"), os.Getenv("E2E_CLUSTER_NAME"), os.Getenv("E2E_CONTEXT")
	if path == "" || !strings.HasPrefix(name, "akt-e2e-") || contextName != "kind-"+name {
		t.Fatal("use scripts/test-e2e.sh with its own kind context")
	}
	raw, err := clientcmd.LoadFromFile(path)
	if err != nil {
		t.Fatal("cannot load owned kubeconfig")
	}
	cfg, err := clientcmd.NewNonInteractiveClientConfig(*raw, contextName, &clientcmd.ConfigOverrides{}, nil).ClientConfig()
	if err != nil {
		t.Fatal("cannot select owned kind context")
	}
	u, err := url.Parse(cfg.Host)
	if err != nil || u.Scheme != "https" || !net.ParseIP(u.Hostname()).IsLoopback() {
		t.Fatal("kind API must be HTTPS on loopback")
	}
	cfg.Timeout, cfg.UserAgent = 10*time.Second, "akt-e2e-admin"
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	nodes, err := client.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil || len(nodes.Items) != 1 || nodes.Items[0].Name != name+"-control-plane" {
		t.Fatal("API is not the runner's single-node kind cluster")
	}
	return cfg, client
}

func eventually(t *testing.T, ctx context.Context, duration time.Duration, description string, check func(context.Context) (bool, error)) {
	t.Helper()
	ctx, cancel := context.WithTimeout(ctx, duration)
	defer cancel()
	var last error
	for {
		ok, err := check(ctx)
		if ok && err == nil {
			return
		}
		last = err
		select {
		case <-ctx.Done():
			t.Fatalf("%s: deadline exceeded, last error: %v", description, redact.Error(last))
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func (s *suite) call(t *testing.T, ctx context.Context, method, path string, body, out any) int {
	t.Helper()
	var data []byte
	var err error
	if body != nil {
		data, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	// Keep below the unmodified deployment's per-IP rate limit.
	select {
	case <-ctx.Done():
		t.Fatal("suite canceled")
	case <-time.After(250 * time.Millisecond):
	}
	req, err := http.NewRequestWithContext(ctx, method, s.apiURL+path, bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if method == "POST" && strings.HasSuffix(path, "/jobs") {
		req.Header.Set("Idempotency-Key", domain.NewID())
	}
	resp, err := s.http.Do(req)
	if err != nil {
		t.Fatal(redact.Error(err))
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), secretValue) || strings.Contains(string(raw), secretAnnotation) || strings.Contains(string(raw), base64.StdEncoding.EncodeToString([]byte(secretValue))) {
		t.Fatal("Secret value or annotations leaked in API response")
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			t.Fatalf("invalid JSON from %s: %v", path, err)
		}
	}
	return resp.StatusCode
}

func (s *suite) investigate(t *testing.T, kind domain.ResourceType, name string) domain.InvestigationRecord {
	t.Helper()
	var inc domain.Incident
	body := map[string]string{"cluster": "local", "namespace": "payments", "resource_type": string(kind), "resource_name": name, "description": "Live cluster fixture"}
	if code := s.call(t, s.ctx, "POST", "/api/v1/incidents", body, &inc); code != 201 || !domain.IsUUID(inc.ID) {
		t.Fatalf("create incident: HTTP %d", code)
	}
	path := "/api/v1/incidents/" + inc.ID
	var job domain.JobRecord
	if code := s.call(t, s.ctx, "POST", path+"/jobs", map[string]string{}, &job); code != 202 || job.Job.ID == "" {
		t.Fatalf("enqueue: HTTP %d", code)
	}
	jobPath := path + "/jobs/" + job.Job.ID
	eventually(t, s.ctx, 2*time.Minute, "terminal durable job", func(ctx context.Context) (bool, error) {
		if code := s.call(t, ctx, "GET", jobPath, nil, &job); code != 200 {
			return false, fmt.Errorf("job HTTP %d", code)
		}
		return job.Job.Status == domain.InvestigationCompleted || job.Job.Status == domain.InvestigationFailed, nil
	})
	var page struct {
		Investigations []domain.Investigation `json:"investigations"`
	}
	if code := s.call(t, s.ctx, "GET", path+"/investigations", nil, &page); code != 200 || len(page.Investigations) == 0 || page.Investigations[0].ID != job.Result.Investigation.ID {
		t.Fatal("durable attempt missing from history")
	}
	var rec domain.InvestigationRecord
	if code := s.call(t, s.ctx, "GET", path+"/investigations/"+job.Result.Investigation.ID, nil, &rec); code != 200 || rec.Investigation.Status != job.Job.Status || len(rec.Evidence) == 0 {
		t.Fatal("attempt or saved evidence missing")
	}
	if rec.Report != nil {
		var latest domain.InvestigationRecord
		if code := s.call(t, s.ctx, "GET", path+"/report", nil, &latest); code != 200 || latest.Investigation.ID != rec.Investigation.ID {
			t.Fatal("completed report is not latest attempt")
		}
		var failed []domain.ToolExecution
		for _, tool := range rec.ToolExecutions {
			if tool.Status == domain.ToolFailed {
				failed = append(failed, tool)
			}
		}
		if err := rec.Report.Analysis.Validate(rec.Evidence, failed); err != nil {
			t.Fatalf("v2 or exact citations invalid: %v", err)
		}
		if len(rec.Report.Analysis.Limitations) == 0 {
			t.Fatal("report sampling limits absent")
		}
		t.Logf("assessment %s/%s: %s", rec.Report.Analysis.Verdict, rec.Report.Analysis.Support, rec.Report.Analysis.RootCause)
		for _, e := range rec.Evidence {
			if e.Health != domain.Healthy {
				t.Logf("observation %s: %s", e.Source, e.Summary)
			}
		}
	} else if code := s.call(t, s.ctx, "GET", path+"/report", nil, nil); code != 404 {
		t.Fatal("failed first attempt fabricated a report")
	}
	return rec
}

func report(t *testing.T, rec domain.InvestigationRecord) domain.Analysis {
	t.Helper()
	if rec.Investigation.Status != domain.InvestigationCompleted || rec.Report == nil {
		t.Fatalf("analysis failed: %s", redact.Text(rec.Investigation.Error))
	}
	return rec.Report.Analysis
}

func requireSignal(t *testing.T, rec domain.InvestigationRecord, signal string) {
	t.Helper()
	for _, e := range rec.Evidence {
		if slices.Contains(e.Signals, signal) {
			return
		}
	}
	t.Fatalf("expected diagnostic signal %s", signal)
}

func failedTool(t *testing.T, rec domain.InvestigationRecord, name string) {
	t.Helper()
	for _, tool := range rec.ToolExecutions {
		if tool.ToolName == name && tool.Status == domain.ToolFailed {
			return
		}
	}
	t.Fatalf("expected failed diagnostic %s", name)
}

func podSpec(image string) corev1.PodSpec {
	uid, yes, no := int64(65532), true, false
	return corev1.PodSpec{
		AutomountServiceAccountToken: &no,
		SecurityContext:              &corev1.PodSecurityContext{RunAsNonRoot: &yes, RunAsUser: &uid, SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}},
		Containers: []corev1.Container{{Name: "fixture", Image: image, ImagePullPolicy: corev1.PullNever,
			SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: &no, ReadOnlyRootFilesystem: &yes, Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}},
			Ports:           []corev1.ContainerPort{{ContainerPort: 8080}},
			ReadinessProbe:  &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/ready", Port: intstr.FromInt32(8080)}}, InitialDelaySeconds: 3, PeriodSeconds: 1, TimeoutSeconds: 2, FailureThreshold: 1},
		}},
	}
}

func (s *suite) deployment(t *testing.T, name string, modify func(*corev1.PodSpec)) {
	t.Helper()
	labels := map[string]string{"app": name}
	spec := podSpec(s.image)
	if modify != nil {
		modify(&spec)
	}
	replicas := int32(1)
	_, err := s.admin.AppsV1().Deployments("payments").Create(s.ctx, &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: name}, Spec: appsv1.DeploymentSpec{Replicas: &replicas, Selector: &metav1.LabelSelector{MatchLabels: labels}, Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels}, Spec: spec}}}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
}

func (s *suite) pod(t *testing.T, selector string, predicate func(corev1.Pod) bool) corev1.Pod {
	t.Helper()
	var found corev1.Pod
	eventually(t, s.ctx, 3*time.Minute, "pod state for "+selector, func(ctx context.Context) (bool, error) {
		pods, err := s.admin.CoreV1().Pods("payments").List(ctx, metav1.ListOptions{LabelSelector: selector})
		if err != nil {
			return false, err
		}
		for _, pod := range pods.Items {
			if predicate(pod) {
				found = pod
				return true, nil
			}
		}
		return false, fmt.Errorf("no pod reached required state (%d observed)", len(pods.Items))
	})
	return found
}

func ready(p corev1.Pod) bool {
	return p.Status.Phase == corev1.PodRunning && slices.ContainsFunc(p.Status.Conditions, func(c corev1.PodCondition) bool { return c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue })
}

func waiting(p corev1.Pod, reason string) bool {
	return slices.ContainsFunc(p.Status.ContainerStatuses, func(c corev1.ContainerStatus) bool { return c.State.Waiting != nil && c.State.Waiting.Reason == reason })
}

func TestCollectDiagnostics(t *testing.T) {
	_, client := clusterClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	pods, err := client.CoreV1().Pods("").List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	clean := func(text string) string {
		return redact.Text(strings.ReplaceAll(strings.ReplaceAll(text, secretValue, "[REDACTED]"), secretAnnotation, "[REDACTED]"))
	}
	for _, pod := range pods.Items {
		t.Logf("pod %s/%s phase=%s ready=%t", pod.Namespace, pod.Name, pod.Status.Phase, ready(pod))
		for _, c := range pod.Status.ContainerStatuses {
			if c.State.Waiting != nil {
				t.Logf("waiting=%s", clean(c.State.Waiting.Reason))
			}
		}
		if pod.Namespace == "troubleshooter" && pod.Labels["app"] == "troubleshooter" || pod.Namespace == "payments" && (pod.Labels["app"] == "net-client" || pod.Labels["app"] == "net-control") {
			tail, maxBytes := int64(80), int64(32768)
			raw, err := client.CoreV1().Pods(pod.Namespace).GetLogs(pod.Name, &corev1.PodLogOptions{TailLines: &tail, LimitBytes: &maxBytes}).DoRaw(ctx)
			if err != nil {
				t.Logf("app logs unavailable: %v", redact.Error(err))
			} else {
				t.Log(clean(string(raw)))
			}
		}
	}
	events, err := client.CoreV1().Events("").List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Logf("events unavailable: %v", redact.Error(err))
		return
	}
	for _, event := range events.Items {
		t.Logf("event %s/%s %s: %s", event.Namespace, event.InvolvedObject.Name, event.Reason, clean(event.Message))
	}
}
