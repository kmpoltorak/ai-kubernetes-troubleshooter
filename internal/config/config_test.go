package config

import (
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
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
		len(c.Clusters) != 1 || c.Clusters[0].Name != "local" || c.Clusters[0].AllowedNamespaces != nil || c.RateLimitRPS != 5 || c.Auth.Enabled {
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
	c, err := Load(env(map[string]string{"DATABASE_URL": "x", "LLM_PROVIDER": "Anthropic", "ANTHROPIC_API_KEY": "k"}))
	if err != nil {
		t.Fatal(err)
	}
	if c.LLMProvider != "anthropic" || c.LLMModel != "claude-opus-5-5" {
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
		{"removed ollama provider", map[string]string{"DATABASE_URL": "x", "LLM_PROVIDER": "ollama"}, "LLM_PROVIDER"},
		{"openai without key", map[string]string{"DATABASE_URL": "x", "LLM_PROVIDER": "openai"}, "OPENAI_API_KEY"},
		{"anthropic without key", map[string]string{"DATABASE_URL": "x", "LLM_PROVIDER": "anthropic"}, "ANTHROPIC_API_KEY"},
		{"gemini without key", map[string]string{"DATABASE_URL": "x", "LLM_PROVIDER": "gemini"}, "GEMINI_API_KEY"},
		{"bad bool", map[string]string{"DATABASE_URL": "x", "SIMULATION_ENABLED": "maybe"}, "SIMULATION_ENABLED"},
		{"bad duration", map[string]string{"DATABASE_URL": "x", "LLM_TIMEOUT": "soon"}, "LLM_TIMEOUT"},
		{"bad level", map[string]string{"DATABASE_URL": "x", "LOG_LEVEL": "loud"}, "LOG_LEVEL"},
		{"bad env", map[string]string{"DATABASE_URL": "x", "APP_ENV": "staging"}, "APP_ENV"},
		{"bad cluster name", map[string]string{"DATABASE_URL": "x", "KUBERNETES_CLUSTER_NAME": "Prod Cluster"}, "KUBERNETES_CLUSTER_NAME"},
		{"bad namespace", map[string]string{"DATABASE_URL": "x", "KUBERNETES_ALLOWED_NAMESPACES": "payments,Bad_NS"}, `"Bad_NS"`},
		{"output tokens zero", map[string]string{"DATABASE_URL": "x", "LLM_MAX_OUTPUT_TOKENS": "0"}, "LLM_MAX_OUTPUT_TOKENS"},
		{"output tokens above limit", map[string]string{"DATABASE_URL": "x", "LLM_MAX_OUTPUT_TOKENS": "128001"}, "LLM_MAX_OUTPUT_TOKENS"},
		{"bad reasoning effort", map[string]string{"DATABASE_URL": "x", "LLM_PROVIDER": "openai", "OPENAI_API_KEY": "k", "LLM_REASONING_EFFORT": "Medium!"}, "LLM_REASONING_EFFORT"},
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
	if strings.Join(c.Clusters[0].AllowedNamespaces, "|") != "payments|orders" {
		t.Fatalf("got %q", c.Clusters[0].AllowedNamespaces)
	}
}

func TestLoadClustersFile(t *testing.T) {
	write := func(body string) string {
		path := filepath.Join(t.TempDir(), "clusters.yaml")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	path := write(`clusters:
  - name: lab
    kubeconfig: /kube/lab.yaml
    allowed_namespaces: [payments]
  - name: prod
    kubeconfig: /kube/prod.yaml
    context: prod-ro
`)
	c, err := Load(env(map[string]string{"DATABASE_URL": "x", "KUBERNETES_CLUSTERS_FILE": path}))
	if err != nil {
		t.Fatal(err)
	}
	want := []Cluster{{Name: "lab", Kubeconfig: "/kube/lab.yaml", AllowedNamespaces: []string{"payments"}}, {Name: "prod", Kubeconfig: "/kube/prod.yaml", Context: "prod-ro"}}
	if !reflect.DeepEqual(c.Clusters, want) {
		t.Fatalf("clusters: %+v", c.Clusters)
	}
	if s := c.String(); !strings.Contains(s, "clusters=[lab prod]") || strings.Contains(s, "/kube/") || strings.Contains(s, "prod-ro") {
		t.Fatalf("String shows paths or contexts: %s", s)
	}

	for _, tt := range []struct{ name, body, want string }{
		{"repeated name", "clusters: [{name: lab}, {name: lab}]", `"lab" is repeated`},
		{"bad name", "clusters: [{name: Lab Cluster}]", "clusters[0].name"},
		{"bad namespace", "clusters: [{name: lab, allowed_namespaces: [Bad_NS]}]", `"Bad_NS"`},
		{"typo in a field", "clusters: [{name: lab, allowed_namespace: [payments]}]", "allowed_namespace"},
		{"no clusters", "clusters: []", "at least one cluster"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Load(env(map[string]string{"DATABASE_URL": "x", "KUBERNETES_CLUSTERS_FILE": write(tt.body)})); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("want %q, got %v", tt.want, err)
			}
		})
	}
	for _, v := range singleClusterVars {
		e := map[string]string{"DATABASE_URL": "x", "KUBERNETES_CLUSTERS_FILE": path, v: "payments"}
		if _, err := Load(env(e)); err == nil || !strings.Contains(err.Error(), "cannot be combined with "+v) {
			t.Fatalf("%s set with the file: %v", v, err)
		}
	}
	// KUBECONFIG is often set in the shell; entries without their own kubeconfig read it.
	if _, err := Load(env(map[string]string{"DATABASE_URL": "x", "KUBERNETES_CLUSTERS_FILE": path, "KUBECONFIG": "/home/example/.kube/config"})); err != nil {
		t.Fatalf("KUBECONFIG with the file: %v", err)
	}
	if _, err := Load(env(map[string]string{"DATABASE_URL": "x", "KUBERNETES_CLUSTERS_FILE": "/does/not/exist"})); err == nil || !strings.Contains(err.Error(), "not readable") {
		t.Fatalf("missing file: %v", err)
	}
}

func TestLoadReasoningEffort(t *testing.T) {
	for _, provider := range []string{"openai", "anthropic", "gemini"} {
		c, err := Load(env(map[string]string{"DATABASE_URL": "x", "LLM_PROVIDER": provider, "OPENAI_API_KEY": "k", "ANTHROPIC_API_KEY": "k", "GEMINI_API_KEY": "k", "LLM_REASONING_EFFORT": "medium"}))
		if err != nil || c.LLMReasoningEffort != "medium" {
			t.Fatalf("%s: got %q, %v", provider, c.LLMReasoningEffort, err)
		}
	}
}

func TestStringRedactsSecrets(t *testing.T) {
	for _, provider := range []string{"openai", "anthropic", "gemini"} {
		c, err := Load(env(map[string]string{"DATABASE_URL": "x", "LLM_PROVIDER": provider, "OPENAI_API_KEY": "sk-secret", "ANTHROPIC_API_KEY": "sk-ant-secret", "GEMINI_API_KEY": "gm-secret"}))
		if err != nil {
			t.Fatal(err)
		}
		if s := c.String(); strings.Contains(s, "secret") || strings.Count(s, "<redacted>") != 3 {
			t.Fatalf("secret leaked or not redacted: %s", s)
		}
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

func TestLoadCustomResources(t *testing.T) {
	c, err := Load(env(map[string]string{"DATABASE_URL": "x", "KUBERNETES_CUSTOM_RESOURCES": "argoproj.io/v1alpha1/rollouts, postgresql.cnpg.io/v1/clusters"}))
	if err != nil {
		t.Fatal(err)
	}
	if got := c.CustomKinds(); len(got) != 2 || got[0] != "rollouts.argoproj.io" || got[1] != "clusters.postgresql.cnpg.io" {
		t.Fatalf("kinds %v", got)
	}
	for entry, want := range map[string]string{
		"apps/v1/deployments":        "custom API group", // built-ins have their own diagnostics
		"v1/secrets":                 "group/version/resource",
		"argoproj.io/latest/rollout": "version",
		"argoproj.io/v1/Rollouts":    "lowercase plural",
		"argoproj.io/v1/roll;outs":   "lowercase plural",
	} {
		if _, err := Load(env(map[string]string{"DATABASE_URL": "x", "KUBERNETES_CUSTOM_RESOURCES": entry})); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v", entry, err)
		}
	}
}

func TestLoadLLMDefaults(t *testing.T) {
	c, err := Load(env(map[string]string{"DATABASE_URL": "x"}))
	if err != nil || c.LLMReasoningEffort != "medium" || c.LLMMaxOutputTokens != 16000 {
		t.Fatalf("defaults: effort=%q tokens=%d err=%v", c.LLMReasoningEffort, c.LLMMaxOutputTokens, err)
	}
	c, err = Load(env(map[string]string{"DATABASE_URL": "x", "LLM_MAX_OUTPUT_TOKENS": "32000", "LLM_REASONING_EFFORT": "high"}))
	if err != nil || c.LLMReasoningEffort != "high" || c.LLMMaxOutputTokens != 32000 {
		t.Fatalf("overrides: effort=%q tokens=%d err=%v", c.LLMReasoningEffort, c.LLMMaxOutputTokens, err)
	}
}
