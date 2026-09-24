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
)

type Provider interface {
	// Name identifies provider and model, e.g. "openai/gpt-4o-mini".
	Name() string
	Analyze(ctx context.Context, in AnalysisInput) (domain.Analysis, error)
}

type AnalysisInput struct {
	Incident domain.Incident
	Evidence []domain.Evidence
}

// FromConfig builds the provider selected by LLM_PROVIDER. Config
// validation guarantees the provider name and required credentials.
func FromConfig(cfg config.Config) Provider {
	switch cfg.LLMProvider {
	case "openai":
		return NewOpenAIProvider(cfg.OpenAIBaseURL, cfg.OpenAIAPIKey, cfg.LLMModel, cfg.LLMTimeout)
	case "ollama":
		return NewOllamaProvider(cfg.OllamaBaseURL, cfg.LLMModel, cfg.LLMTimeout)
	default:
		return RulesProvider{}
	}
}

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
	return a, nil
}

// postJSON sends body to url and decodes a 2xx JSON response into out.
// Error bodies are truncated so a misbehaving endpoint cannot flood logs.
func postJSON(ctx context.Context, client *http.Client, url string, headers map[string]string, body, out any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return err
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
		return fmt.Errorf("unexpected status %d: %s", resp.StatusCode, truncate(string(data), 300))
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
