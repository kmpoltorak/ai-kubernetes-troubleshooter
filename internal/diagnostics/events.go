package diagnostics

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/domain"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/kube"
)

const maxEvents = 50

type EventInfo struct {
	Time    time.Time `json:"time"`
	Type    string    `json:"type"`
	Reason  string    `json:"reason"`
	Object  string    `json:"object"`
	Message string    `json:"message"`
	Count   int32     `json:"count"`
}

type EventsInfo struct {
	Total    int         `json:"total"`
	Warnings int         `json:"warnings"`
	Events   []EventInfo `json:"events"`
}

// replicaSetOf matches "<deployment>-<pod-template-hash>". The hash uses
// the alphabet of k8s.io/apimachinery/pkg/util/rand.SafeEncodeString, which
// has no vowels, so "payment-worker" is not a ReplicaSet of "payment".
var replicaSetOf = regexp.MustCompile(`^(.+)-[bcdfghjklmnpqrstvwxz2456789]{1,10}$`)

// getEvents collects events whose involved object is one of in.Names (the
// target and its pods, by exact name) or a ReplicaSet of the target. The
// newest maxEvents are returned oldest first.
// ponytail: matched by name, not UID; an object recreated under the same
// name shares its predecessor's recent events.
func getEvents(ctx context.Context, c kube.Cluster, in Input) (Result, error) {
	list, err := c.Client.CoreV1().Events(in.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return Result{}, err
	}
	related := func(obj corev1.ObjectReference) bool {
		if slices.Contains(in.Names, obj.Name) {
			return true
		}
		m := replicaSetOf.FindStringSubmatch(obj.Name)
		return obj.Kind == "ReplicaSet" && m != nil && slices.Contains(in.Names, m[1])
	}
	var events []EventInfo
	for _, e := range list.Items {
		if len(in.Names) > 0 && !related(e.InvolvedObject) {
			continue
		}
		events = append(events, EventInfo{
			Time: eventTime(e), Type: e.Type, Reason: e.Reason, Count: max(e.Count, 1),
			Object:  strings.ToLower(e.InvolvedObject.Kind) + "/" + e.InvolvedObject.Name,
			Message: truncate(Redact(e.Message), maxMessageBytes),
		})
	}
	sort.SliceStable(events, func(i, j int) bool { return events[i].Time.Before(events[j].Time) })
	info := EventsInfo{Total: len(events)}
	if len(events) > maxEvents {
		events = events[len(events)-maxEvents:]
	}
	info.Events = events

	var signals signalSet
	for _, e := range events {
		if e.Type == corev1.EventTypeWarning {
			info.Warnings++
		}
		if sig := eventSignal(e.Reason, e.Message); sig != "" {
			signals.add(sig)
		}
	}
	subject := "namespace/" + in.Namespace
	if len(in.Names) > 0 {
		subject = "events/" + in.Names[0]
	}
	health := domain.Healthy
	if info.Warnings > 0 {
		health = domain.Degraded
	}
	summary := fmt.Sprintf("%d related events, %d warnings.", info.Total, info.Warnings)
	if len(signals) > 0 {
		summary += " Patterns: " + strings.Join(signals, ", ") + "."
	}
	return Result{Subject: subject, Health: health, Summary: summary, Signals: signals, Data: info}, nil
}

func eventTime(e corev1.Event) time.Time {
	switch {
	case !e.LastTimestamp.IsZero():
		return e.LastTimestamp.UTC()
	case !e.EventTime.IsZero():
		return e.EventTime.UTC()
	default:
		return e.FirstTimestamp.UTC()
	}
}

func eventSignal(reason, message string) string {
	msg := strings.ToLower(message)
	switch reason {
	case "BackOff":
		if strings.Contains(msg, "pulling image") {
			return SignalFailedPull
		}
		return SignalBackOff
	case "FailedMount", "FailedAttachVolume":
		return SignalFailedMount
	case "FailedScheduling":
		return SignalFailedScheduling
	case "ErrImagePull", "ImagePullBackOff":
		return SignalFailedPull
	case "Failed":
		if strings.Contains(msg, "pull") || strings.Contains(msg, "image") {
			return SignalFailedPull
		}
		if strings.Contains(msg, "configmap") || strings.Contains(msg, "secret") {
			return SignalContainerConfigError
		}
	case "Unhealthy":
		if strings.Contains(msg, "readiness probe") {
			return SignalReadinessProbeFailed
		}
		if strings.Contains(msg, "liveness probe") {
			return SignalLivenessProbeFailed
		}
	case "Killing":
		if strings.Contains(msg, "failed liveness probe") {
			return SignalLivenessProbeFailed
		}
	case "OOMKilling":
		return SignalOOMKilled
	case "Evicted":
		return SignalEvicted
	}
	return ""
}
