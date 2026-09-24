package api

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/observability"
)

var (
	safeRequestID = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
	// W3C traceparent: version-traceid-parentid-flags
	traceparent = regexp.MustCompile(`^[0-9a-f]{2}-([0-9a-f]{32})-[0-9a-f]{16}-[0-9a-f]{2}$`)
)

// withRequestIDs accepts a well-formed X-Request-ID and W3C traceparent from
// the caller or generates new IDs, and echoes the request ID back.
func withRequestIDs(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqID := r.Header.Get("X-Request-ID")
		if !safeRequestID.MatchString(reqID) {
			reqID = randomHex(16)
		}
		traceID := randomHex(16)
		if m := traceparent.FindStringSubmatch(r.Header.Get("traceparent")); m != nil {
			traceID = m[1]
		}
		w.Header().Set("X-Request-ID", reqID)
		next.ServeHTTP(w, r.WithContext(observability.WithRequestIDs(r.Context(), reqID, traceID)))
	})
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

// observe records metrics and an access log line. It must wrap the mux
// directly: the mux sets r.Pattern on this request, which keeps the route
// label bounded to registered patterns instead of raw paths.
func observe(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)

		route := r.Pattern
		if route == "" {
			route = "unmatched"
		}
		elapsed := time.Since(start)
		observability.HTTPRequestsTotal.WithLabelValues(r.Method, route, strconv.Itoa(rec.status)).Inc()
		observability.HTTPRequestDuration.WithLabelValues(r.Method, route).Observe(elapsed.Seconds())
		// Probe and scrape endpoints are polled constantly; keep them out of the access log.
		if route != "GET /metrics" && route != "GET /health" && route != "GET /ready" {
			log.InfoContext(r.Context(), "http request", "method", r.Method, "route", route,
				"status", rec.status, "duration_ms", elapsed.Milliseconds())
		}
	})
}

func recoverPanics(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				if err, ok := v.(error); ok && errors.Is(err, http.ErrAbortHandler) {
					panic(v)
				}
				log.ErrorContext(r.Context(), "panic in handler", "panic", v)
				writeError(w, r, http.StatusInternalServerError, "internal", "internal server error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// securityHeaders applies browser hardening to every response. The UI loads
// only same-origin scripts and styles, so the policy can be strict.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			h.Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

// rateLimit applies a token bucket per client IP to /api/ routes; probes and
// metrics are exempt. The client IP is the TCP peer: X-Forwarded-For is not
// trusted because any client can set it. Behind an ingress, the limit
// therefore applies per ingress replica, which still caps total load.
func rateLimit(rps float64, next http.Handler) http.Handler {
	var mu sync.Mutex
	limiters := map[string]*rate.Limiter{}
	burst := max(int(rps*2), 1)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}
		ip, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			ip = r.RemoteAddr
		}
		mu.Lock()
		// ponytail: whole-map reset bounds memory; an LRU would keep active clients' buckets.
		if len(limiters) > 10_000 {
			clear(limiters)
		}
		l, ok := limiters[ip]
		if !ok {
			l = rate.NewLimiter(rate.Limit(rps), burst)
			limiters[ip] = l
		}
		allowed := l.Allow()
		mu.Unlock()
		if !allowed {
			observability.HTTPRequestsTotal.WithLabelValues(r.Method, "rate_limited", "429").Inc()
			w.Header().Set("Retry-After", "1")
			writeError(w, r, http.StatusTooManyRequests, "rate_limited", "too many requests, slow down")
			return
		}
		next.ServeHTTP(w, r)
	})
}
