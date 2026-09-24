// Package api exposes the REST API over the standard library router.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/domain"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/incidents"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/investigation"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/observability"
)

const maxBodyBytes = 1 << 20

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

type handler struct {
	incidents *incidents.Service
	engine    *investigation.Engine
	checks    map[string]func(context.Context) error
	log       *slog.Logger
}

// New returns the fully wired HTTP handler. checks are the readiness checks
// by dependency name (database, kubernetes). API routes are rate limited per
// client IP at rps requests per second.
func New(svc *incidents.Service, engine *investigation.Engine, checks map[string]func(context.Context) error, log *slog.Logger, rps float64) http.Handler {
	h := &handler{incidents: svc, engine: engine, checks: checks, log: log}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/incidents", h.createIncident)
	mux.HandleFunc("GET /api/v1/incidents", h.listIncidents)
	mux.HandleFunc("GET /api/v1/incidents/{id}", h.getIncident)
	mux.HandleFunc("POST /api/v1/incidents/{id}/investigate", h.investigate)
	mux.HandleFunc("GET /api/v1/incidents/{id}/report", h.getReport)
	mux.HandleFunc("GET /health", h.health)
	mux.HandleFunc("GET /ready", h.readiness)
	mux.Handle("GET /metrics", promhttp.Handler())
	registerUI(mux)
	return recoverPanics(log, securityHeaders(withRequestIDs(rateLimit(rps, observe(log, mux)))))
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

func (h *handler) getIncident(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
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
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var opts investigation.Options
	if !decodeBody(w, r, &opts, true) {
		return
	}
	rec, err := h.engine.Investigate(r.Context(), id, opts)
	switch {
	case errors.Is(err, investigation.ErrBusy):
		w.Header().Set("Retry-After", "10")
		writeError(w, r, http.StatusTooManyRequests, "busy", "too many concurrent investigations, retry later")
	case errors.Is(err, investigation.ErrInvalidOptions):
		writeError(w, r, http.StatusBadRequest, "validation_failed", err.Error())
	case errors.Is(err, investigation.ErrAnalysisFailed):
		// The investigation ran and was stored; return it so the caller can
		// inspect the evidence, with a gateway error for the failed analysis.
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"error":      errorBody{Code: "analysis_failed", Message: rec.Investigation.Error},
			"request_id": observability.RequestID(r.Context()),
			"result":     rec,
		})
	case err != nil:
		h.fail(w, r, err)
	default:
		writeJSON(w, http.StatusCreated, rec)
	}
}

func (h *handler) getReport(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
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

func (h *handler) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
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
	writeJSON(w, code, map[string]any{"status": status, "checks": results})
}

// fail maps service errors to HTTP responses. Unexpected errors are logged
// and hidden from the client.
func (h *handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	var ve *incidents.ValidationError
	switch {
	case errors.As(err, &ve):
		writeError(w, r, http.StatusBadRequest, "validation_failed", ve.Error())
	case errors.Is(err, domain.ErrNotFound):
		writeError(w, r, http.StatusNotFound, "not_found", err.Error())
	default:
		h.log.ErrorContext(r.Context(), "request failed", "error", err)
		writeError(w, r, http.StatusInternalServerError, "internal", "internal server error")
	}
}

func pathID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := r.PathValue("id")
	if !uuidPattern.MatchString(id) {
		writeError(w, r, http.StatusBadRequest, "validation_failed", "id: must be a UUID")
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
		"error":      errorBody{Code: code, Message: msg},
		"request_id": observability.RequestID(r.Context()),
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
