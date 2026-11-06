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
)

// Reasoning models (e.g. gpt-6-luna) reject temperature 0; they get reasoning_effort instead.
func TestOpenAIReasoningEffortReplacesTemperature(t *testing.T) {
	for _, effort := range []string{"", "medium"} {
		var body map[string]any
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewDecoder(r.Body).Decode(&body)
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"choices":[{"finish_reason":"stop","message":{"content":%q}}]}`, validJSON)
		}))
		_, _ = NewOpenAIProvider(srv.URL, "k", "m", time.Second).WithReasoningEffort(effort).Analyze(context.Background(), sampleInput())
		srv.Close()
		temperature, hasTemperature := body["temperature"]
		if effort == "" && (!hasTemperature || temperature != float64(0) || body["reasoning_effort"] != nil) {
			t.Fatalf("default request: %v", body)
		}
		if effort != "" && (hasTemperature || body["reasoning_effort"] != effort) {
			t.Fatalf("reasoning request: temperature=%v effort=%v", temperature, body["reasoning_effort"])
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
		if body["system"] != systemPrompt || len(msgs) != 1 || format["type"] != "json_schema" || format["schema"] == nil || body["max_tokens"] != float64(anthropicMaxTokens) {
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
		if _, ok := gen["temperature"]; ok || gen["responseMimeType"] != "application/json" || gen["responseJsonSchema"] == nil || gen["maxOutputTokens"] != float64(geminiMaxTokens) || len(contents) != 1 {
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
