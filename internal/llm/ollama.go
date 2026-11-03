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

// OllamaProvider calls a local Ollama server, constraining output with the
// analysis JSON schema via the "format" field.
type OllamaProvider struct {
	maxInputBytes int
	baseURL       string
	model         string
	client        *http.Client
}

func NewOllamaProvider(baseURL, model string, timeout time.Duration) *OllamaProvider {
	return &OllamaProvider{maxInputBytes: config.DefaultLLMMaxInputBytes, baseURL: baseURL, model: model, client: &http.Client{Timeout: timeout}}
}

func (p *OllamaProvider) Name() string { return "ollama/" + p.model }

func (p *OllamaProvider) Analyze(ctx context.Context, in AnalysisInput) (domain.Analysis, error) {
	result, err := p.analyze(ctx, in, 0)
	if result.Analysis == nil {
		return domain.Analysis{}, err
	}
	return *result.Analysis, err
}

// AnalyzeMeasured adds a bounded generation and per-call metadata for manual evaluation.
func (p *OllamaProvider) AnalyzeMeasured(ctx context.Context, in AnalysisInput, maxOutputTokens int) (Measurement, error) {
	if maxOutputTokens < 1 || maxOutputTokens > 4096 {
		return Measurement{}, fmt.Errorf("output token limit must be between 1 and 4096")
	}
	return p.analyze(ctx, in, maxOutputTokens)
}

func (p *OllamaProvider) analyze(ctx context.Context, in AnalysisInput, maxOutputTokens int) (result Measurement, err error) {
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
		"model":    p.model,
		"messages": msgs,
		"stream":   false,
		"format":   analysisSchema,
		"options":  map[string]any{"temperature": 0},
	}
	if maxOutputTokens > 0 {
		req["options"].(map[string]any)["num_predict"] = maxOutputTokens
	}
	var resp struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	}
	var raw json.RawMessage
	if err := postJSON(ctx, evaluationClient(p.client, maxOutputTokens), p.baseURL+"/api/chat", nil, req, &raw, p.maxInputBytes); err != nil {
		return result, fmt.Errorf("ollama: %w", err)
	}

	if maxOutputTokens > 0 {
		var metadata struct {
			Model          string `json:"model"`
			DoneReason     string `json:"done_reason"`
			Input          *int64 `json:"prompt_eval_count"`
			Output         *int64 `json:"eval_count"`
			TotalDuration  *int64 `json:"total_duration"`
			LoadDuration   *int64 `json:"load_duration"`
			InputDuration  *int64 `json:"prompt_eval_duration"`
			OutputDuration *int64 `json:"eval_duration"`
		}

		if err := json.Unmarshal(raw, &metadata); err != nil {
			return result, fmt.Errorf("ollama: %w: invalid provider metadata", ErrInvalidOutput)
		}
		result.ReportedModel, result.FinishReason = metadata.Model, metadata.DoneReason
		result.Usage = Usage{InputTokens: metadata.Input, OutputTokens: metadata.Output, TotalDurationNS: metadata.TotalDuration, LoadDurationNS: metadata.LoadDuration, InputDurationNS: metadata.InputDuration, OutputDurationNS: metadata.OutputDuration}

	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return result, fmt.Errorf("ollama: %w: decode response envelope: %w", ErrInvalidOutput, err)
	}
	if maxOutputTokens > 0 && result.FinishReason == "length" {
		return result, fmt.Errorf("ollama: %w: response truncated", ErrInvalidOutput)
	}
	a, err := decodeAnalysis(resp.Message.Content)
	if err != nil {
		return result, fmt.Errorf("ollama: %w", err)
	}
	result.Analysis = &a
	return result, nil
}
