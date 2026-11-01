package api

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/config"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/observability"
)

const authTimeout = 5 * time.Second

// authenticate delegates credential validation to OAuth2 Proxy. The backend
// checks every request itself, so reaching its port cannot bypass authentication.
func authenticate(cfg config.Auth, log *slog.Logger, next http.Handler) http.Handler {
	if !cfg.Enabled {
		return next
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = authTimeout
	client := &http.Client{Timeout: authTimeout, Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	checkURL := cfg.ProxyURL.ResolveReference(&url.URL{Path: "/oauth2/auth"}).String()
	origin := cfg.PublicURL.String()
	unavailable := func(w http.ResponseWriter, r *http.Request) {
		observability.HTTPRequestsTotal.WithLabelValues(r.Method, "authentication", "503").Inc()
		log.WarnContext(r.Context(), "authentication service unavailable")
		writeError(w, r, http.StatusServiceUnavailable, "auth_unavailable", "authentication service unavailable")
	}
	proxy := &httputil.ReverseProxy{
		Transport: transport,
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(cfg.ProxyURL)
			pr.Out.Host = cfg.ProxyURL.Host
			pr.Out.Header.Set("X-Forwarded-Host", cfg.PublicURL.Host)
			pr.Out.Header.Set("X-Forwarded-Proto", cfg.PublicURL.Scheme)
			pr.Out.Header.Del("X-Auth-Request-Redirect")
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, _ error) { unavailable(w, r) },
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if (r.Method == http.MethodGet || r.Method == http.MethodHead) && (r.URL.Path == "/health" || r.URL.Path == "/ready") {
			next.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/oauth2/") {
			ctx, cancel := context.WithTimeout(r.Context(), authTimeout)
			defer cancel()
			proxy.ServeHTTP(w, r.WithContext(ctx))
			return
		}
		check, err := http.NewRequestWithContext(r.Context(), http.MethodGet, checkURL, nil)
		if err != nil {
			unavailable(w, r)
			return
		}
		// An invalid bearer token must not fall back to a valid browser cookie.
		if authorization := r.Header.Get("Authorization"); authorization != "" {
			check.Header.Set("Authorization", authorization)
		} else {
			check.Header.Set("Cookie", r.Header.Get("Cookie"))
		}
		check.Header.Set("X-Forwarded-Host", cfg.PublicURL.Host)
		check.Header.Set("X-Forwarded-Proto", cfg.PublicURL.Scheme)
		resp, err := client.Do(check)
		if err != nil {
			unavailable(w, r)
			return
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			if resp.StatusCode == http.StatusUnauthorized && r.Method == http.MethodGet &&
				r.URL.Path == "/" && r.Header.Get("Authorization") == "" && !strings.Contains(r.Header.Get("Accept"), "application/json") {
				// A relative return URL cannot redirect to a host supplied by a client.
				returnURL := url.URL{Path: "/", RawQuery: r.URL.RawQuery}
				observability.HTTPRequestsTotal.WithLabelValues(r.Method, "authentication", "302").Inc()
				http.Redirect(w, r, "/oauth2/start?rd="+url.QueryEscape(returnURL.String()), http.StatusFound)
				return
			}
			observability.HTTPRequestsTotal.WithLabelValues(r.Method, "authentication", strconv.Itoa(resp.StatusCode)).Inc()
			if resp.StatusCode == http.StatusUnauthorized {
				w.Header().Set("WWW-Authenticate", "Bearer")
			}
			writeError(w, r, resp.StatusCode, "auth_required", "authentication or group membership required")
			return
		}
		user := strings.TrimSpace(resp.Header.Get("X-Auth-Request-User"))
		if resp.StatusCode != http.StatusAccepted || user == "" || len(user) > 256 {
			unavailable(w, r)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
			origins := r.Header.Values("Origin")
			if len(origins) > 1 || (len(origins) == 1 && origins[0] != origin) ||
				(len(origins) == 0 && r.Header.Get("Authorization") == "") {
				observability.HTTPRequestsTotal.WithLabelValues(r.Method, "authentication", "403").Inc()
				writeError(w, r, http.StatusForbidden, "invalid_origin", "request origin does not match the public URL")
				return
			}
		}
		for _, cookie := range resp.Header.Values("Set-Cookie") {
			w.Header().Add("Set-Cookie", cookie)
		}
		next.ServeHTTP(w, r.WithContext(observability.WithUser(r.Context(), user)))
	})
}

// AuthenticationReady checks the authentication service without a user session.
func AuthenticationReady(ctx context.Context, cfg config.Auth) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cfg.ProxyURL.ResolveReference(&url.URL{Path: "/ready"}).String(), nil)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: authTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return errors.New("authentication service not ready")
	}
	return nil
}
