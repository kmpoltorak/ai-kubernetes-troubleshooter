package llm

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/domain"
)

// OpenAIProvider calls the Chat Completions API (or any compatible endpoint)
// with a strict JSON schema response format.
type OpenAIProvider struct {
	baseURL string
	apiKey  string
	model   string
	client  *http.Client
}

func NewOpenAIProvider(baseURL, apiKey, model string, timeout time.Duration) *OpenAIProvider {
	return &OpenAIProvider{baseURL: baseURL, apiKey: apiKey, model: model, client: &http.Client{Timeout: timeout}}
}

func (p *OpenAIProvider) Name() string { return "openai/" + p.model }

func (p *OpenAIProvider) Analyze(ctx context.Context, in AnalysisInput) (domain.Analysis, error) {
	msgs, err := messages(in)
	if err != nil {
		return domain.Analysis{}, err
	}
	req := map[string]any{
		"model":       p.model,
		"messages":    msgs,
		"temperature": 0,
		"response_format": map[string]any{
			"type": "json_schema",
			"json_schema": map[string]any{
				"name":   "kubernetes_incident_analysis",
				"strict": true,
				"schema": analysisSchema,
			},
		},
	}
	var resp struct {
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Content string `json:"content"`
				Refusal string `json:"refusal"`
			} `json:"message"`
		} `json:"choices"`
	}
	headers := map[string]string{"Authorization": "Bearer " + p.apiKey}
	if err := postJSON(ctx, p.client, p.baseURL+"/chat/completions", headers, req, &resp); err != nil {
		return domain.Analysis{}, fmt.Errorf("openai: %w", err)
	}
	if len(resp.Choices) == 0 {
		return domain.Analysis{}, fmt.Errorf("openai: %w: no choices returned", ErrInvalidOutput)
	}
	c := resp.Choices[0]
	switch {
	case c.Message.Refusal != "":
		return domain.Analysis{}, fmt.Errorf("openai: %w: model refused: %s", ErrInvalidOutput, truncate(c.Message.Refusal, 200))
	case c.FinishReason == "length":
		return domain.Analysis{}, fmt.Errorf("openai: %w: response truncated", ErrInvalidOutput)
	}
	a, err := decodeAnalysis(c.Message.Content)
	if err != nil {
		return domain.Analysis{}, fmt.Errorf("openai: %w", err)
	}
	return a, nil
}
