package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/config"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/domain"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/redact"
)

// GeminiProvider calls the Gemini generateContent API over the same request
// path as the other providers (size budget, masked errors, no retries), with
// the analysis schema as responseJsonSchema. No temperature is sent: Gemini's
// thinking models are tuned for the default.
type GeminiProvider struct {
	maxInputBytes   int
	maxOutputTokens int
	baseURL         string
	apiKey          string
	model           string
	effort          string
	client          *http.Client
}

func NewGeminiProvider(baseURL, apiKey, model, effort string, timeout time.Duration) *GeminiProvider {
	return &GeminiProvider{maxInputBytes: config.DefaultLLMMaxInputBytes, maxOutputTokens: config.DefaultLLMMaxOutputTokens, baseURL: baseURL, apiKey: apiKey, model: model, effort: effort, client: &http.Client{Timeout: timeout}}
}

func (p *GeminiProvider) Name() string { return "gemini/" + p.model }

func (p *GeminiProvider) Analyze(ctx context.Context, in AnalysisInput) (domain.Analysis, error) {
	result, err := p.analyze(ctx, in, p.maxOutputTokens, false)
	if result.Analysis == nil {
		return domain.Analysis{}, err
	}
	return *result.Analysis, err
}

// AnalyzeMeasured adds a bounded generation and per-call metadata for manual evaluation.
func (p *GeminiProvider) AnalyzeMeasured(ctx context.Context, in AnalysisInput, maxOutputTokens int) (Measurement, error) {
	if maxOutputTokens < 1 || maxOutputTokens > config.MaxLLMOutputTokens {
		return Measurement{}, fmt.Errorf("output token limit must be between 1 and %d", config.MaxLLMOutputTokens)
	}
	return p.analyze(ctx, in, maxOutputTokens, true)
}

type geminiContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []geminiPart `json:"parts"`
}

type geminiPart struct {
	Text    string `json:"text"`
	Thought bool   `json:"thought,omitempty"`
}

func (p *GeminiProvider) analyze(ctx context.Context, in AnalysisInput, maxOutputTokens int, measured bool) (result Measurement, err error) {
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
	generation := map[string]any{
		"responseMimeType":   "application/json",
		"responseJsonSchema": analysisSchema,
		"maxOutputTokens":    maxOutputTokens,
	}
	if p.effort != "" {
		generation["thinkingConfig"] = map[string]string{"thinkingLevel": strings.ToUpper(p.effort)}
	}
	req := map[string]any{
		"systemInstruction": geminiContent{Parts: []geminiPart{{Text: systemPrompt}}},
		"contents":          []geminiContent{{Role: "user", Parts: []geminiPart{{Text: user}}}},
		"generationConfig":  generation,
	}
	var resp struct {
		Candidates []struct {
			FinishReason string        `json:"finishReason"`
			Content      geminiContent `json:"content"`
		} `json:"candidates"`
		PromptFeedback struct {
			BlockReason string `json:"blockReason"`
		} `json:"promptFeedback"`
	}
	endpoint := p.baseURL + "/models/" + url.PathEscape(p.model) + ":generateContent"
	var raw json.RawMessage
	if err := postJSON(ctx, evaluationClient(p.client, measured), endpoint, map[string]string{"x-goog-api-key": p.apiKey}, req, &raw, p.maxInputBytes); err != nil {
		return result, fmt.Errorf("gemini: %w", err)
	}
	if measured {
		var metadata struct {
			ModelVersion string `json:"modelVersion"`
			Usage        struct {
				Input  *int64 `json:"promptTokenCount"`
				Output *int64 `json:"candidatesTokenCount"`
				Total  *int64 `json:"totalTokenCount"`
			} `json:"usageMetadata"`
		}
		if err := json.Unmarshal(raw, &metadata); err != nil {
			return result, fmt.Errorf("gemini: %w: invalid provider metadata", ErrInvalidOutput)
		}
		result.ReportedModel = metadata.ModelVersion
		result.Usage.InputTokens, result.Usage.OutputTokens, result.Usage.TotalTokens = metadata.Usage.Input, metadata.Usage.Output, metadata.Usage.Total
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return result, fmt.Errorf("gemini: %w: decode response envelope: %w", ErrInvalidOutput, err)
	}
	if len(resp.Candidates) == 0 {
		return result, fmt.Errorf("gemini: %w: no candidate (block reason %q)", ErrInvalidOutput, resp.PromptFeedback.BlockReason)
	}
	c := resp.Candidates[0]
	result.FinishReason = c.FinishReason
	// MAX_TOKENS, SAFETY, RECITATION and the rest end without a usable answer.
	if c.FinishReason != "STOP" {
		return result, fmt.Errorf("gemini: %w: generation stopped: %s", ErrInvalidOutput, c.FinishReason)
	}
	var text strings.Builder
	for _, part := range c.Content.Parts {
		if !part.Thought {
			text.WriteString(part.Text)
		}
	}
	a, err := decodeAnalysis(text.String())
	if err != nil {
		return result, fmt.Errorf("gemini: %w", err)
	}
	result.Analysis = &a
	return result, nil
}
