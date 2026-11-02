package api

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	metric "github.com/prometheus/client_model/go"

	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/config"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/llm"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/observability"
)

func authConfig(t *testing.T, proxy, public string) config.Auth {
	t.Helper()
	c, err := config.Load(func(key string) string {
		return map[string]string{"DATABASE_URL": "x", "AUTH_ENABLED": "true", "AUTH_PROXY_URL": proxy, "AUTH_PUBLIC_URL": public}[key]
	})
	if err != nil {
		t.Fatal(err)
	}
	return c.Auth
}

func TestAuthenticationProtectsBackend(t *testing.T) {
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Auth-Request-User") != "" || r.Header.Get("X-Forwarded-User") != "" {
			t.Error("untrusted identity forwarded to authentication service")
		}
		switch r.Header.Get("Authorization") {
		case "Bearer valid":
			if r.Header.Get("Cookie") != "" {
				t.Error("bearer check must not fall back to cookies")
			}
			w.Header().Set("X-Auth-Request-User", "verified-user")
			w.WriteHeader(http.StatusAccepted)
		case "Bearer forbidden":
			w.WriteHeader(http.StatusForbidden)
		case "":
			if r.Header.Get("Cookie") == "session=valid" {
				w.Header().Set("X-Auth-Request-User", "verified-user")
				w.WriteHeader(http.StatusAccepted)
				return
			}
			fallthrough
		default:
			w.WriteHeader(http.StatusUnauthorized)
		}
	}))
	t.Cleanup(proxy.Close)
	const public = "https://troubleshooter.example.com"
	srv := httptest.NewServer(newHandler(t, llm.RulesProvider{}, nil, 1000, authConfig(t, proxy.URL, public)))
	t.Cleanup(srv.Close)
	for _, tt := range []struct {
		name, method, path, body string
		headers                  []string
		want                     int
	}{
		{"anonymous read", "GET", "/api/v1/incidents", "", nil, 401},
		{"anonymous job create", "POST", "/api/v1/incidents/00000000-0000-0000-0000-000000000001/jobs", "", nil, 401},
		{"anonymous job read", "GET", "/api/v1/incidents/00000000-0000-0000-0000-000000000001/jobs/00000000-0000-0000-0000-000000000002", "", nil, 401},
		{"anonymous history", "GET", "/api/v1/incidents/00000000-0000-0000-0000-000000000001/investigations", "", nil, 401},
		{"anonymous attempt", "GET", "/api/v1/incidents/00000000-0000-0000-0000-000000000001/investigations/00000000-0000-0000-0000-000000000002", "", nil, 401},
		{"anonymous create", "POST", "/api/v1/incidents", incidentBody, nil, 401},
		{"forged identity", "GET", "/api/v1/incidents", "", []string{"X-Forwarded-User", "admin", "X-Auth-Request-User", "admin"}, 401},
		{"forged proxy IP", "GET", "/api/v1/incidents", "", []string{"X-Forwarded-For", "127.0.0.1"}, 401},
		{"anonymous metrics", "GET", "/metrics", "", nil, 401},
		{"allowed JWT", "GET", "/api/v1/incidents", "", []string{"Authorization", "Bearer valid"}, 200},
		{"allowed cookie", "GET", "/api/v1/incidents", "", []string{"Cookie", "session=valid"}, 200},
		{"forbidden group", "GET", "/api/v1/incidents", "", []string{"Authorization", "Bearer forbidden"}, 403},
		{"invalid JWT with good cookie", "GET", "/api/v1/incidents", "", []string{"Authorization", "Bearer invalid", "Cookie", "session=valid"}, 401},
		{"cookie mutation needs origin", "POST", "/api/v1/incidents", incidentBody, []string{"Cookie", "session=valid"}, 403},
		{"foreign origin", "POST", "/api/v1/incidents", incidentBody, []string{"Cookie", "session=valid", "Origin", "https://other.example.com"}, 403},
		{"same origin", "POST", "/api/v1/incidents", incidentBody, []string{"Cookie", "session=valid", "Origin", public}, 201},
		{"JWT mutation", "POST", "/api/v1/incidents", incidentBody, []string{"Authorization", "Bearer valid"}, 201},
		{"health", "GET", "/health", "", nil, 200},
		{"ready", "GET", "/ready", "", nil, 200},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if resp, _ := do(t, tt.method, srv.URL+tt.path, tt.body, tt.headers...); resp.StatusCode != tt.want {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tt.want)
			}
		})
	}
	_, list := do(t, "GET", srv.URL+"/api/v1/incidents", "", "Authorization", "Bearer valid")
	if len(list["incidents"].([]any)) != 2 {
		t.Fatalf("rejected writes reached the store: %v", list)
	}
}

func TestAuthenticationFailsClosed(t *testing.T) {
	for _, status := range []int{200, 202, 302, 500} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				// 202 without a verified identity is also a failure.
				w.Header().Set("Location", "https://untrusted.example.com")
				w.WriteHeader(status)
			}))
			defer proxy.Close()
			srv := httptest.NewServer(newHandler(t, llm.RulesProvider{}, nil, 1000, authConfig(t, proxy.URL, "https://example.com")))
			defer srv.Close()
			if resp, _ := do(t, "GET", srv.URL+"/api/v1/incidents", ""); resp.StatusCode != 503 {
				t.Fatalf("status = %d, want 503", resp.StatusCode)
			}
			proxy.Close()
			if resp, _ := do(t, "GET", srv.URL+"/api/v1/incidents", ""); resp.StatusCode != 503 {
				t.Fatalf("unavailable proxy: %d", resp.StatusCode)
			}
		})
	}
}

func TestAuthenticationLoginAndTrustedOrigin(t *testing.T) {
	count := func(status string) float64 {
		var value metric.Metric
		if err := observability.HTTPRequestsTotal.WithLabelValues("GET", "authentication", status).Write(&value); err != nil {
			t.Fatal(err)
		}
		return value.GetCounter().GetValue()
	}
	redirects, denied := count("302"), count("401")
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/oauth2/auth" {
			w.WriteHeader(401)
			return
		}
		if r.Header.Get("X-Forwarded-Host") != "troubleshooter.example.com" || r.Header.Get("X-Forwarded-Proto") != "https" || r.Header.Get("X-Auth-Request-Redirect") != "" {
			t.Errorf("untrusted forwarding headers: %v", r.Header)
		}
		w.WriteHeader(204)
	}))
	t.Cleanup(proxy.Close)
	srv := httptest.NewServer(newHandler(t, llm.RulesProvider{}, nil, 1000, authConfig(t, proxy.URL, "https://troubleshooter.example.com")))
	t.Cleanup(srv.Close)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, _ := http.NewRequestWithContext(context.Background(), "GET", srv.URL+"/?incident=example", nil)
	req.Host = "untrusted.example.com"
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	location, err := url.Parse(resp.Header.Get("Location"))
	if err != nil || resp.StatusCode != 302 || location.Query().Get("rd") != "/?incident=example" {
		t.Fatalf("unsafe redirect: %s, %v", resp.Header.Get("Location"), err)
	}
	if count("302") != redirects+1 || count("401") != denied {
		t.Fatal("login redirect counted as an authentication error")
	}
	if resp, _ := do(t, "GET", srv.URL+"/oauth2/start", "", "X-Forwarded-Host", "untrusted.example.com", "X-Forwarded-Proto", "http", "X-Auth-Request-Redirect", "https://untrusted.example.com"); resp.StatusCode != 204 {
		t.Fatalf("login endpoint not proxied: %d", resp.StatusCode)
	}
}

func TestAuthenticationAuditIdentity(t *testing.T) {
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Auth-Request-User", "verified-user")
		w.WriteHeader(202)
	}))
	t.Cleanup(proxy.Close)
	var logs bytes.Buffer
	log := observability.NewLogger(&logs, slog.LevelInfo)
	h := authenticate(authConfig(t, proxy.URL, "https://example.com"), log, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.InfoContext(r.Context(), "operation")
		w.WriteHeader(204)
	}))
	req := httptest.NewRequest("GET", "/api/v1/incidents", nil)
	req.Header.Set("X-Forwarded-User", "forged-user")
	req.Header.Set("Authorization", "Bearer test-credential")
	h.ServeHTTP(httptest.NewRecorder(), req)
	if got := logs.String(); !strings.Contains(got, `"user_id":"verified-user"`) || strings.Contains(got, "forged-user") || strings.Contains(got, "test-credential") {
		t.Fatalf("invalid audit identity or credential logged: %s", got)
	}
}

func TestDisabledAuthenticationNeedsNoProvider(t *testing.T) {
	srv := httptest.NewServer(newHandler(t, llm.RulesProvider{}, errors.New("probe failed"), 1000, config.Auth{}))
	t.Cleanup(srv.Close)
	if resp, _ := do(t, "POST", srv.URL+"/api/v1/incidents", incidentBody); resp.StatusCode != 201 {
		t.Fatalf("disabled auth still requires login: %d", resp.StatusCode)
	}
	if resp, _ := do(t, "GET", srv.URL+"/ready", ""); resp.StatusCode != 503 {
		t.Fatalf("readiness failure hidden: %d", resp.StatusCode)
	}
}

func TestAuthenticationTimeout(t *testing.T) {
	proxy := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer proxy.Close()
	h := authenticate(authConfig(t, proxy.URL, "https://example.com"), slog.New(slog.NewTextHandler(io.Discard, nil)), http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("timed-out authentication admitted a request")
	}))
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	start := time.Now()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/incidents", nil).WithContext(ctx))
	if w.Code != 503 || time.Since(start) >= 7*time.Second {
		t.Fatalf("authentication did not fail within its timeout: status %d, elapsed %s", w.Code, time.Since(start))
	}
}

func TestAuthenticationReadiness(t *testing.T) {
	for _, status := range []int{200, 302, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/ready" {
					t.Errorf("readiness used %s", r.URL.Path)
				}
				w.Header().Set("Location", "https://other.example.com")
				w.WriteHeader(status)
			}))
			defer proxy.Close()
			cfg := authConfig(t, proxy.URL, "https://example.com")
			if err := AuthenticationReady(t.Context(), cfg); (err == nil) != (status == 200) {
				t.Fatalf("unexpected readiness for %d: %v", status, err)
			}
			proxy.Close()
			if err := AuthenticationReady(t.Context(), cfg); err == nil {
				t.Fatal("unavailable proxy was ready")
			}
		})
	}
}

func TestAuthenticationRateLimit(t *testing.T) {
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(401) }))
	defer proxy.Close()
	srv := httptest.NewServer(newHandler(t, llm.RulesProvider{}, nil, 1, authConfig(t, proxy.URL, "https://example.com")))
	defer srv.Close()
	for i := range 3 {
		want := 401
		if i == 2 {
			want = 429
		}
		if resp, _ := do(t, "GET", srv.URL+"/metrics", ""); resp.StatusCode != want {
			t.Fatalf("request %d: %d, want %d", i, resp.StatusCode, want)
		}
	}
	if resp, _ := do(t, "GET", srv.URL+"/health", ""); resp.StatusCode != 200 {
		t.Fatalf("probe rate limited: %d", resp.StatusCode)
	}
}
