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
	"slices"
	"strconv"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/metadata"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/domain"
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
	// Dynamic reads the configured custom resources, and only those.
	Dynamic         dynamic.Interface
	CustomResources map[domain.ResourceType]schema.GroupVersionResource
}

// CustomResourceMap indexes the configured custom resources by incident kind.
func CustomResourceMap(crs []domain.CustomResource) map[domain.ResourceType]schema.GroupVersionResource {
	m := make(map[domain.ResourceType]schema.GroupVersionResource, len(crs))
	for _, cr := range crs {
		m[cr.Kind()] = schema.GroupVersionResource{Group: cr.Group, Version: cr.Version, Resource: cr.Resource}
	}
	return m
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
func Connect(kubeconfig, kubeContext string, custom []domain.CustomResource) (Cluster, error) {
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
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return Cluster{}, fmt.Errorf("dynamic client: %w", err)
	}
	return Cluster{Client: client, Metadata: meta, PodMetrics: metricsReader(client), Dynamic: dyn, CustomResources: CustomResourceMap(custom)}, nil
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
// Ping asks the API server for /version.
func (c Cluster) Ping(ctx context.Context) error {
	return rejected(c.Client.Discovery().RESTClient().Get().AbsPath("/version").Do(ctx).Error())
}

// ErrCredentialsRejected means the API server answered 401: the kubeconfig's
// token or certificate is invalid or expired, not a network problem.
var ErrCredentialsRejected = errors.New("the cluster rejected the kubeconfig credentials (expired or invalid token?)")

func rejected(err error) error {
	if apierrors.IsUnauthorized(err) {
		return fmt.Errorf("%w: %w", ErrCredentialsRejected, err)
	}
	return err
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

// Live serves the configured clusters by name. Simulation scenarios cannot be
// requested against them.
type Live map[string]Cluster

func (l Live) For(t domain.Target, scenario string) (Cluster, string, error) {
	if scenario != "" {
		return Cluster{}, "", fmt.Errorf("scenario %q requires simulation mode", scenario)
	}
	c, err := l.get(t.Cluster)
	return c, "", err
}

func (l Live) get(name string) (Cluster, error) {
	c, ok := l[name]
	if !ok {
		return Cluster{}, fmt.Errorf("cluster %q is not configured", name)
	}
	return c, nil
}

// ListNames lists names in one configured cluster; see Cluster.ListNames.
func (l Live) ListNames(ctx context.Context, cluster, namespace string, kind domain.ResourceType) ([]string, error) {
	c, err := l.get(cluster)
	if err != nil {
		return nil, err
	}
	names, err := c.ListNames(ctx, namespace, kind)
	return names, rejected(err)
}

// Ping checks every cluster in parallel; the result has one entry per
// cluster, nil when its API server answered.
func (l Live) Ping(ctx context.Context) map[string]error {
	out := make(map[string]error, len(l))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for name, c := range l {
		wg.Go(func() {
			err := c.Ping(ctx)
			mu.Lock()
			out[name] = err
			mu.Unlock()
		})
	}
	wg.Wait()
	return out
}

// Ready fails only when no cluster answers: one unreachable cluster must not
// take the whole installation out of service. It returns at the first answer,
// so a cluster that hangs does not use up the readiness timeout of the others.
func (l Live) Ready(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel() // stops the pings still waiting
	results := make(chan error, len(l))
	for name, c := range l {
		go func() {
			if err := c.Ping(ctx); err != nil {
				results <- fmt.Errorf("cluster %s: %w", name, err)
				return
			}
			results <- nil
		}()
	}
	var errs []error
	for range l {
		err := <-results
		if err == nil {
			return nil
		}
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// maxListedNames bounds the names offered for one namespace and kind.
const maxListedNames = 500

// ListNames lists object names of a supported kind for the incident form.
// It reads names only and is bounded; diagnostics never depend on it.
func (c Cluster) ListNames(ctx context.Context, namespace string, kind domain.ResourceType) ([]string, error) {
	opts := metav1.ListOptions{Limit: maxListedNames}
	var list runtime.Object
	var err error
	switch kind {
	case domain.ResourceDeployment:
		list, err = c.Client.AppsV1().Deployments(namespace).List(ctx, opts)
	case domain.ResourceStatefulSet:
		list, err = c.Client.AppsV1().StatefulSets(namespace).List(ctx, opts)
	case domain.ResourceDaemonSet:
		list, err = c.Client.AppsV1().DaemonSets(namespace).List(ctx, opts)
	case domain.ResourceJob:
		list, err = c.Client.BatchV1().Jobs(namespace).List(ctx, opts)
	case domain.ResourceCronJob:
		list, err = c.Client.BatchV1().CronJobs(namespace).List(ctx, opts)
	case domain.ResourceService:
		list, err = c.Client.CoreV1().Services(namespace).List(ctx, opts)
	case domain.ResourcePod:
		list, err = c.Client.CoreV1().Pods(namespace).List(ctx, opts)
	case domain.ResourceIngress:
		list, err = c.Client.NetworkingV1().Ingresses(namespace).List(ctx, opts)
	case domain.ResourcePVC:
		list, err = c.Client.CoreV1().PersistentVolumeClaims(namespace).List(ctx, opts)
	case domain.ResourceHPA:
		list, err = c.Client.AutoscalingV2().HorizontalPodAutoscalers(namespace).List(ctx, opts)
	default:
		gvr, ok := c.CustomResources[kind]
		if !ok || c.Dynamic == nil {
			return nil, fmt.Errorf("unsupported kind %q", kind)
		}
		list, err = c.Dynamic.Resource(gvr).Namespace(namespace).List(ctx, opts)
	}
	if err != nil {
		return nil, err
	}
	var names []string
	err = meta.EachListItem(list, func(o runtime.Object) error {
		m, err := meta.Accessor(o)
		if err == nil {
			names = append(names, m.GetName())
		}
		return err
	})
	slices.Sort(names)
	return names, err
}
