// Package api exposes the REST API over the standard library router.
package api

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/buildinfo"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/config"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/domain"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/incidents"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/investigation"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/jobs"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/observability"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/redact"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/simulation"
)

const maxBodyBytes = 1 << 20

type JobService interface {
	Submit(context.Context, string, string, investigation.Options) (domain.JobRecord, error)
	Get(context.Context, string, string) (domain.JobRecord, error)
	Wait(context.Context, string, string) (domain.JobRecord, error)
}

type handler struct {
	incidents *incidents.Service
	jobs      JobService
	checks    map[string]func(context.Context) error
	log       *slog.Logger
	build     buildinfo.Info
}

// Runtime is what the UI needs to know about this installation. It is served
// behind authentication and never holds URLs, keys, contexts or credentials.
type Runtime struct {
	Mode string `json:"mode"`
	// ClusterName and AllowedNamespaces describe the first cluster, for 1.x clients.
	ClusterName       string           `json:"cluster_name"`
	AllowedNamespaces []string         `json:"allowed_namespaces"`
	Clusters          []RuntimeCluster `json:"clusters"`
	Analyzer          Analyzer         `json:"analyzer"`
	Limits            Limits           `json:"limits"`
	Authentication    bool             `json:"authentication"`
	Scenarios         []string         `json:"scenarios"`
	// ResourceTypes are the kinds an incident can target, in form order.
	ResourceTypes []domain.ResourceType `json:"resource_types"`
}

// RuntimeCluster is a configured cluster as the UI sees it: no kubeconfig or context.
type RuntimeCluster struct {
	Name              string   `json:"name"`
	AllowedNamespaces []string `json:"allowed_namespaces"`
}

// Analyzer is the configured analysis: no keys or endpoint URLs.
type Analyzer struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	// The rest applies to API providers only; ReasoningEffort is "off" when no
	// effort parameter is sent.
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
	MaxOutputTokens int    `json:"max_output_tokens,omitempty"`
	TimeoutSeconds  int    `json:"timeout_seconds,omitempty"`
}

// Limits are the job and request bounds of this installation.
type Limits struct {
	InvestigationTimeoutSeconds int     `json:"investigation_timeout_seconds"`
	JobTimeoutSeconds           int     `json:"job_timeout_seconds"`
	JobQueueCapacity            int     `json:"job_queue_capacity"`
	MaxConcurrentInvestigations int     `json:"max_concurrent_investigations"`
	RateLimitRPS                float64 `json:"rate_limit_rps"`
}

// RuntimeFrom selects the UI-relevant settings; empty namespaces mean all readable ones.
func RuntimeFrom(cfg config.Config) Runtime {
	rt := Runtime{Mode: "live", Clusters: []RuntimeCluster{}, AllowedNamespaces: []string{},
		Analyzer: Analyzer{Provider: cfg.LLMProvider, Model: cfg.LLMModel},
		Limits: Limits{InvestigationTimeoutSeconds: int(cfg.InvestigationTimeout.Seconds()), JobTimeoutSeconds: int(cfg.JobTimeout.Seconds()),
			JobQueueCapacity: cfg.JobQueueCapacity, MaxConcurrentInvestigations: cfg.MaxConcurrentInvestigations, RateLimitRPS: cfg.RateLimitRPS},
		Authentication: cfg.Auth.Enabled, Scenarios: []string{}, ResourceTypes: append(slices.Clone(domain.ResourceTypes), cfg.CustomKinds()...)}
	for _, c := range cfg.Clusters {
		rt.Clusters = append(rt.Clusters, RuntimeCluster{Name: c.Name, AllowedNamespaces: append([]string{}, c.AllowedNamespaces...)})
	}
	if len(rt.Clusters) > 0 {
		rt.ClusterName, rt.AllowedNamespaces = rt.Clusters[0].Name, rt.Clusters[0].AllowedNamespaces
	}
	if cfg.LLMProvider != "rules" {
		rt.Analyzer.ReasoningEffort = cmp.Or(cfg.LLMReasoningEffort, "off")
		rt.Analyzer.MaxOutputTokens, rt.Analyzer.TimeoutSeconds = cfg.LLMMaxOutputTokens, int(cfg.LLMTimeout.Seconds())
	}
	if cfg.SimulationEnabled {
		rt.Mode, rt.Scenarios = "simulation", simulation.Scenarios
	}
	return rt
}

// Policy is the target policy the runtime describes, shared by incidents and the form.
func (r Runtime) Policy() domain.TargetPolicy {
	clusters := make(map[string][]string, len(r.Clusters))
	for _, c := range r.Clusters {
		clusters[c.Name] = c.AllowedNamespaces
	}
	return domain.TargetPolicy{Clusters: clusters, CustomResources: r.CustomKinds()}
}

// CustomKinds are the configured custom resources among the form's kinds.
func (r Runtime) CustomKinds() []domain.ResourceType {
	return slices.DeleteFunc(slices.Clone(r.ResourceTypes), func(k domain.ResourceType) bool { return !k.Custom() })
}

// Clusters is the live cluster access the API needs besides investigations
// (kube.Live); nil in simulation, which lists no names and pings nothing.
type Clusters interface {
	ListNames(ctx context.Context, cluster, namespace string, kind domain.ResourceType) ([]string, error)
	Ping(ctx context.Context) map[string]error
}

// New returns the fully wired HTTP handler. checks are the readiness checks
// by dependency name (database, kubernetes). API routes are rate limited per
// client IP at rps requests per second; auth also limits other non-probe routes.
func New(svc *incidents.Service, jobService JobService, checks map[string]func(context.Context) error, log *slog.Logger, rps float64, auth config.Auth, runtime Runtime, live Clusters) http.Handler {
	h := &handler{incidents: svc, jobs: jobService, checks: checks, log: log, build: buildinfo.Current()}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/incidents", h.createIncident)
	mux.HandleFunc("POST /api/v1/incidents/delete", h.deleteIncidents)
	mux.HandleFunc("GET /api/v1/incidents", h.listIncidents)
	mux.HandleFunc("GET /api/v1/incidents/{id}", h.getIncident)
	mux.HandleFunc("POST /api/v1/incidents/{id}/investigate", h.investigate)
	mux.HandleFunc("GET /api/v1/incidents/{id}/report", h.getReport)
	mux.HandleFunc("GET /api/v1/incidents/{id}/investigations", h.listInvestigations)
	mux.HandleFunc("POST /api/v1/incidents/{id}/jobs", h.submitJob)
	mux.HandleFunc("GET /api/v1/incidents/{id}/jobs/{jobID}", h.getJob)
	mux.HandleFunc("GET /api/v1/incidents/{id}/investigations/{investigationID}", h.getInvestigation)
	mux.HandleFunc("GET /api/v1/runtime", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, http.StatusOK, runtime) })
	mux.HandleFunc("GET /api/v1/targets", h.listTargets(runtime, live))
	mux.HandleFunc("GET /api/v1/clusters", h.clusterStatus(runtime, live))
	mux.HandleFunc("GET /health", h.health)
	mux.HandleFunc("GET /ready", h.readiness)
	mux.Handle("GET /metrics", promhttp.Handler())
	registerDocs(mux)
	registerUI(mux)
	return recoverPanics(log, securityHeaders(withRequestIDs(rateLimit(rps, auth.Enabled, authenticate(auth, log, observe(log, mux))))))
}

func (h *handler) createIncident(w http.ResponseWriter, r *http.Request) {
	var in incidents.CreateInput
	if !decodeBody(w, r, &in, false) {
		return
	}
	inc, err := h.incidents.Create(r.Context(), in)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/v1/incidents/"+inc.ID)
	writeJSON(w, http.StatusCreated, inc)
}

func (h *handler) listIncidents(w http.ResponseWriter, r *http.Request) {
	limit, err1 := queryInt(r, "limit", 0)
	offset, err2 := queryInt(r, "offset", 0)
	if err := errors.Join(err1, err2); err != nil {
		writeError(w, r, http.StatusBadRequest, "validation_failed", err.Error())
		return
	}
	list, err := h.incidents.List(r.Context(), limit, offset)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if list == nil {
		list = []domain.Incident{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"incidents": list, "offset": offset})
}

// deleteIncidents removes incidents in bulk (one ID for a single delete). There
// is no undo; the server log records who deleted which incidents.
func (h *handler) deleteIncidents(w http.ResponseWriter, r *http.Request) {
	var in struct {
		IDs []string `json:"ids"`
	}
	if !decodeBody(w, r, &in, false) {
		return
	}
	deleted, skipped, err := h.incidents.Delete(r.Context(), in.IDs)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.log.InfoContext(r.Context(), "incidents deleted", "deleted", deleted, "skipped", skipped)
	writeJSON(w, http.StatusOK, map[string][]string{"deleted": deleted, "skipped": skipped})
}

func (h *handler) getIncident(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	inc, err := h.incidents.Get(r.Context(), id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, inc)
}

func (h *handler) investigate(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	var opts investigation.Options
	if !decodeBody(w, r, &opts, true) {
		return
	}

	key := r.Header.Get("Idempotency-Key")
	if key == "" {
		key = domain.NewID()
	}
	accepted, err := h.jobs.Submit(r.Context(), id, key, opts)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	w.Header().Set("Location", jobLocation(id, accepted.Job.ID))
	finished, err := h.jobs.Wait(r.Context(), id, accepted.Job.ID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if finished.Job.Status == domain.InvestigationFailed {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": errorBody{Code: "analysis_failed", Message: finished.Job.Error}, "request_id": observability.RequestID(r.Context()), "result": finished.Result})
		return
	}
	writeJSON(w, http.StatusCreated, finished.Result)
}

func (h *handler) getReport(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	rec, err := h.incidents.Report(r.Context(), id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, rec)
}

// listTargets offers names for the incident form within the same target policy as incidents.
// The cluster may be omitted when only one is configured.
func (h *handler) listTargets(runtime Runtime, live Clusters) http.HandlerFunc {
	policy := runtime.Policy()
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		cluster := q.Get("cluster")
		if cluster == "" && len(runtime.Clusters) == 1 {
			cluster = runtime.Clusters[0].Name
		}
		// The name is a placeholder: only cluster, namespace and kind come from the request.
		t := domain.Target{Cluster: cluster, Namespace: q.Get("namespace"),
			ResourceType: domain.ResourceType(q.Get("kind")), ResourceName: "placeholder"}
		if err := errors.Join(t.Validate(), policy.Check(t)); err != nil {
			writeError(w, r, http.StatusBadRequest, "validation_failed", err.Error())
			return
		}
		names := []string{}
		if live != nil {
			ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
			defer cancel()
			found, err := live.ListNames(ctx, t.Cluster, t.Namespace, t.ResourceType)
			if err != nil {
				h.log.WarnContext(r.Context(), "listing targets failed", "cluster", t.Cluster, "namespace", t.Namespace, "kind", t.ResourceType, "error", redact.Text(err.Error()))
				writeError(w, r, http.StatusBadGateway, "list_failed", redact.Text(err.Error()))
				return
			}
			names = append(names, found...)
		}
		writeJSON(w, http.StatusOK, map[string][]string{"names": names})
	}
}

type clusterState struct {
	Name string `json:"name"`
	// Status is ok, unreachable or simulated. Errors are logged, not returned:
	// they carry API server addresses.
	Status string `json:"status"`
}

// clusterStatus pings every configured cluster for the UI's cluster list.
// ponytail: pings on every request (each open tab every 30 s, bounded by the rate
// limit); cache the result if many tabs or clusters make that noticeable.
func (h *handler) clusterStatus(runtime Runtime, live Clusters) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var pings map[string]error
		if live != nil {
			ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
			defer cancel()
			pings = live.Ping(ctx)
		}
		out := make([]clusterState, 0, len(runtime.Clusters))
		for _, c := range runtime.Clusters {
			state := clusterState{Name: c.Name, Status: "simulated"}
			if live != nil {
				state.Status = "ok"
				err, pinged := pings[c.Name]
				if !pinged {
					err = errors.New("not checked")
				}
				if err != nil {
					h.log.WarnContext(r.Context(), "cluster unreachable", "cluster", c.Name, "error", redact.Text(err.Error()))
					state.Status = "unreachable"
				}
			}
			out = append(out, state)
		}
		writeJSON(w, http.StatusOK, map[string][]clusterState{"clusters": out})
	}
}

func (h *handler) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "build": h.build})
}

func (h *handler) readiness(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	status, code := "ready", http.StatusOK
	results := map[string]string{}
	for name, check := range h.checks {
		results[name] = "ok"
		if err := check(ctx); err != nil {
			h.log.WarnContext(ctx, "readiness check failed", "check", name, "error", err)
			results[name] = "unreachable"
			status, code = "unavailable", http.StatusServiceUnavailable
		}
	}
	writeJSON(w, code, map[string]any{"status": status, "checks": results, "build": h.build})
}

// fail maps service errors to HTTP responses. Unexpected errors are logged
// and hidden from the client.
func (h *handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	if r.Context().Err() != nil {
		return
	}
	var ve *incidents.ValidationError
	switch {
	case errors.Is(err, domain.ErrQueueFull) || errors.Is(err, investigation.ErrBusy):
		w.Header().Set("Retry-After", "10")
		writeError(w, r, http.StatusTooManyRequests, "busy", "investigation queue is full, retry later")
	case errors.Is(err, domain.ErrIdempotencyConflict):
		writeError(w, r, http.StatusConflict, "idempotency_conflict", err.Error())
	case errors.Is(err, jobs.ErrUnavailable):
		h.log.ErrorContext(r.Context(), "job storage unavailable", "error", err)
		writeError(w, r, http.StatusServiceUnavailable, "unavailable", "job storage unavailable")
	case errors.Is(err, investigation.ErrInvalidOptions):
		writeError(w, r, http.StatusBadRequest, "validation_failed", err.Error())
	case errors.As(err, &ve):
		writeError(w, r, http.StatusBadRequest, "validation_failed", ve.Error())
	case errors.Is(err, domain.ErrNotFound):
		writeError(w, r, http.StatusNotFound, "not_found", err.Error())
	default:
		h.log.ErrorContext(r.Context(), "request failed", "error", err)
		writeError(w, r, http.StatusInternalServerError, "internal", "internal server error")
	}
}

func pathID(w http.ResponseWriter, r *http.Request, key string) (string, bool) {
	id := r.PathValue(key)
	if !domain.IsUUID(id) {
		writeError(w, r, http.StatusBadRequest, "validation_failed", key+": must be a UUID")
		return "", false
	}
	return id, true
}

// decodeBody strictly decodes a JSON body of at most maxBodyBytes. When
// optional is true an empty body is accepted.
func decodeBody(w http.ResponseWriter, r *http.Request, dst any, optional bool) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	err := dec.Decode(dst)
	if optional && errors.Is(err, io.EOF) {
		return true
	}
	if err == nil && dec.More() {
		err = errors.New("unexpected data after JSON object")
	}
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, r, http.StatusRequestEntityTooLarge, "body_too_large", "request body exceeds 1 MiB")
		} else {
			writeError(w, r, http.StatusBadRequest, "invalid_json", "invalid JSON body: "+err.Error())
		}
		return false
	}
	return true
}

func queryInt(r *http.Request, key string, def int) (int, error) {
	v := r.URL.Query().Get(key)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, errors.New(key + ": must be an integer")
	}
	return n, nil
}

type errorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeError(w http.ResponseWriter, r *http.Request, status int, code, msg string) {
	writeJSON(w, status, map[string]any{
		"error":      errorBody{Code: code, Message: redact.Text(msg)},
		"request_id": observability.RequestID(r.Context()),
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (h *handler) listInvestigations(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	limit, err1 := queryInt(r, "limit", 0)
	offset, err2 := queryInt(r, "offset", 0)
	if err := errors.Join(err1, err2); err != nil {
		writeError(w, r, http.StatusBadRequest, "validation_failed", err.Error())
		return
	}
	page, err := h.incidents.Investigations(r.Context(), id, limit, offset)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (h *handler) getInvestigation(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	investigationID, ok := pathID(w, r, "investigationID")
	if !ok {
		return
	}
	rec, err := h.incidents.Investigation(r.Context(), id, investigationID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, rec)
}

func jobLocation(incidentID, jobID string) string {
	return "/api/v1/incidents/" + incidentID + "/jobs/" + jobID
}

func (h *handler) submitJob(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	var opts investigation.Options
	if !decodeBody(w, r, &opts, true) {
		return
	}
	rec, err := h.jobs.Submit(r.Context(), id, r.Header.Get("Idempotency-Key"), opts)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	w.Header().Set("Location", jobLocation(id, rec.Job.ID))
	status := http.StatusAccepted
	if rec.Replayed && (rec.Job.Status == domain.InvestigationCompleted || rec.Job.Status == domain.InvestigationFailed) {
		status = http.StatusOK
	}
	writeJSON(w, status, rec)
}

func (h *handler) getJob(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	jobID, ok := pathID(w, r, "jobID")
	if !ok {
		return
	}
	rec, err := h.jobs.Get(r.Context(), id, jobID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, rec)
}
