// Package api exposes the REST API over the standard library router.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
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
	Mode              string   `json:"mode"`
	ClusterName       string   `json:"cluster_name"`
	AllowedNamespaces []string `json:"allowed_namespaces"`
	Analyzer          Analyzer `json:"analyzer"`
	Scenarios         []string `json:"scenarios"`
	// ResourceTypes are the kinds an incident can target, in form order.
	ResourceTypes []domain.ResourceType `json:"resource_types"`
}

type Analyzer struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

// RuntimeFrom selects the UI-relevant settings; empty namespaces mean all readable ones.
func RuntimeFrom(cfg config.Config) Runtime {
	rt := Runtime{Mode: "live", ClusterName: cfg.ClusterName, AllowedNamespaces: append([]string{}, cfg.AllowedNamespaces...),
		Analyzer: Analyzer{Provider: cfg.LLMProvider, Model: cfg.LLMModel}, Scenarios: []string{}, ResourceTypes: domain.ResourceTypes}
	if cfg.SimulationEnabled {
		rt.Mode, rt.Scenarios = "simulation", simulation.Scenarios
	}
	return rt
}

// TargetLister lists object names for the incident form; nil lists nothing (simulation).
type TargetLister func(ctx context.Context, namespace string, kind domain.ResourceType) ([]string, error)

// New returns the fully wired HTTP handler. checks are the readiness checks
// by dependency name (database, kubernetes). API routes are rate limited per
// client IP at rps requests per second; auth also limits other non-probe routes.
func New(svc *incidents.Service, jobService JobService, checks map[string]func(context.Context) error, log *slog.Logger, rps float64, auth config.Auth, runtime Runtime, targets TargetLister) http.Handler {
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
	mux.HandleFunc("GET /api/v1/targets", h.listTargets(runtime, targets))
	mux.HandleFunc("GET /health", h.health)
	mux.HandleFunc("GET /ready", h.readiness)
	mux.Handle("GET /metrics", promhttp.Handler())
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
func (h *handler) listTargets(runtime Runtime, list TargetLister) http.HandlerFunc {
	policy := domain.TargetPolicy{ClusterName: runtime.ClusterName, AllowedNamespaces: runtime.AllowedNamespaces}
	return func(w http.ResponseWriter, r *http.Request) {
		// The name is a placeholder: only namespace and kind come from the request.
		t := domain.Target{Cluster: runtime.ClusterName, Namespace: r.URL.Query().Get("namespace"),
			ResourceType: domain.ResourceType(r.URL.Query().Get("kind")), ResourceName: "placeholder"}
		if err := errors.Join(t.Validate(), policy.Check(t)); err != nil {
			writeError(w, r, http.StatusBadRequest, "validation_failed", err.Error())
			return
		}
		names := []string{}
		if list != nil {
			ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
			defer cancel()
			found, err := list(ctx, t.Namespace, t.ResourceType)
			if err != nil {
				h.log.WarnContext(r.Context(), "listing targets failed", "namespace", t.Namespace, "kind", t.ResourceType, "error", redact.Text(err.Error()))
				writeError(w, r, http.StatusBadGateway, "list_failed", redact.Text(err.Error()))
				return
			}
			names = append(names, found...)
		}
		writeJSON(w, http.StatusOK, map[string][]string{"names": names})
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
