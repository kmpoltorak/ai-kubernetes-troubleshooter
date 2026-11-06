package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/config"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/domain"
)

func TestProvidersMaskDataAndLimitWholeRequest(t *testing.T) {
	for _, name := range []string{"openai", "anthropic"} {
		t.Run(name, func(t *testing.T) {
			var body []byte
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				body, _ = io.ReadAll(r.Body)
				content := strings.Replace(validJSON, `"summary":"s"`, `"summary":"password=synthetic-output"`, 1)
				switch name {
				case "openai":
					_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": content}}}})
				case "anthropic":
					_ = json.NewEncoder(w).Encode(map[string]any{"stop_reason": "end_turn", "content": []any{map[string]string{"type": "text", "text": content}}})
				}
			}))
			defer srv.Close()
			cfg := config.Config{LLMProvider: name, LLMModel: "test", LLMTimeout: time.Second, OpenAIBaseURL: srv.URL, AnthropicBaseURL: srv.URL}
			in := sampleInput()
			in.Limitations = []string{"password=synthetic-limitation"}
			in.Incident.Title = "password=synthetic-title"
			in.Incident.Description = "password=synthetic-description"
			in.Evidence[0].Summary = "password=synthetic-summary"
			in.Evidence[0].Data = json.RawMessage(`{"password":"synthetic-json","count":18446744073709551615}`)
			in.Failed = []domain.ToolExecution{{ToolName: "get_events", Error: "password=synthetic-error"}}
			a, err := FromConfig(cfg).Analyze(context.Background(), in)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(body), "synthetic-") || strings.Contains(a.Summary, "synthetic-output") || !strings.Contains(string(body), "18446744073709551615") || !strings.Contains(string(body), "payment-service") {
				t.Fatalf("unsafe request/output: %s %+v", body, a)
			}
			size := len(body)
			cfg.LLMMaxInputBytes = size
			if _, err = FromConfig(cfg).Analyze(context.Background(), in); err != nil {
				t.Fatalf("exact limit rejected: %v", err)
			}
			cfg.LLMMaxInputBytes = size - 1
			if _, err = FromConfig(cfg).Analyze(context.Background(), in); !errors.Is(err, ErrInputTooLarge) {
				t.Fatalf("want size limit, got %v", err)
			}
			if calls != 2 {
				t.Fatalf("over limit request sent: %d calls", calls)
			}
			if len(in.Incident.Description) == 0 || !strings.Contains(in.Evidence[0].Summary, "synthetic-summary") {
				t.Fatal("input modified")
			}
			in.Evidence[0].Data = json.RawMessage(`{"password":"synthetic-malformed"`)
			if _, err = FromConfig(cfg).Analyze(context.Background(), in); err == nil || strings.Contains(err.Error(), "synthetic-malformed") {
				t.Fatalf("unsafe malformed error: %v", err)
			}
			if calls != 2 {
				t.Fatal("malformed data sent")
			}
		})
	}
}

func TestProvidersMaskErrors(t *testing.T) {
	for _, name := range []string{"openai", "anthropic"} {
		for _, body := range []string{`{"error":{"password":"short-secret","message":"password=synthetic-error"}}`, "password=synthetic-error"} {
			t.Run(name+body, func(t *testing.T) {
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(500); _, _ = w.Write([]byte(body)) }))
				defer srv.Close()
				cfg := config.Config{LLMProvider: name, LLMModel: "test", LLMTimeout: time.Second, OpenAIBaseURL: srv.URL, AnthropicBaseURL: srv.URL}
				_, err := FromConfig(cfg).Analyze(context.Background(), sampleInput())
				if err == nil || strings.Contains(err.Error(), "synthetic-error") || strings.Contains(err.Error(), "short-secret") || !strings.Contains(err.Error(), "500") {
					t.Fatalf("unsafe error: %v", err)
				}
			})
		}
	}
}
