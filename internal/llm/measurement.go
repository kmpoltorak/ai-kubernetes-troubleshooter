package llm

import (
	"crypto/sha256"
	"fmt"
	"net/http"

	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/domain"
)

// Measurement belongs to one request, including requests with rejected output.
// Analysis is decoded and masked, before the engine adds authoritative context.
type Measurement struct {
	Analysis      *domain.Analysis `json:"analysis"`
	ReportedModel string           `json:"reported_model,omitempty"`
	FinishReason  string           `json:"finish_reason,omitempty"`
	ElapsedNS     int64            `json:"elapsed_ns"`
	PromptSHA256  string           `json:"system_prompt_sha256"`
	SchemaSHA256  string           `json:"schema_sha256"`
	Usage         Usage            `json:"usage"`
}

// Usage preserves reported values. Nil means absent; totals are never inferred.
type Usage struct {
	InputTokens      *int64 `json:"input_tokens"`
	OutputTokens     *int64 `json:"output_tokens"`
	TotalTokens      *int64 `json:"total_tokens"`
	TotalDurationNS  *int64 `json:"total_duration_ns"`
	LoadDurationNS   *int64 `json:"load_duration_ns"`
	InputDurationNS  *int64 `json:"input_duration_ns"`
	OutputDurationNS *int64 `json:"output_duration_ns"`
}

func measurement() Measurement {
	return Measurement{PromptSHA256: fmt.Sprintf("%x", sha256.Sum256([]byte(systemPrompt))), SchemaSHA256: fmt.Sprintf("%x", sha256.Sum256(analysisSchema))}
}

func evaluationClient(client *http.Client, outputLimit int) *http.Client {
	if outputLimit == 0 {
		return client
	}
	copy := *client
	// A measured attempt sends one request, including when the endpoint redirects.
	copy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &copy
}
