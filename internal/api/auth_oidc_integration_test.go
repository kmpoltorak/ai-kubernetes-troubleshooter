//go:build authintegration

package api

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/domain"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/llm"
)

// This is a protocol fixture, not an authentication stub: the real OAuth2
// Proxy verifies signatures, issuer, audience, expiry, groups and browser state.
func TestOIDCAuthentication(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	var issuer string
	claims := func() map[string]any {
		return map[string]any{"iss": issuer, "aud": "test-client", "sub": "verified-operator",
			"email": "operator@example.com", "email_verified": true, "groups": []string{"troubleshooter-users"},
			"iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix()}
	}
	sign := func(payload map[string]any) string {
		b, marshalErr := json.Marshal(payload)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		unsigned := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","kid":"test-key"}`)) + "." + base64.RawURLEncoding.EncodeToString(b)
		hash := sha256.Sum256([]byte(unsigned))
		sig, signErr := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, hash[:])
		if signErr != nil {
			t.Fatal(signErr)
		}
		return unsigned + "." + base64.RawURLEncoding.EncodeToString(sig)
	}
	var mu sync.Mutex
	codes := map[string]url.Values{}
	mux := http.NewServeMux()
	fixture := httptest.NewUnstartedServer(mux)
	_ = fixture.Listener.Close()
	fixture.Listener, err = net.Listen("tcp4", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	port := fixture.Listener.Addr().(*net.TCPAddr).Port
	issuer = fmt.Sprintf("http://host.docker.internal:%d", port)
	respond := func(w http.ResponseWriter, value any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(value)
	}
	mux.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		respond(w, map[string]any{"issuer": issuer, "authorization_endpoint": issuer + "/authorize",
			"token_endpoint": issuer + "/token", "jwks_uri": issuer + "/keys",
			"response_types_supported": []string{"code"}, "subject_types_supported": []string{"public"},
			"id_token_signing_alg_values_supported": []string{"RS256"},
			"token_endpoint_auth_methods_supported": []string{"client_secret_basic"},
			"code_challenge_methods_supported":      []string{"S256"}})
	})
	mux.HandleFunc("GET /keys", func(w http.ResponseWriter, _ *http.Request) {
		respond(w, map[string]any{"keys": []any{map[string]string{"kty": "RSA", "kid": "test-key", "alg": "RS256", "use": "sig",
			"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes())}}})
	})
	mux.HandleFunc("GET /authorize", func(w http.ResponseWriter, r *http.Request) {
		values := r.URL.Query()
		if values.Get("nonce") == "" || values.Get("code_challenge_method") != "S256" {
			http.Error(w, "nonce and PKCE required", 400)
			return
		}
		code := domain.NewID()
		mu.Lock()
		codes[code] = values
		mu.Unlock()
		callback, parseErr := url.Parse(values.Get("redirect_uri"))
		if parseErr != nil {
			http.Error(w, "invalid callback", 400)
			return
		}
		callback.RawQuery = url.Values{"code": {code}, "state": {values.Get("state")}}.Encode()
		http.Redirect(w, r, callback.String(), http.StatusFound)
	})
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		values, ok := codes[r.FormValue("code")]
		delete(codes, r.FormValue("code"))
		mu.Unlock()
		challenge := sha256.Sum256([]byte(r.FormValue("code_verifier")))
		if !ok || base64.RawURLEncoding.EncodeToString(challenge[:]) != values.Get("code_challenge") {
			http.Error(w, "invalid code or PKCE", 400)
			return
		}
		payload := claims()
		payload["nonce"] = values.Get("nonce")
		token := sign(payload)
		respond(w, map[string]any{"access_token": token, "id_token": token, "token_type": "Bearer", "expires_in": 3600})
	})
	fixture.Start()
	t.Cleanup(fixture.Close)
	app := httptest.NewUnstartedServer(http.NotFoundHandler())
	public := "http://" + app.Listener.Addr().String()
	t.Cleanup(app.Close)
	configPath, err := filepath.Abs("../../deployments/kubernetes-authenticated/oauth2-proxy.cfg")
	if err != nil {
		t.Fatal(err)
	}
	docker := func(args ...string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		out, cmdErr := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
		if cmdErr != nil {
			t.Fatalf("docker %s failed: %v: %s", args[0], cmdErr, out)
		}
		return strings.TrimSpace(string(out))
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		t.Fatal(err)
	}
	id := docker("run", "--detach", "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges:true",
		"--add-host=host.docker.internal:host-gateway", "--publish=127.0.0.1::4180",
		"--mount=type=bind,source="+configPath+",target=/etc/oauth2-proxy.cfg,readonly",
		"quay.io/oauth2-proxy/oauth2-proxy:v7.15.5", "--config=/etc/oauth2-proxy.cfg",
		"--oidc-issuer-url="+issuer, "--client-id=test-client", "--client-secret=test-only",
		"--cookie-secret="+base64.URLEncoding.EncodeToString(secret),
		"--redirect-url="+public+"/oauth2/callback", "--cookie-secure=false", "--cookie-name=test_session", "--cookie-expire=3s")
	t.Cleanup(func() { docker("rm", "--force", id) })
	if docker("inspect", "--format={{.State.Running}}", id) != "true" {
		t.Fatalf("OAuth2 Proxy failed to start: %s", docker("logs", id))
	}
	proxyURL := "http://" + docker("port", id, "4180/tcp")
	transport := http.DefaultTransport.(*http.Transport).Clone()
	dialer := &net.Dialer{Timeout: 2 * time.Second}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if address == fmt.Sprintf("host.docker.internal:%d", port) {
			address = fmt.Sprintf("127.0.0.1:%d", port)
		}
		return dialer.DialContext(ctx, network, address)
	}
	t.Cleanup(transport.CloseIdleConnections)
	client := &http.Client{Timeout: 5 * time.Second, Transport: transport}
	send := func(client *http.Client, method, target, body string, headers ...string) int {
		t.Helper()
		req, reqErr := http.NewRequestWithContext(t.Context(), method, target, strings.NewReader(body))
		if reqErr != nil {
			t.Fatal(reqErr)
		}
		for i := 0; i+1 < len(headers); i += 2 {
			req.Header.Set(headers[i], headers[i+1])
		}
		resp, reqErr := client.Do(req)
		if reqErr != nil {
			t.Fatal(reqErr)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		req, reqErr := http.NewRequestWithContext(t.Context(), "GET", proxyURL+"/ready", nil)
		if reqErr != nil {
			t.Fatal(reqErr)
		}
		resp, reqErr := client.Do(req)
		if reqErr == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == 200 {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("OAuth2 Proxy did not become ready: %s", docker("logs", id))
		}
		time.Sleep(100 * time.Millisecond)
	}
	app.Config.Handler = newHandler(t, llm.RulesProvider{}, nil, 1000, authConfig(t, proxyURL, public))
	app.Start()
	apiURL := app.URL + "/api/v1/incidents"
	if status := send(client, "GET", apiURL, "", "X-Forwarded-User", "admin"); status != 401 {
		t.Fatalf("anonymous/forged identity reached backend: %d", status)
	}
	for _, field := range []string{"valid", "iss", "aud", "exp", "groups", "missing groups", "signature"} {
		t.Run(field, func(t *testing.T) {
			payload := claims()
			switch field {
			case "iss":
				payload["iss"] = "https://other.example.com"
			case "aud":
				payload["aud"] = "other-client"
			case "exp":
				payload["exp"] = time.Now().Add(-time.Minute).Unix()
			case "groups":
				payload["groups"] = []string{"other-group"}
			case "missing groups":
				delete(payload, "groups")
			}
			token := sign(payload)
			if field == "signature" {
				parts := strings.Split(token, ".")
				parts[2] = base64.RawURLEncoding.EncodeToString(make([]byte, key.Size()))
				token = strings.Join(parts, ".")
			}
			status := send(client, "GET", apiURL, "", "Authorization", "Bearer "+token)
			if (field == "valid" && status != 200) || (field != "valid" && status != 401 && status != 403) {
				t.Fatalf("JWT %s: status %d", field, status)
			}
		})
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	browser := &http.Client{Timeout: 10 * time.Second, Transport: transport, Jar: jar}
	// Preserve the nonce:redirect state format so this reaches the CSRF check,
	// rather than the upstream's malformed-state parser error.
	if status := send(browser, "GET", app.URL+"/oauth2/callback?code=untrusted&state=untrusted%3A%2F", ""); status != 403 {
		t.Fatalf("callback without valid state/CSRF accepted: %d", status)
	}
	if status := send(browser, "GET", app.URL+"/", ""); status != 200 {
		t.Fatalf("OIDC login/callback failed: %d", status)
	}
	if status := send(browser, "GET", apiURL, ""); status != 200 {
		t.Fatalf("session not accepted: %d", status)
	}
	if status := send(browser, "POST", apiURL, incidentBody, "Origin", public); status != 201 {
		t.Fatalf("session mutation failed: %d", status)
	}
	if status := send(browser, "POST", apiURL, incidentBody, "Origin", "https://other.example.com"); status != 403 {
		t.Fatalf("cross-origin mutation accepted: %d", status)
	}
	if status := send(browser, "GET", apiURL, "", "Authorization", "Bearer invalid"); status != 401 && status != 403 {
		t.Fatalf("invalid JWT used cookie fallback: %d", status)
	}
	parsed, _ := url.Parse(app.URL)
	var session string
	for _, cookie := range jar.Cookies(parsed) {
		if cookie.Name == "test_session" {
			session = cookie.String()
		}
	}
	if session == "" {
		t.Fatal("no session cookie issued")
	}
	deadline = time.Now().Add(6 * time.Second)
	var expiredStatus int
	for {
		expiredStatus = send(client, "GET", apiURL, "", "Cookie", session)
		if expiredStatus != 200 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("expired session still accepted")
		}
		time.Sleep(100 * time.Millisecond)
	}
	if expiredStatus != 401 && expiredStatus != 403 {
		t.Fatalf("session expiry returned an unrelated failure: %d", expiredStatus)
	}
	docker("stop", id)
	if status := send(client, "GET", apiURL, "", "Authorization", "Bearer "+sign(claims())); status != 503 {
		t.Fatalf("proxy failure did not close access: %d", status)
	}
}
