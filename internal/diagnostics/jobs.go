package diagnostics

import (
	"context"
	"fmt"
	"strings"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/domain"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/kube"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/redact"
)

// maxListedJobs bounds the Jobs read to find a CronJob's latest run.
const maxListedJobs = 500

func getJob(ctx context.Context, c kube.Cluster, in Input) (Result, error) {
	j, err := c.Client.BatchV1().Jobs(in.Namespace).Get(ctx, in.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return workloadNotFound("Job", in), nil
	}
	if err != nil {
		return Result{}, err
	}
	info, err := newWorkload("Job", j.Name, j.Spec.Selector, j.Spec.Template)
	if err != nil {
		return Result{}, err
	}
	st := j.Status
	info.DesiredReplicas = ptr.Deref(j.Spec.Completions, 1)
	info.Active, info.Succeeded, info.Failed = st.Active, st.Succeeded, st.Failed
	info.ReadyReplicas = ptr.Deref(st.Ready, 0)

	var signals signalSet
	var failed, complete, suspended *batchv1.JobCondition
	for i, cond := range st.Conditions {
		info.Conditions = append(info.Conditions, condition(string(cond.Type), string(cond.Status), cond.Reason, cond.Message))
		if cond.Status != corev1.ConditionTrue {
			continue
		}
		switch cond.Type {
		case batchv1.JobFailed:
			failed = &st.Conditions[i]
		case batchv1.JobComplete:
			complete = &st.Conditions[i]
		case batchv1.JobSuspended:
			suspended = &st.Conditions[i]
		}
	}
	info.RolloutComplete = complete != nil

	subject := "job/" + j.Name
	progress := fmt.Sprintf("%d/%d succeeded, %d active, %d failed pods", st.Succeeded, info.DesiredReplicas, st.Active, st.Failed)
	switch {
	case failed != nil:
		signals.add(SignalJobFailed)
		if failed.Reason == "DeadlineExceeded" {
			signals.add(SignalJobDeadlineExceeded)
		}
		return Result{Subject: subject, Health: domain.Down, Signals: signals, Data: info,
			Summary: fmt.Sprintf("Job %s failed (%s): %s. %s.", j.Name, failed.Reason, strings.TrimSuffix(truncate(redact.Text(failed.Message), maxMessageBytes), "."), progress)}, nil
	case complete != nil:
		return Result{Subject: subject, Health: domain.Healthy, Data: info, Summary: fmt.Sprintf("Job %s completed: %s.", j.Name, progress)}, nil
	case suspended != nil:
		signals.add(SignalJobSuspended)
		return Result{Subject: subject, Health: domain.Degraded, Signals: signals, Data: info, Summary: fmt.Sprintf("Job %s is suspended: %s.", j.Name, progress)}, nil
	case st.Failed > 0:
		// Still retrying within backoffLimit: the failed pods explain why.
		return Result{Subject: subject, Health: domain.Degraded, Signals: signals, Data: info, Summary: fmt.Sprintf("Job %s is running with pod failures: %s.", j.Name, progress)}, nil
	}
	return Result{Subject: subject, Health: domain.Healthy, Data: info, Summary: fmt.Sprintf("Job %s is running: %s.", j.Name, progress)}, nil
}

type CronJobInfo struct {
	Name               string         `json:"name"`
	Schedule           string         `json:"schedule"`
	Suspended          bool           `json:"suspended"`
	LastScheduleTime   *metav1.Time   `json:"last_schedule_time,omitempty"`
	LastSuccessfulTime *metav1.Time   `json:"last_successful_time,omitempty"`
	LatestJob          string         `json:"latest_job,omitempty"`
	Template           PodSpecSummary `json:"template"`
}

// getCronJob reads the schedule state and names the latest Job it created;
// that Job's own diagnosis explains a failed run.
func getCronJob(ctx context.Context, c kube.Cluster, in Input) (Result, error) {
	cj, err := c.Client.BatchV1().CronJobs(in.Namespace).Get(ctx, in.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return workloadNotFound("CronJob", in), nil
	}
	if err != nil {
		return Result{}, err
	}
	info := CronJobInfo{Name: cj.Name, Schedule: cj.Spec.Schedule, Suspended: ptr.Deref(cj.Spec.Suspend, false),
		LastScheduleTime: cj.Status.LastScheduleTime, LastSuccessfulTime: cj.Status.LastSuccessfulTime,
		Template: summarizeSpec(cj.Spec.JobTemplate.Spec.Template.Spec)}
	// ponytail: lists up to maxListedJobs Jobs and matches the owner; a namespace with more may miss the latest run.
	jobs, err := c.Client.BatchV1().Jobs(in.Namespace).List(ctx, metav1.ListOptions{Limit: maxListedJobs})
	if err != nil {
		return Result{}, fmt.Errorf("list jobs of cronjob: %w", err)
	}
	var latest *batchv1.Job
	for i, j := range jobs.Items {
		owned := false
		for _, o := range j.OwnerReferences {
			owned = owned || (o.Kind == "CronJob" && o.UID == cj.UID)
		}
		if owned && (latest == nil || latest.CreationTimestamp.Before(&j.CreationTimestamp)) {
			latest = &jobs.Items[i]
		}
	}
	if latest != nil {
		info.LatestJob = latest.Name
	}

	var signals signalSet
	health, summary := domain.Healthy, fmt.Sprintf("CronJob %s (%s)", cj.Name, cj.Spec.Schedule)
	switch {
	case info.Suspended:
		health = domain.Degraded
		signals.add(SignalCronJobSuspended)
		summary += " is suspended: no new runs are scheduled."
	case info.LastScheduleTime == nil:
		summary += " has not been scheduled yet."
	default:
		summary += " last ran at " + info.LastScheduleTime.UTC().Format("2006-01-02 15:04 MST") + "."
	}
	if info.LatestJob != "" {
		summary += " Latest Job: " + info.LatestJob + "."
	}
	return Result{Subject: "cronjob/" + cj.Name, Health: health, Summary: summary, Signals: signals, Data: info}, nil
}
