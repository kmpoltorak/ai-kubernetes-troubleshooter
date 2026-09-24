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
	if c.HTTPPort != 8080 || c.LLMProvider != "rules" || c.LLMModel != "rules-v1" ||
		!c.SimulationEnabled || c.SimulationScenario != "healthy" || c.LogLevel != slog.LevelInfo ||
		c.InvestigationTimeout != 90*time.Second || c.MaxConcurrentInvestigations != 4 ||
		c.ClusterName != "local" || c.AllowedNamespaces != nil || c.RateLimitRPS != 5 {
		t.Fatalf("unexpected defaults: %+v", c)
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
