package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/config"
)

// Reasoning models (e.g. gpt-6-luna) reject temperature 0, so they get
// reasoning_effort instead; with effort off, other models get temperature 0.
func TestOpenAIReasoningEffortReplacesTemperature(t *testing.T) {
	for _, effort := range []string{"", "medium"} {
		var body map[string]any
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewDecoder(r.Body).Decode(&body)
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"choices":[{"finish_reason":"stop","message":{"content":%q}}]}`, validJSON)
		}))
		_, _ = NewOpenAIProvider(srv.URL, "k", "m", effort, time.Second).Analyze(context.Background(), sampleInput())
		srv.Close()
		temperature, hasTemperature := body["temperature"]
		if effort == "" && (!hasTemperature || temperature != float64(0) || body["reasoning_effort"] != nil) {
			t.Fatalf("effort off: %v", body)
		}
		if effort != "" && (hasTemperature || body["reasoning_effort"] != effort) {
			t.Fatalf("effort %q: temperature=%v effort=%v", effort, temperature, body["reasoning_effort"])
		}
	}
}

// The Anthropic API version in use rejects sampling parameters; effort replaces them.
func TestAnthropicRequest(t *testing.T) {
	for _, effort := range []string{"", "medium"} {
		var body map[string]any
		var r0 *http.Request
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r0 = r
			_ = json.NewDecoder(r.Body).Decode(&body)
			fmt.Fprintf(w, `{"stop_reason":"end_turn","content":[{"type":"text","text":%q}]}`, validJSON)
		}))
		a, err := NewAnthropicProvider(srv.URL, "synthetic-key", "claude-test", effort, time.Second).Analyze(context.Background(), sampleInput())
		srv.Close()
		if err != nil || a.RootCause != "r" {
			t.Fatalf("analysis: %v", err)
		}
		if r0.URL.Path != "/v1/messages" || r0.Header.Get("x-api-key") != "synthetic-key" || r0.Header.Get("anthropic-version") != anthropicVersion {
			t.Fatalf("endpoint or headers: %s %v", r0.URL.Path, r0.Header)
		}
		for _, field := range []string{"temperature", "top_p", "top_k", "thinking"} {
			if _, ok := body[field]; ok {
				t.Fatalf("%s sent: %v", field, body)
			}
		}
		out, _ := body["output_config"].(map[string]any)
		format, _ := out["format"].(map[string]any)
		msgs, _ := body["messages"].([]any)
		if body["system"] != systemPrompt || len(msgs) != 1 || format["type"] != "json_schema" || format["schema"] == nil || body["max_tokens"] != float64(config.DefaultLLMMaxOutputTokens) {
			t.Fatalf("request shape: %v", body)
		}
		if got, ok := out["effort"]; (effort == "") == ok || (ok && got != effort) {
			t.Fatalf("effort %q: %v", effort, out)
		}
	}
}

// Gemini gets the schema as responseJsonSchema, thinkingLevel instead of a
// temperature, and only non-thought parts are read back.
func TestGeminiRequest(t *testing.T) {
	for _, effort := range []string{"", "medium"} {
		var body map[string]any
		var r0 *http.Request
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r0 = r
			_ = json.NewDecoder(r.Body).Decode(&body)
			fmt.Fprintf(w, `{"candidates":[{"finishReason":"STOP","content":{"parts":[{"text":"not json","thought":true},{"text":%q}]}}]}`, validJSON)
		}))
		a, err := NewGeminiProvider(srv.URL, "synthetic-key", "gemini/test?x", effort, time.Second).Analyze(context.Background(), sampleInput())
		srv.Close()
		if err != nil || a.RootCause != "r" {
			t.Fatalf("analysis: %v", err)
		}
		if r0.URL.EscapedPath() != "/models/gemini%2Ftest%3Fx:generateContent" || r0.URL.RawQuery != "" || r0.Header.Get("x-goog-api-key") != "synthetic-key" {
			t.Fatalf("endpoint or headers: %s ?%s %v", r0.URL.EscapedPath(), r0.URL.RawQuery, r0.Header)
		}
		gen, _ := body["generationConfig"].(map[string]any)
		system, _ := json.Marshal(body["systemInstruction"])
		contents, _ := body["contents"].([]any)
		if _, ok := gen["temperature"]; ok || gen["responseMimeType"] != "application/json" || gen["responseJsonSchema"] == nil || gen["maxOutputTokens"] != float64(config.DefaultLLMMaxOutputTokens) || len(contents) != 1 {
			t.Fatalf("request shape: %v", body)
		}
		if want, _ := json.Marshal(systemPrompt); !strings.Contains(string(system), string(want)) {
			t.Fatalf("system prompt not sent: %s", system)
		}
		thinking, ok := gen["thinkingConfig"].(map[string]any)
		if (effort == "") == ok || (ok && thinking["thinkingLevel"] != "MEDIUM") {
			t.Fatalf("effort %q: %v", effort, gen)
		}
	}
}

func TestGeminiBlockedPrompt(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"promptFeedback":{"blockReason":"SAFETY"}}`)
	}))
	defer srv.Close()
	if _, err := NewGeminiProvider(srv.URL, "k", "m", "", time.Second).Analyze(context.Background(), sampleInput()); !errors.Is(err, ErrInvalidOutput) || !strings.Contains(err.Error(), "SAFETY") {
		t.Fatalf("blocked prompt: %v", err)
	}
}

// LLM_MAX_OUTPUT_TOKENS and LLM_REASONING_EFFORT reach every provider's request.
func TestFromConfigSendsOutputLimitAndEffort(t *testing.T) {
	limits := map[string]func(map[string]any) (any, any){
		"openai": func(b map[string]any) (any, any) { return b["max_completion_tokens"], b["reasoning_effort"] },
		"anthropic": func(b map[string]any) (any, any) {
			return b["max_tokens"], b["output_config"].(map[string]any)["effort"]
		},
		"gemini": func(b map[string]any) (any, any) {
			g := b["generationConfig"].(map[string]any)
			return g["maxOutputTokens"], g["thinkingConfig"].(map[string]any)["thinkingLevel"]
		},
	}
	want := map[string]any{"openai": "high", "anthropic": "high", "gemini": "HIGH"}
	for name, read := range limits {
		var body map[string]any
		srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { _ = json.NewDecoder(r.Body).Decode(&body) }))
		cfg := config.Config{LLMProvider: name, LLMModel: "m", LLMTimeout: time.Second, LLMMaxOutputTokens: 1234, LLMReasoningEffort: "high",
			OpenAIBaseURL: srv.URL, AnthropicBaseURL: srv.URL, GeminiBaseURL: srv.URL}
		_, _ = FromConfig(cfg).Analyze(context.Background(), sampleInput())
		srv.Close()
		if limit, effort := read(body); limit != float64(1234) || effort != want[name] {
			t.Fatalf("%s: limit=%v effort=%v", name, limit, effort)
		}
	}
}
