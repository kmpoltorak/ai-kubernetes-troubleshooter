// Package config loads and validates application configuration from
// environment variables. The result is an immutable value passed explicitly
// to the components that need it.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/domain"
)

// Config holds all runtime settings. Construct it with Load.
type Config struct {
	AppEnv   string
	HTTPPort int
	LogLevel slog.Level

	DatabaseURL string

	Kubeconfig        string
	KubernetesContext string
	// ClusterName is the only value accepted in an incident's "cluster" field.
	ClusterName string
	// AllowedNamespaces restricts investigations; empty allows any namespace
	// the ServiceAccount can read.
	AllowedNamespaces []string

	LLMProvider   string
	LLMModel      string
	LLMTimeout    time.Duration
	OpenAIAPIKey  string
	OpenAIBaseURL string
	OllamaBaseURL string

	SimulationEnabled  bool
	SimulationScenario string

	InvestigationTimeout        time.Duration
	MaxConcurrentInvestigations int
	RateLimitRPS                float64
}

var defaultModels = map[string]string{
	"rules":  "rules-v1",
	"openai": "gpt-4o-mini",
	"ollama": "llama3.2",
}

// Load reads configuration using getenv (typically os.Getenv) and validates it.
// All problems are reported together so a misconfigured deployment can be
// fixed in one pass.
func Load(getenv func(string) string) (Config, error) {
	p := parser{getenv: getenv}
	c := Config{
		AppEnv:                      p.str("APP_ENV", "development"),
		HTTPPort:                    p.int("HTTP_PORT", 8080),
		LogLevel:                    p.level("LOG_LEVEL", slog.LevelInfo),
		DatabaseURL:                 p.str("DATABASE_URL", ""),
		Kubeconfig:                  p.str("KUBECONFIG", ""),
		KubernetesContext:           p.str("KUBERNETES_CONTEXT", ""),
		ClusterName:                 p.str("KUBERNETES_CLUSTER_NAME", "local"),
		AllowedNamespaces:           p.list("KUBERNETES_ALLOWED_NAMESPACES"),
		LLMProvider:                 strings.ToLower(p.str("LLM_PROVIDER", "rules")),
		LLMModel:                    p.str("LLM_MODEL", ""),
		LLMTimeout:                  p.duration("LLM_TIMEOUT", 60*time.Second),
		OpenAIAPIKey:                p.str("OPENAI_API_KEY", ""),
		OpenAIBaseURL:               strings.TrimRight(p.str("OPENAI_BASE_URL", "https://api.openai.com/v1"), "/"),
		OllamaBaseURL:               strings.TrimRight(p.str("OLLAMA_BASE_URL", "http://localhost:11434"), "/"),
		SimulationEnabled:           p.bool("SIMULATION_ENABLED", true),
		SimulationScenario:          p.str("SIMULATION_SCENARIO", "healthy"),
		InvestigationTimeout:        p.duration("INVESTIGATION_TIMEOUT", 90*time.Second),
		MaxConcurrentInvestigations: p.int("MAX_CONCURRENT_INVESTIGATIONS", 4),
		RateLimitRPS:                p.float("RATE_LIMIT_RPS", 5),
	}

	if c.AppEnv != "development" && c.AppEnv != "production" {
		p.fail("APP_ENV", "must be development or production")
	}
	if c.HTTPPort < 1 || c.HTTPPort > 65535 {
		p.fail("HTTP_PORT", "must be between 1 and 65535")
	}
	if c.DatabaseURL == "" {
		p.fail("DATABASE_URL", "is required")
	}
	if err := domain.ValidateObjectName(c.ClusterName); err != nil {
		p.fail("KUBERNETES_CLUSTER_NAME", err.Error())
	}
	for _, ns := range c.AllowedNamespaces {
		if err := domain.ValidateNamespace(ns); err != nil {
			p.fail("KUBERNETES_ALLOWED_NAMESPACES", fmt.Sprintf("entry %q %s", ns, err))
		}
	}
	if _, ok := defaultModels[c.LLMProvider]; !ok {
		p.fail("LLM_PROVIDER", "must be one of rules, openai, ollama")
	} else if c.LLMModel == "" {
		c.LLMModel = defaultModels[c.LLMProvider]
	}
	if c.LLMProvider == "openai" && c.OpenAIAPIKey == "" {
		p.fail("OPENAI_API_KEY", "is required when LLM_PROVIDER=openai")
	}
	if c.LLMTimeout <= 0 {
		p.fail("LLM_TIMEOUT", "must be positive")
	}
	if c.InvestigationTimeout <= 0 {
		p.fail("INVESTIGATION_TIMEOUT", "must be positive")
	}
	if c.MaxConcurrentInvestigations < 1 {
		p.fail("MAX_CONCURRENT_INVESTIGATIONS", "must be at least 1")
	}
	if c.RateLimitRPS <= 0 {
		p.fail("RATE_LIMIT_RPS", "must be positive")
	}

	if len(p.errs) > 0 {
		return Config{}, fmt.Errorf("invalid configuration: %w", errors.Join(p.errs...))
	}
	return c, nil
}

// String renders the configuration with secrets redacted, safe for logs.
func (c Config) String() string {
	return fmt.Sprintf("env=%s port=%d cluster=%s context=%q namespaces=%v provider=%s model=%s simulation=%t scenario=%s openai_key=%s",
		c.AppEnv, c.HTTPPort, c.ClusterName, c.KubernetesContext, c.AllowedNamespaces, c.LLMProvider, c.LLMModel,
		c.SimulationEnabled, c.SimulationScenario, redact(c.OpenAIAPIKey))
}

func redact(s string) string {
	if s == "" {
		return "<unset>"
	}
	return "<redacted>"
}

// parser collects errors instead of stopping at the first bad variable.
type parser struct {
	getenv func(string) string
	errs   []error
}

func (p *parser) fail(key, msg string) {
	p.errs = append(p.errs, fmt.Errorf("%s %s", key, msg))
}

func (p *parser) str(key, def string) string {
	if v := strings.TrimSpace(p.getenv(key)); v != "" {
		return v
	}
	return def
}

// list parses a comma-separated value, ignoring empty entries.
func (p *parser) list(key string) []string {
	var out []string
	for _, s := range strings.Split(p.str(key, ""), ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func (p *parser) int(key string, def int) int {
	v := p.str(key, "")
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		p.fail(key, "must be an integer")
		return def
	}
	return n
}

func (p *parser) float(key string, def float64) float64 {
	v := p.str(key, "")
	if v == "" {
		return def
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		p.fail(key, "must be a number")
		return def
	}
	return f
}

func (p *parser) bool(key string, def bool) bool {
	v := p.str(key, "")
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		p.fail(key, "must be a boolean")
		return def
	}
	return b
}

func (p *parser) duration(key string, def time.Duration) time.Duration {
	v := p.str(key, "")
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		p.fail(key, "must be a duration such as 30s")
		return def
	}
	return d
}

func (p *parser) level(key string, def slog.Level) slog.Level {
	v := p.str(key, "")
	if v == "" {
		return def
	}
	var l slog.Level
	if err := l.UnmarshalText([]byte(v)); err != nil {
		p.fail(key, "must be debug, info, warn or error")
		return def
	}
	return l
}
