package config

import (
	"log/slog"
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoadDefaults(t *testing.T) {
	c, err := Load(env(map[string]string{"DATABASE_URL": "postgres://x"}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.HTTPPort != 8080 || c.LLMProvider != "rules" || c.LLMModel != "rules-v2" ||
		!c.SimulationEnabled || c.SimulationScenario != "healthy" || c.LogLevel != slog.LevelInfo ||
		c.InvestigationTimeout != 90*time.Second || c.MaxConcurrentInvestigations != 4 ||
		c.ClusterName != "local" || c.AllowedNamespaces != nil || c.RateLimitRPS != 5 || c.Auth.Enabled {
		t.Fatalf("unexpected defaults: %+v", c)
	}
}

func TestAuthConfiguration(t *testing.T) {
	for _, tt := range []struct {
		name, enabled, proxy, public string
		wantError                    string
	}{
		{"disabled without provider", "false", "", "", ""},
		{"disabled ignores unused URLs", "false", ":bad", ":bad", ""},
		{"enabled", "true", "", "https://troubleshooter.example.com/", ""},
		{"local HTTP", "true", "http://127.0.0.1:4180", "http://127.0.0.1:8080", ""},
		{"missing public URL", "true", "", "", "AUTH_PUBLIC_URL"},
		{"bad bool", "maybe", "", "", "AUTH_ENABLED"},
		{"public HTTP", "true", "", "http://example.com", "AUTH_PUBLIC_URL"},
		{"credentials", "true", "http://user:secret@example.com", "https://example.com", "AUTH_PROXY_URL"},
		{"proxy path", "true", "http://example.com/auth", "https://example.com", "AUTH_PROXY_URL"},
		{"proxy scheme", "true", "file:///tmp/proxy", "https://example.com", "AUTH_PROXY_URL"},
		{"proxy port", "true", "http://example.com:70000", "https://example.com", "AUTH_PROXY_URL"},
		{"public query", "true", "", "https://example.com?token=secret", "AUTH_PUBLIC_URL"},
		{"public fragment", "true", "", "https://example.com#fragment", "AUTH_PUBLIC_URL"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c, err := Load(env(map[string]string{"DATABASE_URL": "x", "AUTH_ENABLED": tt.enabled,
				"AUTH_PROXY_URL": tt.proxy, "AUTH_PUBLIC_URL": tt.public}))
			if tt.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantError) || strings.Contains(err.Error(), "secret") {
					t.Fatalf("expected safe error for %s, got %v", tt.wantError, err)
				}
				return
			}
			if err != nil || c.Auth.Enabled != (tt.enabled == "true") {
				t.Fatalf("unexpected config: %+v, %v", c.Auth, err)
			}
			if c.Auth.Enabled && (c.Auth.ProxyURL == nil || c.Auth.PublicURL == nil || c.Auth.PublicURL.Path != "") {
				t.Fatalf("URLs not parsed/canonicalized: %+v", c.Auth)
			}
		})
	}
}

func TestLoadProviderDefaultModel(t *testing.T) {
	c, err := Load(env(map[string]string{"DATABASE_URL": "x", "LLM_PROVIDER": "Ollama"}))
	if err != nil {
		t.Fatal(err)
	}
	if c.LLMProvider != "ollama" || c.LLMModel != "llama3.2" {
		t.Fatalf("got provider=%s model=%s", c.LLMProvider, c.LLMModel)
	}
}

func TestLoadErrors(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		wantErr string
	}{
		{"missing database url", map[string]string{}, "DATABASE_URL is required"},
		{"bad port", map[string]string{"DATABASE_URL": "x", "HTTP_PORT": "99999"}, "HTTP_PORT"},
		{"non numeric port", map[string]string{"DATABASE_URL": "x", "HTTP_PORT": "abc"}, "HTTP_PORT must be an integer"},
		{"unknown provider", map[string]string{"DATABASE_URL": "x", "LLM_PROVIDER": "bard"}, "LLM_PROVIDER"},
		{"openai without key", map[string]string{"DATABASE_URL": "x", "LLM_PROVIDER": "openai"}, "OPENAI_API_KEY"},
		{"bad bool", map[string]string{"DATABASE_URL": "x", "SIMULATION_ENABLED": "maybe"}, "SIMULATION_ENABLED"},
		{"bad duration", map[string]string{"DATABASE_URL": "x", "LLM_TIMEOUT": "soon"}, "LLM_TIMEOUT"},
		{"bad level", map[string]string{"DATABASE_URL": "x", "LOG_LEVEL": "loud"}, "LOG_LEVEL"},
		{"bad env", map[string]string{"DATABASE_URL": "x", "APP_ENV": "staging"}, "APP_ENV"},
		{"bad cluster name", map[string]string{"DATABASE_URL": "x", "KUBERNETES_CLUSTER_NAME": "Prod Cluster"}, "KUBERNETES_CLUSTER_NAME"},
		{"bad namespace", map[string]string{"DATABASE_URL": "x", "KUBERNETES_ALLOWED_NAMESPACES": "payments,Bad_NS"}, `"Bad_NS"`},
		{"zero rate limit", map[string]string{"DATABASE_URL": "x", "RATE_LIMIT_RPS": "0"}, "RATE_LIMIT_RPS"},
		{"zero concurrency", map[string]string{"DATABASE_URL": "x", "MAX_CONCURRENT_INVESTIGATIONS": "0"}, "MAX_CONCURRENT_INVESTIGATIONS"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(env(tt.env))
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("want error containing %q, got %v", tt.wantErr, err)
			}
		})
	}
}

func TestLoadAllowedNamespaces(t *testing.T) {
	c, err := Load(env(map[string]string{"DATABASE_URL": "x", "KUBERNETES_ALLOWED_NAMESPACES": " payments, ,orders "}))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(c.AllowedNamespaces, "|") != "payments|orders" {
		t.Fatalf("got %q", c.AllowedNamespaces)
	}
}

func TestStringRedactsSecrets(t *testing.T) {
	c, err := Load(env(map[string]string{"DATABASE_URL": "x", "LLM_PROVIDER": "openai", "OPENAI_API_KEY": "sk-secret"}))
	if err != nil {
		t.Fatal(err)
	}
	if s := c.String(); strings.Contains(s, "sk-secret") || !strings.Contains(s, "<redacted>") {
		t.Fatalf("secret leaked or not redacted: %s", s)
	}
}

func TestLLMInputBudget(t *testing.T) {
	for _, value := range []string{"0", "-1", "invalid"} {
		if _, err := Load(env(map[string]string{"DATABASE_URL": "x", "LLM_MAX_INPUT_BYTES": value})); err == nil || !strings.Contains(err.Error(), "LLM_MAX_INPUT_BYTES") {
			t.Fatalf("accepted %q: %v", value, err)
		}
	}
	for _, tc := range []struct {
		value string
		want  int
	}{{"", DefaultLLMMaxInputBytes}, {"1024", 1024}} {
		got, err := Load(env(map[string]string{"DATABASE_URL": "x", "LLM_MAX_INPUT_BYTES": tc.value}))
		if err != nil || got.LLMMaxInputBytes != tc.want {
			t.Fatalf("got %d %v", got.LLMMaxInputBytes, err)
		}
	}
}

func TestDurableJobConfiguration(t *testing.T) {
	defaults, err := Load(env(map[string]string{"DATABASE_URL": "x"}))
	if err != nil || defaults.JobTimeout != 15*time.Minute || defaults.JobQueueCapacity != 100 {
		t.Fatalf("defaults: %+v %v", defaults, err)
	}
	for _, tc := range []struct{ key, value string }{{"JOB_TIMEOUT", "0s"}, {"JOB_TIMEOUT", "-1s"}, {"JOB_TIMEOUT", "bad"}, {"JOB_QUEUE_CAPACITY", "0"}, {"JOB_QUEUE_CAPACITY", "-1"}, {"JOB_QUEUE_CAPACITY", "bad"}} {
		if _, err := Load(env(map[string]string{"DATABASE_URL": "x", tc.key: tc.value})); err == nil || !strings.Contains(err.Error(), tc.key) {
			t.Fatalf("accepted %s=%s: %v", tc.key, tc.value, err)
		}
	}
}
