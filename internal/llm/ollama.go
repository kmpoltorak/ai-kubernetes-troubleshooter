package llm

import (
	"context"
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

func (p *OllamaProvider) Analyze(ctx context.Context, in AnalysisInput) (analysis domain.Analysis, err error) {
	defer func() { err = redact.Error(err) }()
	msgs, err := messages(in)
	if err != nil {
		return domain.Analysis{}, err
	}
	req := map[string]any{
		"model":    p.model,
		"messages": msgs,
		"stream":   false,
		"format":   analysisSchema,
		"options":  map[string]any{"temperature": 0},
	}
	var resp struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	}
	if err := postJSON(ctx, p.client, p.baseURL+"/api/chat", nil, req, &resp, p.maxInputBytes); err != nil {
		return domain.Analysis{}, fmt.Errorf("ollama: %w", err)
	}
	a, err := decodeAnalysis(resp.Message.Content)
	if err != nil {
		return domain.Analysis{}, fmt.Errorf("ollama: %w", err)
	}
	return a, nil
}
