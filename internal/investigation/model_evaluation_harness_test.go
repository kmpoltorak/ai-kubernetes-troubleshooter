package investigation

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/domain"
	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/llm"
)

func evaluationEnvironment(endpoint, provider string) map[string]string {
	return map[string]string{"MODEL_EVAL_ALLOW_NETWORK": "true", "MODEL_EVAL_PROVIDER": provider, "MODEL_EVAL_MODEL": "synthetic-model", "OPENAI_API_KEY": "opaque-synthetic-credential", "ANTHROPIC_API_KEY": "opaque-synthetic-credential", "OPENAI_BASE_URL": endpoint, "ANTHROPIC_BASE_URL": endpoint, "GEMINI_API_KEY": "opaque-synthetic-credential", "GEMINI_BASE_URL": endpoint}
}

func TestModelEvaluationRequiresOptIn(t *testing.T) {
	var requests atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { requests.Add(1); w.WriteHeader(500) }))
	defer srv.Close()
	env := evaluationEnvironment(srv.URL, "openai")
	delete(env, "MODEL_EVAL_ALLOW_NETWORK")
	keyRead := false
	_, err := runModelEvaluation(context.Background(), func(key string) string {
		if key == "OPENAI_API_KEY" {
			keyRead = true
		}
		return env[key]
	}, func([]byte) error { return nil })
	if err == nil || requests.Load() != 0 || keyRead {
		t.Fatalf("opt-in bypassed: requests=%d keyRead=%t error=%v", requests.Load(), keyRead, err)
	}
}

func TestModelEvaluationRejectsConfigurationBeforeRequests(t *testing.T) {
	for _, tc := range []struct{ key, value string }{
		{"MODEL_EVAL_ALLOW_NETWORK", "yes"}, {"MODEL_EVAL_PROVIDER", "rules"}, {"MODEL_EVAL_PROVIDER", "ollama"}, {"MODEL_EVAL_MODEL", ""}, {"MODEL_EVAL_MODEL", "m\nsecret"}, {"OPENAI_API_KEY", ""},
		{"MODEL_EVAL_TIMEOUT_SECONDS", "0"}, {"MODEL_EVAL_TIMEOUT_SECONDS", "601"}, {"MODEL_EVAL_TIMEOUT_SECONDS", "1.5"},
		{"OPENAI_BASE_URL", "https://user:password@example.com"}, {"OPENAI_BASE_URL", "https://example.com?token=synthetic"}, {"OPENAI_BASE_URL", "https://example.com#token"}, {"OPENAI_BASE_URL", "http://example.com"}, {"OPENAI_BASE_URL", "file:///tmp/api"}, {"OPENAI_BASE_URL", "https://example.com:99999"},
	} {
		t.Run(tc.key+"/"+tc.value, func(t *testing.T) {
			env := evaluationEnvironment("http://127.0.0.1:1", "openai")
			env[tc.key] = tc.value
			_, err := runModelEvaluation(context.Background(), func(key string) string { return env[key] }, func([]byte) error { t.Fatal("invalid configuration emitted results"); return nil })
			if err == nil || strings.Contains(err.Error(), "request failed") || strings.Contains(err.Error(), "password@example.com") {
				t.Fatalf("configuration reached network or leaked input: %v", err)
			}
		})
	}
}

// The fake endpoint derives its response from the actual evidence in the request,
// allowing the entire measured-provider/engine path to exercise real validation.
func evaluationServer(t *testing.T, provider string, change func(*domain.Analysis)) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	requests := &atomic.Int64{}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		var request struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
			MaxTokens          int `json:"max_completion_tokens"`
			AnthropicMaxTokens int `json:"max_tokens"`
			Contents           []struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"contents"`
			GenerationConfig struct {
				MaxOutputTokens int `json:"maxOutputTokens"`
			} `json:"generationConfig"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("invalid request: %v", err)
			w.WriteHeader(500)
			return
		}
		// The evidence is the last user message; system prompts travel separately
		// except on OpenAI.
		var user string
		var limit int
		switch {
		case provider == "openai" && len(request.Messages) == 2:
			user, limit = request.Messages[1].Content, request.MaxTokens
		case provider == "anthropic" && len(request.Messages) == 1:
			user, limit = request.Messages[0].Content, request.AnthropicMaxTokens
		case provider == "gemini" && len(request.Contents) == 1 && len(request.Contents[0].Parts) == 1:
			user, limit = request.Contents[0].Parts[0].Text, request.GenerationConfig.MaxOutputTokens
		default:
			t.Errorf("unexpected %s request shape", provider)
			w.WriteHeader(500)
			return
		}
		if limit != 4096 {
			t.Error("unbounded evaluation request")
			w.WriteHeader(500)
			return
		}
		var prompt struct {
			Incident    domain.Incident                  `json:"incident"`
			Evidence    []domain.Evidence                `json:"evidence"`
			Failed      []struct{ Source, Error string } `json:"failed_diagnostics"`
			Limitations []string                         `json:"limitations"`
		}
		if err := json.Unmarshal([]byte(user), &prompt); err != nil {
			t.Error(err)
			w.WriteHeader(500)
			return
		}
		in := llm.AnalysisInput{Incident: prompt.Incident, Evidence: prompt.Evidence, Limitations: prompt.Limitations}
		for _, f := range prompt.Failed {
			in.Failed = append(in.Failed, domain.ToolExecution{ToolName: f.Source, Error: f.Error})
		}
		a, err := (llm.RulesProvider{}).Analyze(r.Context(), in)
		if err != nil {
			t.Error(err)
			w.WriteHeader(500)
			return
		}
		if change != nil {
			change(&a)
		}
		content, err := json.Marshal(a)
		if err != nil {
			t.Error(err)
			w.WriteHeader(500)
			return
		}
		var body any
		switch provider {
		case "openai":
			body = map[string]any{"model": "reported-opaque-synthetic-credential", "usage": map[string]int{"prompt_tokens": 12, "completion_tokens": 0, "total_tokens": 12}, "choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]string{"content": string(content)}}}}
			if r.Header.Get("Authorization") == "Bearer a" {
				// A counter above float64's exact integer range exercises output masking.
				body.(map[string]any)["usage"] = map[string]int64{"prompt_tokens": 12, "completion_tokens": 0, "total_tokens": 9007199254740993}
			}
		case "gemini":
			body = map[string]any{"modelVersion": "reported", "usageMetadata": map[string]int{"promptTokenCount": 12, "candidatesTokenCount": 0, "totalTokenCount": 12}, "candidates": []any{map[string]any{"finishReason": "STOP", "content": map[string]any{"role": "model", "parts": []any{map[string]any{"text": "considering", "thought": true}, map[string]string{"text": string(content)}}}}}}
		default:
			body = map[string]any{"model": "reported", "stop_reason": "end_turn", "usage": map[string]int{"input_tokens": 12, "output_tokens": 0}, "content": []any{map[string]string{"type": "text", "text": string(content)}}}
		}
		if err := json.NewEncoder(w).Encode(body); err != nil {
			t.Error(err)
		}
	})), requests
}

func TestModelEvaluationRunsBoundedCorpus(t *testing.T) {
	for _, provider := range []string{"openai", "anthropic", "gemini"} {
		t.Run(provider, func(t *testing.T) {
			srv, calls := evaluationServer(t, provider, nil)
			defer srv.Close()
			env := evaluationEnvironment(srv.URL, provider)
			var results []modelEvaluationResult
			summary, err := runModelEvaluation(context.Background(), func(key string) string { return env[key] }, func(line []byte) error {
				if strings.Contains(string(line), env["OPENAI_API_KEY"]) {
					t.Fatal("configured credential leaked")
				}
				var result modelEvaluationResult
				if err := json.Unmarshal(line, &result); err != nil {
					return err
				}
				if result.Kind == "attempt" {
					results = append(results, result)
				}
				return nil
			})
			if err != nil || !summary.AutomatedPassed || summary.Qualified || summary.HumanReview != "pending" || summary.Attempts != 18 || len(results) != 18 || calls.Load() != 18 {
				t.Fatalf("summary=%+v attempts=%d calls=%d error=%v", summary, len(results), calls.Load(), err)
			}
			seen := map[string]int{}
			ids := map[string]bool{}
			for _, r := range results {
				seen[r.Case]++
				if r.Repeat != seen[r.Case] || r.Repeat > 3 || r.Measurement.Analysis == nil || r.EngineAnalysis == nil || len(r.Violations) != 0 || r.Config.MaxOutputTokens != 4096 || r.Config.TimeoutSeconds != 60 || r.Config.MaxInputBytes != 262144 {
					t.Fatalf("invalid trial: %+v", r)
				}
				for _, e := range r.Input.Evidence {
					if ids[e.ID] {
						t.Fatal("evidence shared between trials")
					}
					ids[e.ID] = true
				}
				if r.Case == "denied_logs" && (len(r.Input.Failed) == 0 || len(r.Measurement.Analysis.MissingData) == 0 || r.EngineAnalysis.Verdict == "no_fault_observed") {
					t.Fatal("failure data lost or false healthy claim")
				}
			}
			if len(seen) != 6 {
				t.Fatalf("cases=%v", seen)
			}
		})
	}
}

func TestModelEvaluationKeepsFailuresAndRawOmissions(t *testing.T) {
	for _, failure := range []string{"unsafe_command", "omitted_context"} {
		t.Run(failure, func(t *testing.T) {
			srv, calls := evaluationServer(t, "openai", func(a *domain.Analysis) {
				if failure == "unsafe_command" {
					a.SafeCommands = []string{"kubectl delete pods --all"}
				} else {
					a.MissingData, a.Limitations = nil, nil
				}
			})
			defer srv.Close()
			env := evaluationEnvironment(srv.URL, "openai")
			var results []modelEvaluationResult
			summary, err := runModelEvaluation(context.Background(), func(key string) string { return env[key] }, func(line []byte) error {
				var r modelEvaluationResult
				if err := json.Unmarshal(line, &r); err != nil {
					return err
				}
				if r.Kind == "attempt" {
					results = append(results, r)
				}
				return nil
			})
			if err != nil || summary.AutomatedPassed || summary.Qualified || len(results) != 18 || calls.Load() != 18 {
				t.Fatalf("failed trials hidden or retried: %+v %v", summary, err)
			}
			for _, r := range results {
				if len(r.Violations) == 0 || r.Measurement.Usage.InputTokens == nil || *r.Measurement.Usage.InputTokens != 12 {
					t.Fatal("violation or usage discarded")
				}
				if failure == "unsafe_command" && (r.EngineAnalysis != nil || r.ErrorClass != "invalid_output") {
					t.Fatal("unsafe report admitted")
				}
				if failure == "omitted_context" && (r.EngineAnalysis == nil || len(r.EngineAnalysis.Limitations) == 0 || len(r.Measurement.Analysis.Limitations) != 0) {
					t.Fatal("engine augmentation concealed raw omission")
				}
			}
		})
	}
}

func TestModelEvaluationKeepsTransportFailures(t *testing.T) {
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		http.Error(w, "opaque-synthetic-credential", http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	env := evaluationEnvironment(srv.URL, "openai")
	failures := 0
	summary, err := runModelEvaluation(context.Background(), func(key string) string { return env[key] }, func(line []byte) error {
		if strings.Contains(string(line), env["OPENAI_API_KEY"]) {
			t.Fatal("transport response leaked")
		}
		var r modelEvaluationResult
		if err := json.Unmarshal(line, &r); err != nil {
			return err
		}
		if r.Kind == "attempt" {
			failures++
			if r.ErrorClass != "request_or_engine_failure" || r.Measurement.Analysis != nil || r.Measurement.Usage.InputTokens != nil || r.EngineAnalysis != nil {
				t.Fatal("failed request fabricated a report or usage")
			}
		}
		return nil
	})
	if err != nil || summary.AutomatedPassed || failures != 18 || calls.Load() != 18 {
		t.Fatalf("failures hidden or retried: %+v failures=%d calls=%d error=%v", summary, failures, calls.Load(), err)
	}
}

func TestModelEvaluationMasksCredentialWithoutChangingFieldsOrCounters(t *testing.T) {
	srv, calls := evaluationServer(t, "openai", nil)
	defer srv.Close()
	env := evaluationEnvironment(srv.URL, "openai")
	env["OPENAI_API_KEY"] = "a"
	checked := 0
	summary, err := runModelEvaluation(context.Background(), func(key string) string { return env[key] }, func(line []byte) error {
		var r modelEvaluationResult
		if err := json.Unmarshal(line, &r); err != nil {
			return err
		}
		if r.Kind == "attempt" {
			checked++
			if r.Case == "" || r.Measurement.Usage.TotalTokens == nil || *r.Measurement.Usage.TotalTokens != 9007199254740993 || strings.Contains(r.Measurement.ReportedModel, "a") || !strings.Contains(r.Measurement.ReportedModel, "[REDACTED]") {
				t.Fatal("credential masking changed field names/counters or missed a value")
			}
		}
		return nil
	})
	if err != nil || !summary.AutomatedPassed || checked != 18 || calls.Load() != 18 {
		t.Fatalf("summary=%+v checked=%d error=%v", summary, checked, err)
	}
}

func TestModelEvaluationStopsWithoutMoreRequests(t *testing.T) {
	for _, mode := range []string{"cancel", "output_failure"} {
		t.Run(mode, func(t *testing.T) {
			srv, calls := evaluationServer(t, "openai", nil)
			defer srv.Close()
			env := evaluationEnvironment(srv.URL, "openai")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			outputError := errors.New("output unavailable")
			summary, err := runModelEvaluation(ctx, func(key string) string { return env[key] }, func([]byte) error {
				if mode == "cancel" {
					cancel()
					return nil
				}
				return outputError
			})
			if calls.Load() != 1 || summary.Attempts != 1 || summary.AutomatedPassed || err == nil {
				t.Fatalf("continued after stop: calls=%d summary=%+v error=%v", calls.Load(), summary, err)
			}
			if mode == "output_failure" && !errors.Is(err, outputError) {
				t.Fatal("output error lost")
			}
			if mode == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatal("cancellation lost")
			}
		})
	}
}

func TestModelEvaluationReasoningEffort(t *testing.T) {
	env := evaluationEnvironment("https://api.example.com/v1", "openai")
	env["MODEL_EVAL_REASONING_EFFORT"] = "medium"
	cfg, err := loadModelEvaluation(func(k string) string { return env[k] })
	if err != nil || cfg.ReasoningEffort != "medium" || cfg.Temperature != nil {
		t.Fatalf("reasoning config: %+v %v", cfg, err)
	}
	env["MODEL_EVAL_REASONING_EFFORT"] = "Medium!"
	if _, err := loadModelEvaluation(func(k string) string { return env[k] }); err == nil {
		t.Fatal("malformed reasoning effort accepted")
	}
	env = evaluationEnvironment("https://api.example.com/v1", "openai")
	if cfg, err := loadModelEvaluation(func(k string) string { return env[k] }); err != nil || cfg.Temperature == nil || *cfg.Temperature != 0 {
		t.Fatalf("default config: %+v %v", cfg, err)
	}
}

func TestModelEvaluationAnthropicConfig(t *testing.T) {
	env := evaluationEnvironment("", "anthropic")
	delete(env, "ANTHROPIC_BASE_URL")
	cfg, err := loadModelEvaluation(func(k string) string { return env[k] })
	if err != nil || cfg.Endpoint != "https://api.anthropic.com" || cfg.Temperature != nil || cfg.apiKey == "" {
		t.Fatalf("anthropic config: %+v %v", cfg, err)
	}
	env["MODEL_EVAL_REASONING_EFFORT"] = "medium"
	if cfg, err := loadModelEvaluation(func(k string) string { return env[k] }); err != nil || cfg.ReasoningEffort != "medium" {
		t.Fatalf("anthropic effort: %+v %v", cfg, err)
	}
	env["ANTHROPIC_API_KEY"] = " "
	if _, err := loadModelEvaluation(func(k string) string { return env[k] }); err == nil || !strings.Contains(err.Error(), "ANTHROPIC_API_KEY") {
		t.Fatalf("missing key accepted: %v", err)
	}
}

func TestModelEvaluationTimeout(t *testing.T) {
	env := evaluationEnvironment("https://api.example.com/v1", "openai")
	env["MODEL_EVAL_TIMEOUT_SECONDS"] = "300"
	if cfg, err := loadModelEvaluation(func(k string) string { return env[k] }); err != nil || cfg.TimeoutSeconds != 300 {
		t.Fatalf("timeout not recorded: %+v %v", cfg, err)
	}
	for _, tc := range []struct {
		seconds                       int
		request, investigation, suite time.Duration
	}{{60, time.Minute, 90 * time.Second, 20 * time.Minute}, {600, 10 * time.Minute, 630 * time.Second, 182 * time.Minute}} {
		r, i, s := evaluationDeadlines(tc.seconds)
		if r != tc.request || i != tc.investigation || s != tc.suite {
			t.Fatalf("%d s: request %v investigation %v suite %v", tc.seconds, r, i, s)
		}
	}
}

func TestModelEvaluationGeminiConfig(t *testing.T) {
	env := evaluationEnvironment("", "gemini")
	delete(env, "GEMINI_BASE_URL")
	cfg, err := loadModelEvaluation(func(k string) string { return env[k] })
	if err != nil || cfg.Endpoint != "https://generativelanguage.googleapis.com/v1beta" || cfg.Temperature != nil || cfg.apiKey == "" {
		t.Fatalf("gemini config: %+v %v", cfg, err)
	}
	env["GEMINI_API_KEY"] = " "
	if _, err := loadModelEvaluation(func(k string) string { return env[k] }); err == nil || !strings.Contains(err.Error(), "GEMINI_API_KEY") {
		t.Fatalf("missing key accepted: %v", err)
	}
}
