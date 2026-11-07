package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/config"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/domain"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/redact"
)

// OpenAIProvider calls the Chat Completions API (or any compatible endpoint)
// with a strict JSON schema response format.
// Reasoning models get reasoning_effort and no temperature, which they reject;
// with effort off, other models get temperature 0.
type OpenAIProvider struct {
	maxInputBytes   int
	maxOutputTokens int
	baseURL         string
	apiKey          string
	model           string
	effort          string
	client          *http.Client
}

func NewOpenAIProvider(baseURL, apiKey, model, effort string, timeout time.Duration) *OpenAIProvider {
	return &OpenAIProvider{maxInputBytes: config.DefaultLLMMaxInputBytes, maxOutputTokens: config.DefaultLLMMaxOutputTokens, baseURL: baseURL, apiKey: apiKey, model: model, effort: effort, client: &http.Client{Timeout: timeout}}
}

func (p *OpenAIProvider) Name() string { return "openai/" + p.model }

func (p *OpenAIProvider) Analyze(ctx context.Context, in AnalysisInput) (domain.Analysis, error) {
	result, err := p.analyze(ctx, in, p.maxOutputTokens, false)
	if result.Analysis == nil {
		return domain.Analysis{}, err
	}
	return *result.Analysis, err
}

// AnalyzeMeasured adds a bounded generation and per-call metadata for manual evaluation.
func (p *OpenAIProvider) AnalyzeMeasured(ctx context.Context, in AnalysisInput, maxOutputTokens int) (Measurement, error) {
	if maxOutputTokens < 1 || maxOutputTokens > config.MaxLLMOutputTokens {
		return Measurement{}, fmt.Errorf("output token limit must be between 1 and %d", config.MaxLLMOutputTokens)
	}
	return p.analyze(ctx, in, maxOutputTokens, true)
}

func (p *OpenAIProvider) analyze(ctx context.Context, in AnalysisInput, maxOutputTokens int, measured bool) (result Measurement, err error) {
	start := time.Now()
	result = measurement()
	defer func() {
		result.ElapsedNS = time.Since(start).Nanoseconds()
		result.ReportedModel = redact.Text(result.ReportedModel)
		result.FinishReason = redact.Text(result.FinishReason)
		err = redact.Error(err)
	}()
	msgs, err := messages(in)
	if err != nil {
		return result, err
	}
	req := map[string]any{
		"model":                 p.model,
		"messages":              msgs,
		"max_completion_tokens": maxOutputTokens,
		"response_format": map[string]any{
			"type": "json_schema",
			"json_schema": map[string]any{
				"name":   "kubernetes_incident_analysis",
				"strict": true,
				"schema": analysisSchema,
			},
		},
	}
	if p.effort != "" {
		req["reasoning_effort"] = p.effort
	} else {
		req["temperature"] = 0
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
	var raw json.RawMessage
	if err := postJSON(ctx, evaluationClient(p.client, measured), p.baseURL+"/chat/completions", headers, req, &raw, p.maxInputBytes); err != nil {
		return result, fmt.Errorf("openai: %w", effortHint(err, p.effort))
	}

	if measured {
		var metadata struct {
			Model string `json:"model"`
			Usage struct {
				Input  *int64 `json:"prompt_tokens"`
				Output *int64 `json:"completion_tokens"`
				Total  *int64 `json:"total_tokens"`
			} `json:"usage"`
		}

		if err := json.Unmarshal(raw, &metadata); err != nil {
			return result, fmt.Errorf("openai: %w: invalid provider metadata", ErrInvalidOutput)
		}
		result.ReportedModel = metadata.Model
		result.Usage.InputTokens, result.Usage.OutputTokens, result.Usage.TotalTokens = metadata.Usage.Input, metadata.Usage.Output, metadata.Usage.Total

	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return result, fmt.Errorf("openai: %w: decode response envelope: %w", ErrInvalidOutput, err)
	}
	if len(resp.Choices) == 0 {
		return result, fmt.Errorf("openai: %w: no choices returned", ErrInvalidOutput)
	}
	c := resp.Choices[0]
	result.FinishReason = c.FinishReason
	switch {
	case c.Message.Refusal != "":
		return result, fmt.Errorf("openai: %w: model refused: %s", ErrInvalidOutput, truncate(redact.Text(c.Message.Refusal), 200))
	case c.FinishReason == "length":
		return result, fmt.Errorf("openai: %w: response truncated", ErrInvalidOutput)
	}
	a, err := decodeAnalysis(c.Message.Content)
	if err != nil {
		return result, fmt.Errorf("openai: %w", err)
	}
	result.Analysis = &a
	return result, nil
}
