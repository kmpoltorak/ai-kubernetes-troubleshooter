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
	for _, provider := range []string{"openai", "ollama"} {
		t.Run(provider, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				body := map[string]any{"model": 42, "usage": "unsupported", "prompt_eval_count": "unsupported", "done_reason": false}
				if provider == "openai" {
					body["choices"] = []any{map[string]any{"message": map[string]string{"content": validJSON}}}
				} else {
					body["message"] = map[string]string{"content": validJSON}
				}
				if err := json.NewEncoder(w).Encode(body); err != nil {
					t.Error(err)
				}
			}))
			defer srv.Close()
			var p Provider
			if provider == "openai" {
				p = NewOpenAIProvider(srv.URL, "synthetic", "m", time.Second)
			} else {
				p = NewOllamaProvider(srv.URL, "m", time.Second)
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
