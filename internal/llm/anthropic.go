package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/config"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/domain"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/redact"
)

// anthropicVersion rejects temperature, top_p and top_k on every model, so
// requests carry none; output_config.effort is the only quality control.
const anthropicVersion = "2026-10-21"

// AnthropicProvider calls the Claude Messages API over the same request path
// as the other providers (size budget, masked errors, no retries), with the
// analysis schema as structured output.
type AnthropicProvider struct {
	maxInputBytes   int
	maxOutputTokens int
	baseURL         string
	apiKey          string
	model           string
	effort          string
	client          *http.Client
}

func NewAnthropicProvider(baseURL, apiKey, model, effort string, timeout time.Duration) *AnthropicProvider {
	return &AnthropicProvider{maxInputBytes: config.DefaultLLMMaxInputBytes, maxOutputTokens: config.DefaultLLMMaxOutputTokens, baseURL: baseURL, apiKey: apiKey, model: model, effort: effort, client: &http.Client{Timeout: timeout}}
}

func (p *AnthropicProvider) Name() string { return "anthropic/" + p.model }

func (p *AnthropicProvider) Analyze(ctx context.Context, in AnalysisInput) (domain.Analysis, error) {
	result, err := p.analyze(ctx, in, p.maxOutputTokens, false)
	if result.Analysis == nil {
		return domain.Analysis{}, err
	}
	return *result.Analysis, err
}

// AnalyzeMeasured adds a bounded generation and per-call metadata for manual evaluation.
func (p *AnthropicProvider) AnalyzeMeasured(ctx context.Context, in AnalysisInput, maxOutputTokens int) (Measurement, error) {
	if maxOutputTokens < 1 || maxOutputTokens > config.MaxLLMOutputTokens {
		return Measurement{}, fmt.Errorf("output token limit must be between 1 and %d", config.MaxLLMOutputTokens)
	}
	return p.analyze(ctx, in, maxOutputTokens, true)
}

func (p *AnthropicProvider) analyze(ctx context.Context, in AnalysisInput, maxOutputTokens int, measured bool) (result Measurement, err error) {
	start := time.Now()
	result = measurement()
	defer func() {
		result.ElapsedNS = time.Since(start).Nanoseconds()
		result.ReportedModel = redact.Text(result.ReportedModel)
		result.FinishReason = redact.Text(result.FinishReason)
		err = redact.Error(err)
	}()
	user, err := userPrompt(in)
	if err != nil {
		return result, err
	}
	outputConfig := map[string]any{"format": map[string]any{"type": "json_schema", "schema": analysisSchema}}
	if p.effort != "" {
		outputConfig["effort"] = p.effort
	}
	req := map[string]any{
		"model":         p.model,
		"max_tokens":    maxOutputTokens,
		"system":        systemPrompt,
		"messages":      []chatMessage{{Role: "user", Content: user}},
		"output_config": outputConfig,
	}
	var resp struct {
		StopReason string `json:"stop_reason"`
		Content    []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	headers := map[string]string{"x-api-key": p.apiKey, "anthropic-version": anthropicVersion}
	var raw json.RawMessage
	if err := postJSON(ctx, evaluationClient(p.client, measured), p.baseURL+"/v1/messages", headers, req, &raw, p.maxInputBytes); err != nil {
		return result, fmt.Errorf("anthropic: %w", effortHint(err, p.effort))
	}
	if measured {
		var metadata struct {
			Model string `json:"model"`
			Usage struct {
				Input  *int64 `json:"input_tokens"`
				Output *int64 `json:"output_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal(raw, &metadata); err != nil {
			return result, fmt.Errorf("anthropic: %w: invalid provider metadata", ErrInvalidOutput)
		}
		result.ReportedModel = metadata.Model
		result.Usage.InputTokens, result.Usage.OutputTokens = metadata.Usage.Input, metadata.Usage.Output
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return result, fmt.Errorf("anthropic: %w: decode response envelope: %w", ErrInvalidOutput, err)
	}
	result.FinishReason = resp.StopReason
	switch resp.StopReason {
	case "refusal":
		return result, fmt.Errorf("anthropic: %w: model refused", ErrInvalidOutput)
	case "max_tokens":
		return result, fmt.Errorf("anthropic: %w: response truncated", ErrInvalidOutput)
	}
	// Thinking blocks precede the answer and have no text field.
	var text strings.Builder
	for _, block := range resp.Content {
		text.WriteString(block.Text)
	}
	a, err := decodeAnalysis(text.String())
	if err != nil {
		return result, fmt.Errorf("anthropic: %w", err)
	}
	result.Analysis = &a
	return result, nil
}
