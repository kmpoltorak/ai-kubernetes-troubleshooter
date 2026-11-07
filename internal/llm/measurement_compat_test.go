package llm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestOrdinaryAnalysisIgnoresUnusedProviderMetadata(t *testing.T) {
	for _, provider := range []string{"openai", "anthropic", "gemini"} {
		t.Run(provider, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				body := map[string]any{"model": 42, "usage": "unsupported"}
				switch provider {
				case "openai":
					body["choices"] = []any{map[string]any{"message": map[string]string{"content": validJSON}}}
				case "anthropic":
					body["content"] = []any{map[string]string{"type": "text", "text": validJSON}}
				case "gemini":
					body["candidates"], body["usageMetadata"], body["modelVersion"] = []any{map[string]any{"finishReason": "STOP", "content": map[string]any{"parts": []any{map[string]string{"text": validJSON}}}}}, "unsupported", 42
				}
				if err := json.NewEncoder(w).Encode(body); err != nil {
					t.Error(err)
				}
			}))
			defer srv.Close()
			var p Provider
			switch provider {
			case "openai":
				p = NewOpenAIProvider(srv.URL, "synthetic", "m", "", time.Second)
			case "anthropic":
				p = NewAnthropicProvider(srv.URL, "synthetic", "m", "", time.Second)
			case "gemini":
				p = NewGeminiProvider(srv.URL, "synthetic", "m", "", time.Second)
			}
			a, err := p.Analyze(context.Background(), sampleInput())
			if err != nil || a.RootCause != "r" {
				t.Fatalf("unused telemetry broke an otherwise valid ordinary analysis: %v", err)
			}
			measured := p.(interface {
				AnalyzeMeasured(context.Context, AnalysisInput, int) (Measurement, error)
			})
			result, err := measured.AnalyzeMeasured(context.Background(), sampleInput(), 4096)
			if !errors.Is(err, ErrInvalidOutput) || result.Analysis != nil || len(result.SchemaSHA256) != 64 {
				t.Fatalf("malformed measured metadata admitted: %+v %v", result, err)
			}
		})
	}
}
