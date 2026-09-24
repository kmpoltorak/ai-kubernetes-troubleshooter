// Package kube connects to the Kubernetes API with read-only intent. It
// hands the rest of the application a Cluster handle that is backed either
// by client-go or, in simulation mode, by fake clients.
package kube

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/metadata"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/observability"
)

// ErrMetricsUnavailable means the metrics.k8s.io API is not served
// (metrics-server missing) or not permitted.
var ErrMetricsUnavailable = errors.New("metrics API unavailable")

// SecretsResource is the only resource read through the metadata client.
var SecretsResource = schema.GroupVersionResource{Version: "v1", Resource: "secrets"}

// Cluster is everything the diagnostic tools may use. There are no write
// paths: tools only call Get, List and GetLogs.
type Cluster struct {
	Client kubernetes.Interface
	// Metadata reads PartialObjectMetadata, so Secret values never reach
	// this process.
	Metadata metadata.Interface
	// PodMetrics reads metrics.k8s.io; the fake clientset cannot serve it.
	PodMetrics func(ctx context.Context, namespace string) ([]PodUsage, error)
}

type PodUsage struct {
	Pod        string           `json:"pod"`
	Containers []ContainerUsage `json:"containers"`
}

type ContainerUsage struct {
	Name        string `json:"name"`
	CPUMilli    int64  `json:"cpu_millicores"`
	MemoryBytes int64  `json:"memory_bytes"`
}

// Connect builds a Cluster from a kubeconfig file and context, or from the
// in-cluster ServiceAccount when no kubeconfig is set and the process runs
// in a pod. Every API request is counted by method and status code.
func Connect(kubeconfig, kubeContext string) (Cluster, error) {
	cfg, err := restConfig(kubeconfig, kubeContext)
	if err != nil {
		return Cluster{}, err
	}
	cfg.UserAgent = "ai-kubernetes-troubleshooter"
	cfg.Timeout = 30 * time.Second
	cfg.Wrap(countRequests)

	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return Cluster{}, fmt.Errorf("kubernetes client: %w", err)
	}
	meta, err := metadata.NewForConfig(cfg)
	if err != nil {
		return Cluster{}, fmt.Errorf("metadata client: %w", err)
	}
	return Cluster{Client: client, Metadata: meta, PodMetrics: metricsReader(client)}, nil
}

func restConfig(kubeconfig, kubeContext string) (*rest.Config, error) {
	if kubeconfig == "" && kubeContext == "" && os.Getenv("KUBERNETES_SERVICE_HOST") != "" {
		cfg, err := rest.InClusterConfig()
		if err != nil {
			return nil, fmt.Errorf("in-cluster config: %w", err)
		}
		return cfg, nil
	}
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if kubeconfig != "" {
		rules.ExplicitPath = kubeconfig
	}
	overrides := &clientcmd.ConfigOverrides{CurrentContext: kubeContext}
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, overrides).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("load kubeconfig: %w", err)
	}
	return cfg, nil
}

// Ping checks the API server answers. /version is readable by any
// authenticated identity, so it needs no extra RBAC.
func (c Cluster) Ping(ctx context.Context) error {
	return c.Client.Discovery().RESTClient().Get().AbsPath("/version").Do(ctx).Error()
}

func metricsReader(client kubernetes.Interface) func(context.Context, string) ([]PodUsage, error) {
	return func(ctx context.Context, namespace string) ([]PodUsage, error) {
		raw, err := client.Discovery().RESTClient().Get().
			AbsPath("/apis/metrics.k8s.io/v1beta1/namespaces", namespace, "pods").DoRaw(ctx)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrMetricsUnavailable, err)
		}
		return parsePodMetrics(raw)
	}
}

// parsePodMetrics decodes a metrics.k8s.io PodMetricsList without importing
// the metrics client module.
func parsePodMetrics(raw []byte) ([]PodUsage, error) {
	var list struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Containers []struct {
				Name  string `json:"name"`
				Usage struct {
					CPU    resource.Quantity `json:"cpu"`
					Memory resource.Quantity `json:"memory"`
				} `json:"usage"`
			} `json:"containers"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("decode pod metrics: %w", err)
	}
	out := make([]PodUsage, 0, len(list.Items))
	for _, it := range list.Items {
		u := PodUsage{Pod: it.Metadata.Name}
		for _, c := range it.Containers {
			u.Containers = append(u.Containers, ContainerUsage{
				Name: c.Name, CPUMilli: c.Usage.CPU.MilliValue(), MemoryBytes: c.Usage.Memory.Value(),
			})
		}
		out = append(out, u)
	}
	return out, nil
}

// countRequests records kubernetes_api_requests_total. Paths are not used as
// labels because they contain object names.
func countRequests(next http.RoundTripper) http.RoundTripper {
	return roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		resp, err := next.RoundTrip(r)
		code := "error"
		if err == nil {
			code = strconv.Itoa(resp.StatusCode)
		}
		observability.KubernetesAPIRequestsTotal.WithLabelValues(r.Method, code).Inc()
		return resp, err
	})
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
