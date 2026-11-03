// Package llm turns collected diagnostic evidence into a structured analysis.
// Providers only analyze data; they have no way to run tools or commands.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/config"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/domain"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/redact"
)

type Provider interface {
	// Name identifies provider and model, e.g. "openai/gpt-4o-mini".
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
		p := NewOpenAIProvider(cfg.OpenAIBaseURL, cfg.OpenAIAPIKey, cfg.LLMModel, cfg.LLMTimeout)
		if cfg.LLMMaxInputBytes > 0 {
			p.maxInputBytes = cfg.LLMMaxInputBytes
		}
		return p
	case "ollama":
		p := NewOllamaProvider(cfg.OllamaBaseURL, cfg.LLMModel, cfg.LLMTimeout)
		if cfg.LLMMaxInputBytes > 0 {
			p.maxInputBytes = cfg.LLMMaxInputBytes
		}
		return p
	default:
		return RulesProvider{}
	}
}

// ErrInputTooLarge prevents sending requests that exceed the configured byte budget.
var ErrInputTooLarge = errors.New("LLM request exceeds LLM_MAX_INPUT_BYTES")

// ErrInvalidOutput marks responses that are not a well-formed analysis.
var ErrInvalidOutput = errors.New("invalid model output")

// decodeAnalysis strictly parses model output. Markdown code fences are
// tolerated because some local models add them despite instructions.
func decodeAnalysis(content string) (domain.Analysis, error) {
	s := strings.TrimSpace(content)
	if strings.HasPrefix(s, "```") {
		s = strings.TrimPrefix(s, "```json")
		s = strings.TrimPrefix(s, "```")
		s = strings.TrimSuffix(strings.TrimSpace(s), "```")
	}
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
		return fmt.Errorf("unexpected status %d: %s", resp.StatusCode, truncate(redactedBody(data), 300))
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("%w: decode response envelope: %w", ErrInvalidOutput, err)
	}
	return nil
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
