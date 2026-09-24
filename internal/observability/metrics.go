package observability

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Metrics are registered on the default Prometheus registry, which is what
// promhttp.Handler serves.
var (
	HTTPRequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "http_requests_total", Help: "HTTP requests by method, route pattern and status code.",
	}, []string{"method", "route", "status"})

	HTTPRequestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name: "http_request_duration_seconds", Help: "HTTP request latency.", Buckets: prometheus.DefBuckets,
	}, []string{"method", "route"})

	IncidentsTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "incidents_total", Help: "Incidents created.",
	})

	InvestigationsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "investigations_total", Help: "Finished investigations by final status.",
	}, []string{"status"})

	InvestigationDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name: "investigation_duration_seconds", Help: "End-to-end investigation duration.",
		Buckets: []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.5, 1, 2.5, 5, 10, 30, 60, 120},
	})

	ToolExecutionsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "kubernetes_tool_executions_total", Help: "Kubernetes diagnostic tool runs by tool and observed health.",
	}, []string{"tool", "health"})

	ToolFailuresTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "kubernetes_tool_failures_total", Help: "Kubernetes diagnostic tool runs that could not complete.",
	}, []string{"tool"})

	KubernetesAPIRequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "kubernetes_api_requests_total", Help: "Requests to the Kubernetes API server by HTTP method and status code.",
	}, []string{"method", "code"})

	LLMRequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "llm_requests_total", Help: "LLM analysis requests by provider and outcome.",
	}, []string{"provider", "status"})

	LLMRequestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name: "llm_request_duration_seconds", Help: "LLM analysis request latency.",
		// Low buckets cover the in-process rules provider; high ones cover local LLMs.
		Buckets: []float64{0.001, 0.01, 0.1, 0.5, 1, 2.5, 5, 10, 30, 60, 120},
	}, []string{"provider"})

	LLMFailuresTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "llm_failures_total", Help: "LLM failures by provider and reason (request, invalid_output).",
	}, []string{"provider", "reason"})
)
