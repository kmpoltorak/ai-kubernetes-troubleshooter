package diagnostics

import (
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"

	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/domain"
)

var template = corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: appLabels}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "app:1"}}}}

func meta(name string) metav1.ObjectMeta {
	return metav1.ObjectMeta{Name: name, Namespace: ns, Generation: 2}
}

func TestGetStatefulSetAndDaemonSet(t *testing.T) {
	sts := &appsv1.StatefulSet{ObjectMeta: meta("db"), Spec: appsv1.StatefulSetSpec{Replicas: ptr.To[int32](3), Selector: &metav1.LabelSelector{MatchLabels: appLabels}, Template: template},
		Status: appsv1.StatefulSetStatus{ObservedGeneration: 2, Replicas: 3, ReadyReplicas: 1, UpdatedReplicas: 1, CurrentRevision: "db-1", UpdateRevision: "db-2"}}
	ds := &appsv1.DaemonSet{ObjectMeta: meta("agent"), Spec: appsv1.DaemonSetSpec{Selector: &metav1.LabelSelector{MatchLabels: appLabels}, Template: template},
		Status: appsv1.DaemonSetStatus{ObservedGeneration: 2}}
	c := cluster(t, sts, ds)

	r := run(t, c, GetStatefulSet, Input{Name: "db"})
	info := r.Data.(WorkloadInfo)
	if r.Subject != "statefulset/db" || r.Health != domain.Degraded || info.Selector != "app=payment-service" || info.RolloutComplete ||
		!strings.Contains(r.Summary, "1/3 ready replicas. Rollout is not complete.") {
		t.Fatalf("statefulset: %+v", r)
	}
	wantSignals(t, r, SignalReplicasUnavailable)

	r = run(t, c, GetDaemonSet, Input{Name: "agent"})
	if r.Health != domain.Degraded || !strings.Contains(r.Summary, "no node matches") {
		t.Fatalf("daemonset with no eligible nodes: %+v", r)
	}
	wantSignals(t, r, SignalNoEligibleNodes)

	for _, tool := range []string{GetStatefulSet, GetDaemonSet, GetJob, GetCronJob} {
		if r := run(t, c, tool, Input{Name: "missing"}); r.Health != domain.Down {
			t.Errorf("%s missing target: %+v", tool, r)
		} else {
			wantSignals(t, r, SignalWorkloadNotFound)
		}
	}
}

func job(name string, status batchv1.JobStatus) *batchv1.Job {
	return &batchv1.Job{ObjectMeta: meta(name), Spec: batchv1.JobSpec{Selector: &metav1.LabelSelector{MatchLabels: appLabels}, Template: template}, Status: status}
}

func failedCond(reason, msg string) []batchv1.JobCondition {
	return []batchv1.JobCondition{{Type: batchv1.JobFailed, Status: corev1.ConditionTrue, Reason: reason, Message: msg}}
}

func TestGetJob(t *testing.T) {
	c := cluster(t,
		job("backoff", batchv1.JobStatus{Failed: 7, Conditions: failedCond("BackoffLimitExceeded", "Job has reached the specified backoff limit")}),
		job("slow", batchv1.JobStatus{Failed: 1, Conditions: failedCond("DeadlineExceeded", "Job was active longer than specified deadline")}),
		job("done", batchv1.JobStatus{Succeeded: 1, Conditions: []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}}}),
		job("retrying", batchv1.JobStatus{Active: 1, Failed: 2}),
		job("paused", batchv1.JobStatus{Conditions: []batchv1.JobCondition{{Type: batchv1.JobSuspended, Status: corev1.ConditionTrue}}}),
	)
	r := run(t, c, GetJob, Input{Name: "backoff"})
	if r.Health != domain.Down || !strings.Contains(r.Summary, "failed (BackoffLimitExceeded): Job has reached the specified backoff limit. 0/1 succeeded, 0 active, 7 failed pods.") {
		t.Fatalf("backoff: %+v", r)
	}
	wantSignals(t, r, SignalJobFailed)
	if r := run(t, c, GetJob, Input{Name: "slow"}); r.Health != domain.Down {
		t.Fatalf("deadline: %+v", r)
	} else {
		wantSignals(t, r, SignalJobFailed, SignalJobDeadlineExceeded)
	}
	if r := run(t, c, GetJob, Input{Name: "done"}); r.Health != domain.Healthy || len(r.Signals) != 0 || !r.Data.(WorkloadInfo).RolloutComplete {
		t.Fatalf("completed job: %+v", r)
	}
	if r := run(t, c, GetJob, Input{Name: "retrying"}); r.Health != domain.Degraded || !strings.Contains(r.Summary, "with pod failures") {
		t.Fatalf("retrying job: %+v", r)
	}
	if r := run(t, c, GetJob, Input{Name: "paused"}); r.Health != domain.Degraded {
		t.Fatalf("suspended job: %+v", r)
	} else {
		wantSignals(t, r, SignalJobSuspended)
	}
}

func TestGetCronJobFindsItsLatestRun(t *testing.T) {
	cron := &batchv1.CronJob{ObjectMeta: meta("nightly"), Spec: batchv1.CronJobSpec{Schedule: "0 2 * * *", Suspend: ptr.To(true),
		JobTemplate: batchv1.JobTemplateSpec{Spec: batchv1.JobSpec{Template: template}}}}
	cron.UID = "cron-uid"
	run1, run2, other := job("nightly-1", batchv1.JobStatus{}), job("nightly-2", batchv1.JobStatus{}), job("nightly-manual", batchv1.JobStatus{})
	for i, j := range []*batchv1.Job{run1, run2, other} {
		j.CreationTimestamp = metav1.NewTime(time.Date(2026, 9, 1, i, 0, 0, 0, time.UTC))
		uid := types.UID("cron-uid")
		if j == other {
			uid = "someone-else" // newest, but not created by this CronJob
		}
		j.OwnerReferences = []metav1.OwnerReference{{Kind: "CronJob", Name: "nightly", UID: uid}}
	}
	r := run(t, cluster(t, cron, run1, run2, other), GetCronJob, Input{Name: "nightly"})
	info := r.Data.(CronJobInfo)
	if info.LatestJob != "nightly-2" || r.Health != domain.Degraded || !strings.Contains(r.Summary, "is suspended") {
		t.Fatalf("cronjob: %+v", r)
	}
	wantSignals(t, r, SignalCronJobSuspended)
}

func TestCompletedJobPodsAreDone(t *testing.T) {
	done := pod("migrate-abc", func(p *corev1.Pod) {
		p.Status.Phase = corev1.PodSucceeded
		p.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionFalse, Reason: "PodCompleted"}}
		p.Status.ContainerStatuses[0].Ready = false
		p.Status.ContainerStatuses[0].State = corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "Completed"}}
	})
	r := run(t, cluster(t, done), GetPods, Input{LabelSelector: "app=payment-service"})
	if r.Health != domain.Healthy || r.Summary != "0/1 pods ready, 1 completed." {
		t.Fatalf("a finished Job pod is not unready: %+v", r)
	}
}
