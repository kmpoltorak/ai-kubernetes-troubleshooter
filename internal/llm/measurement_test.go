package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestMeasuredProviders(t *testing.T) {
	for _, provider := range []string{"openai", "anthropic", "gemini"} {
		for _, output := range []string{"valid", "missing usage", "invalid analysis", "truncated", "refusal"} {
			t.Run(provider+"/"+output, func(t *testing.T) {
				var request map[string]any
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
						t.Error(err)
						return
					}
					content, reason := validJSON, "stop"
					if output == "invalid analysis" {
						content = "unknown prose"
					}
					if output == "refusal" {
						content = ""
					}
					if output == "truncated" {
						reason = "length"
					}
					body := map[string]any{"model": "reported password=synthetic-value"}
					switch provider {
					case "anthropic":
						reason = map[string]string{"stop": "end_turn", "length": "max_tokens"}[reason]
						if output == "refusal" {
							// A refusal is rejected even when the text happens to parse.
							content, reason = validJSON, "refusal"
						}
						body["stop_reason"] = reason
						body["content"] = []any{map[string]any{"type": "thinking", "thinking": ""}, map[string]any{"type": "text", "text": content}}
						if output != "missing usage" {
							body["usage"] = map[string]int{"input_tokens": 11, "output_tokens": 0}
						}
					case "gemini":
						reason = map[string]string{"stop": "STOP", "length": "MAX_TOKENS"}[reason]
						if output == "refusal" {
							// A safety stop is rejected even when the text happens to parse.
							content, reason = validJSON, "SAFETY"
						}
						body["modelVersion"] = body["model"]
						body["candidates"] = []any{map[string]any{"finishReason": reason, "content": map[string]any{"parts": []any{map[string]any{"text": "thinking", "thought": true}, map[string]any{"text": content}}}}}
						if output != "missing usage" {
							body["usageMetadata"] = map[string]int{"promptTokenCount": 11, "candidatesTokenCount": 0, "totalTokenCount": 11}
						}
					case "openai":
						body["choices"] = []any{map[string]any{"finish_reason": reason, "message": map[string]any{"content": content}}}
						if output == "refusal" {
							body["choices"] = []any{map[string]any{"message": map[string]any{"refusal": "declined"}}}
						}
						if output != "missing usage" {
							body["usage"] = map[string]int{"prompt_tokens": 11, "completion_tokens": 0, "total_tokens": 11}
						}
					}
					if err := json.NewEncoder(w).Encode(body); err != nil {
						t.Error(err)
					}
				}))
				defer srv.Close()
				var result Measurement
				var err error
				switch provider {
				case "openai":
					result, err = NewOpenAIProvider(srv.URL, "synthetic", "requested", time.Second).AnalyzeMeasured(context.Background(), sampleInput(), 4096)
					if request["max_completion_tokens"] != float64(4096) {
						t.Fatalf("missing token cap: %v", request)
					}
				case "anthropic":
					result, err = NewAnthropicProvider(srv.URL, "synthetic", "requested", "", time.Second).AnalyzeMeasured(context.Background(), sampleInput(), 4096)
					if request["max_tokens"] != float64(4096) {
						t.Fatalf("missing token cap: %v", request)
					}
				case "gemini":
					result, err = NewGeminiProvider(srv.URL, "synthetic", "requested", "", time.Second).AnalyzeMeasured(context.Background(), sampleInput(), 4096)
					if request["generationConfig"].(map[string]any)["maxOutputTokens"] != float64(4096) {
						t.Fatalf("missing token cap: %v", request)
					}
				}
				invalid := output == "invalid analysis" || output == "truncated" || output == "refusal"
				if errors.Is(err, ErrInvalidOutput) != invalid || (result.Analysis == nil) != invalid {
					t.Fatalf("result=%+v error=%v", result, err)
				}
				if len(result.PromptSHA256) != 64 || len(result.SchemaSHA256) != 64 || result.ElapsedNS <= 0 || strings.Contains(result.ReportedModel, "synthetic-value") {
					t.Fatalf("missing or unsafe metadata: %+v", result)
				}
				if output == "missing usage" {
					if result.Usage.InputTokens != nil || result.Usage.OutputTokens != nil {
						t.Fatal("missing usage fabricated")
					}
				} else if result.Usage.InputTokens == nil || *result.Usage.InputTokens != 11 || result.Usage.OutputTokens == nil || *result.Usage.OutputTokens != 0 {
					t.Fatalf("usage lost, including zero/invalid output: %+v", result.Usage)
				}
				if provider != "anthropic" && output != "missing usage" && (result.Usage.TotalTokens == nil || *result.Usage.TotalTokens != 11) {
					t.Fatal("reported total lost")
				}
			})
		}
	}
}

func TestMeasuredLimitsAndRedirects(t *testing.T) {
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Redirect(w, r, "/second", http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	for _, provider := range []string{"openai", "anthropic", "gemini"} {
		t.Run(provider, func(t *testing.T) {
			var analyze func(context.Context, AnalysisInput, int) (Measurement, error)
			switch provider {
			case "openai":
				analyze = NewOpenAIProvider(srv.URL, "synthetic", "m", time.Second).AnalyzeMeasured
			case "anthropic":
				analyze = NewAnthropicProvider(srv.URL, "synthetic", "m", "", time.Second).AnalyzeMeasured
			case "gemini":
				analyze = NewGeminiProvider(srv.URL, "synthetic", "m", "", time.Second).AnalyzeMeasured
			}
			for _, limit := range []int{-1, 0, 4097} {
				before := calls.Load()
				if _, err := analyze(context.Background(), sampleInput(), limit); err == nil || calls.Load() != before {
					t.Fatal("invalid limit sent a request")
				}
			}
			before := calls.Load()
			if _, err := analyze(context.Background(), sampleInput(), 4096); err == nil || calls.Load() != before+1 {
				t.Fatal("redirect followed or request retried")
			}
		})
	}
}

func TestMeasuredTimeoutKeepsFingerprint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(200 * time.Millisecond):
		}
	}))
	defer srv.Close()
	result, err := NewOpenAIProvider(srv.URL, "synthetic", "m", 20*time.Millisecond).AnalyzeMeasured(context.Background(), sampleInput(), 4096)
	if !errors.Is(err, context.DeadlineExceeded) || result.ElapsedNS <= 0 || len(result.SchemaSHA256) != 64 || result.Analysis != nil || result.Usage.InputTokens != nil {
		t.Fatalf("timeout metadata: %+v %v", result, err)
	}
}

func TestMeasuredRequestsKeepSeparateUsage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []chatMessage `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		count := 1
		if strings.Contains(req.Messages[1].Content, "second") {
			count = 2
		}
		content, _ := json.Marshal(validJSON)
		_, _ = fmt.Fprintf(w, `{"usage":{"prompt_tokens":%d},"choices":[{"message":{"content":%s}}]}`, count, content)
	}))
	defer srv.Close()
	p := NewOpenAIProvider(srv.URL, "synthetic", "m", time.Second)
	results := make(chan Measurement, 2)
	for _, title := range []string{"first", "second"} {
		go func() {
			in := sampleInput()
			in.Incident.Title = title
			r, err := p.AnalyzeMeasured(context.Background(), in, 4096)
			if err != nil {
				t.Error(err)
			}
			results <- r
		}()
	}
	a, b := <-results, <-results
	if a.Usage.InputTokens == nil || b.Usage.InputTokens == nil || *a.Usage.InputTokens+*b.Usage.InputTokens != 3 || a.Usage.InputTokens == b.Usage.InputTokens {
		t.Fatal("request metadata shared")
	}
}
