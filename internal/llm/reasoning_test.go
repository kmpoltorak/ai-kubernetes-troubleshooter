package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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
