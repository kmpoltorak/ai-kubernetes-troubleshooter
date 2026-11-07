// Package llm turns collected diagnostic evidence into a structured analysis.
// Providers only analyze data; they have no way to run tools or commands.
package llm

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"

	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/config"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/domain"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/redact"
)

type Provider interface {
	// Name identifies provider and model, e.g. "openai/gpt-6-luna".
	Name() string
	Analyze(ctx context.Context, in AnalysisInput) (domain.Analysis, error)
}

type AnalysisInput struct {
	Incident domain.Incident
	Evidence []domain.Evidence
	// Failed lists diagnostics that observed nothing; their absence from
	// Evidence is not a sign of health.
	Failed      []domain.ToolExecution
	Limitations []string
}

func (in AnalysisInput) MissingData() []string {
	out := []string{}
	for _, te := range in.Failed {
		out = append(out, te.ToolName+": "+redact.Text(te.Error))
	}
	return out
}

// FromConfig builds the provider selected by LLM_PROVIDER. Config
// validation guarantees the provider name and required credentials.
func FromConfig(cfg config.Config) Provider {
	switch cfg.LLMProvider {
	case "openai":
		p := NewOpenAIProvider(cfg.OpenAIBaseURL, cfg.OpenAIAPIKey, cfg.LLMModel, cfg.LLMReasoningEffort, cfg.LLMTimeout)
		p.maxInputBytes, p.maxOutputTokens = cmp.Or(cfg.LLMMaxInputBytes, p.maxInputBytes), cmp.Or(cfg.LLMMaxOutputTokens, p.maxOutputTokens)
		return p
	case "anthropic":
		p := NewAnthropicProvider(cfg.AnthropicBaseURL, cfg.AnthropicAPIKey, cfg.LLMModel, cfg.LLMReasoningEffort, cfg.LLMTimeout)
		p.maxInputBytes, p.maxOutputTokens = cmp.Or(cfg.LLMMaxInputBytes, p.maxInputBytes), cmp.Or(cfg.LLMMaxOutputTokens, p.maxOutputTokens)
		return p
	case "gemini":
		p := NewGeminiProvider(cfg.GeminiBaseURL, cfg.GeminiAPIKey, cfg.LLMModel, cfg.LLMReasoningEffort, cfg.LLMTimeout)
		p.maxInputBytes, p.maxOutputTokens = cmp.Or(cfg.LLMMaxInputBytes, p.maxInputBytes), cmp.Or(cfg.LLMMaxOutputTokens, p.maxOutputTokens)
		return p
	default:
		return RulesProvider{}
	}
}

// ErrModelUnavailable means the configured key cannot use LLM_MODEL: the
// provider rejected the key, or its model list does not include the model.
var ErrModelUnavailable = errors.New("model not available")

// CheckModel confirms that the configured model is in the provider's list for
// its key. The rules analyzer has nothing to check. A list that cannot be read
// returns another error, so callers can tell a wrong model or key from an outage.
func CheckModel(ctx context.Context, p Provider, model string) error {
	lister, ok := p.(interface {
		Models(context.Context) ([]string, error)
	})
	if !ok {
		return nil
	}
	models, err := lister.Models(ctx)
	var se *statusError
	if errors.As(err, &se) && (se.code == http.StatusUnauthorized || se.code == http.StatusForbidden) {
		return fmt.Errorf("%w: the provider rejected the API key (status %d); check the key for LLM_PROVIDER", ErrModelUnavailable, se.code)
	}
	if err != nil {
		return fmt.Errorf("list models: %w", redact.Error(err))
	}
	if slices.Contains(models, model) {
		return nil
	}
	slices.Sort(models)
	shown := models
	if len(shown) > 60 {
		shown = shown[:60]
	}
	return fmt.Errorf("%w: LLM_MODEL %q is not in %s's list for this key (%d models): %s",
		ErrModelUnavailable, model, strings.SplitN(p.Name(), "/", 2)[0], len(models), strings.Join(shown, ", "))
}

// ErrInputTooLarge prevents sending requests that exceed the configured byte budget.
var ErrInputTooLarge = errors.New("LLM request exceeds LLM_MAX_INPUT_BYTES")

// ErrInvalidOutput marks responses that are not a well-formed analysis.
var ErrInvalidOutput = errors.New("invalid model output")

// decodeAnalysis strictly parses model output: one JSON object, as the
// providers' structured output returns it, with nothing around it.
func decodeAnalysis(content string) (domain.Analysis, error) {
	s := strings.TrimSpace(content)
	dec := json.NewDecoder(strings.NewReader(s))
	dec.DisallowUnknownFields()
	var a domain.Analysis
	if err := dec.Decode(&a); err != nil {
		return domain.Analysis{}, fmt.Errorf("%w: %w", ErrInvalidOutput, err)
	}
	if dec.More() {
		return domain.Analysis{}, fmt.Errorf("%w: trailing data after JSON object", ErrInvalidOutput)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(s), &fields); err != nil {
		return domain.Analysis{}, fmt.Errorf("%w: %w", ErrInvalidOutput, err)
	}
	for field := range fields {
		if strings.EqualFold(field, "confidence") {
			return domain.Analysis{}, fmt.Errorf("%w: confidence is not supported in v2", ErrInvalidOutput)
		}
	}
	if a.SchemaVersion != 2 {
		return domain.Analysis{}, fmt.Errorf("%w: expected v2 without confidence", ErrInvalidOutput)
	}
	return a.Redacted(), nil
}

// postJSON sends body to url and decodes a 2xx JSON response into out.
// Error bodies are truncated so a misbehaving endpoint cannot flood logs.
func postJSON(ctx context.Context, client *http.Client, url string, headers map[string]string, body, out any, maxBytes int) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	if len(payload) > maxBytes {
		return ErrInputTooLarge
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	return send(client, req, headers, out)
}

// getJSON fetches url and decodes a 2xx JSON response into out.
func getJSON(ctx context.Context, client *http.Client, url string, headers map[string]string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	return send(client, req, headers, out)
}

func send(client *http.Client, req *http.Request, headers map[string]string, out any) error {
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		return &statusError{resp.StatusCode, truncate(redactedBody(data), 300)}
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("%w: decode response envelope: %w", ErrInvalidOutput, err)
	}
	return nil
}

// statusError is a non-2xx provider response; body is masked and truncated.
type statusError struct {
	code int
	body string
}

func (e *statusError) Error() string { return fmt.Sprintf("unexpected status %d: %s", e.code, e.body) }

// effortHint names the likely fix when a request with an effort parameter is
// rejected: models without reasoning refuse it.
func effortHint(err error, effort string) error {
	var se *statusError
	if effort != "" && errors.As(err, &se) && se.code == http.StatusBadRequest {
		return fmt.Errorf("%w (a model without reasoning needs LLM_REASONING_EFFORT=off)", err)
	}
	return err
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func redactedBody(data []byte) string {
	if clean, err := redact.JSON(data); err == nil {
		return string(clean)
	}
	return redact.Text(string(data))
}
