package diagnostics

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/domain"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/kube"
)

// ConfigMapInfo reports presence and key names only; values can be large
// and are unrelated to most incidents.
type ConfigMapInfo struct {
	Name   string         `json:"name"`
	Exists bool           `json:"exists"`
	Keys   []string       `json:"keys,omitempty"`
	Sizes  map[string]int `json:"sizes_bytes,omitempty"`
}

func getConfigMap(ctx context.Context, c kube.Cluster, in Input) (Result, error) {
	if in.Name == "" {
		return Result{}, errors.New("name is required")
	}
	subject := "configmap/" + in.Name
	cm, err := c.Client.CoreV1().ConfigMaps(in.Namespace).Get(ctx, in.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return Result{Subject: subject, Health: domain.Down, Summary: fmt.Sprintf("Referenced ConfigMap %s does not exist.", in.Name),
			Signals: []string{SignalConfigMapMissing}, Data: ConfigMapInfo{Name: in.Name}}, nil
	}
	if err != nil {
		return Result{}, err
	}
	info := ConfigMapInfo{Name: cm.Name, Exists: true, Sizes: map[string]int{}}
	for k, v := range cm.Data {
		info.Keys = append(info.Keys, k)
		info.Sizes[k] = len(v)
	}
	for k, v := range cm.BinaryData {
		info.Keys = append(info.Keys, k)
		info.Sizes[k] = len(v)
	}
	sort.Strings(info.Keys)
	return Result{Subject: subject, Health: domain.Healthy,
		Summary: fmt.Sprintf("ConfigMap %s exists with %d keys.", cm.Name, len(info.Keys)), Data: info}, nil
}

// SecretMetadata never contains values, types or annotations: the
// last-applied-configuration annotation can embed the whole Secret.
type SecretMetadata struct {
	Name      string     `json:"name"`
	Exists    bool       `json:"exists"`
	CreatedAt *time.Time `json:"created_at,omitempty"`
}

// getSecretMetadata checks a referenced Secret exists using the metadata
// API (PartialObjectMetadata), so values are never transferred.
func getSecretMetadata(ctx context.Context, c kube.Cluster, in Input) (Result, error) {
	if in.Name == "" {
		return Result{}, errors.New("name is required")
	}
	subject := "secret/" + in.Name
	m, err := c.Metadata.Resource(kube.SecretsResource).Namespace(in.Namespace).Get(ctx, in.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return Result{Subject: subject, Health: domain.Down, Summary: fmt.Sprintf("Referenced Secret %s does not exist.", in.Name),
			Signals: []string{SignalSecretMissing}, Data: SecretMetadata{Name: in.Name}}, nil
	}
	if err != nil {
		return Result{}, err
	}
	created := m.CreationTimestamp.UTC()
	return Result{Subject: subject, Health: domain.Healthy, Summary: fmt.Sprintf("Secret %s exists.", m.Name),
		Data: SecretMetadata{Name: m.Name, Exists: true, CreatedAt: &created}}, nil
}
