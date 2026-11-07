package llm

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestProvidersListModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		switch {
		case r.URL.Path == "/models" && r.Header.Get("Authorization") == "Bearer k":
			fmt.Fprint(w, `{"data":[{"id":"gpt-6-luna"},{"id":"gpt-6.1-terra"}]}`)
		case r.URL.Path == "/v1/models" && r.Header.Get("x-api-key") == "k" && r.Header.Get("anthropic-version") != "":
			if q.Get("after_id") == "" {
				fmt.Fprint(w, `{"data":[{"id":"claude-haiku-5-5"}],"has_more":true,"last_id":"claude-haiku-5-5"}`)
			} else {
				fmt.Fprint(w, `{"data":[{"id":"claude-opus-5-5"}],"has_more":false,"last_id":"claude-opus-5-5"}`)
			}
		case r.URL.Path == "/gemini/models" && r.Header.Get("x-goog-api-key") == "k":
			if q.Get("pageToken") == "" {
				fmt.Fprint(w, `{"models":[{"name":"models/gemini-3.8-flash","supportedGenerationMethods":["generateContent"]},{"name":"models/gemini-3.8-flash-tts","supportedGenerationMethods":["bidiGenerateContent"]}],"nextPageToken":"p2"}`)
			} else {
				fmt.Fprint(w, `{"models":[{"name":"models/gemini-4-argon","supportedGenerationMethods":["countTokens","generateContent"]}]}`)
			}
		default:
			http.Error(w, "unexpected request", http.StatusTeapot)
		}
	}))
	defer srv.Close()
	for _, tc := range []struct {
		p interface {
			Models(context.Context) ([]string, error)
		}
		want []string
	}{
		{NewOpenAIProvider(srv.URL, "k", "m", "", time.Second), []string{"gpt-6-luna", "gpt-6.1-terra"}},
		{NewAnthropicProvider(srv.URL, "k", "m", "", time.Second), []string{"claude-haiku-5-5", "claude-opus-5-5"}},
		{NewGeminiProvider(srv.URL+"/gemini", "k", "m", "", time.Second), []string{"gemini-3.8-flash", "gemini-4-argon"}},
	} {
		got, err := tc.p.Models(context.Background())
		if err != nil || !slices.Equal(got, tc.want) {
			t.Fatalf("%T: got %v, %v; want %v", tc.p, got, err, tc.want)
		}
	}
}

func TestCheckModel(t *testing.T) {
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		fmt.Fprint(w, `{"data":[{"id":"gpt-6-luna"},{"id":"gpt-6.1-terra"}]}`)
	}))
	defer srv.Close()
	p := NewOpenAIProvider(srv.URL, "k", "m", "", time.Second)
	if err := CheckModel(context.Background(), p, "gpt-6-luna"); err != nil {
		t.Fatalf("listed model rejected: %v", err)
	}
	err := CheckModel(context.Background(), p, "gpt-typo")
	if !errors.Is(err, ErrModelUnavailable) || !strings.Contains(err.Error(), "gpt-6.1-terra") || !strings.Contains(err.Error(), `"gpt-typo"`) {
		t.Fatalf("missing model: %v", err)
	}
	status = http.StatusServiceUnavailable
	if err := CheckModel(context.Background(), p, "gpt-6-luna"); err == nil || errors.Is(err, ErrModelUnavailable) {
		t.Fatalf("outage reported as a wrong model: %v", err)
	}
	if err := CheckModel(context.Background(), RulesProvider{}, "rules-v2"); err != nil {
		t.Fatalf("rules analyzer checked: %v", err)
	}
}

// A provider repeating its cursor must not loop forever.
func TestModelPagingStopsOnRepeatedCursor(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests++; requests > 5 {
			http.Error(w, "looping", http.StatusTeapot) // fail fast instead of hanging
			return
		}
		if strings.HasSuffix(r.URL.Path, "/v1/models") {
			fmt.Fprint(w, `{"data":[{"id":"claude-opus-5-5"}],"has_more":true,"last_id":"claude-opus-5-5"}`)
		} else {
			fmt.Fprint(w, `{"models":[{"name":"models/gemini-4-argon","supportedGenerationMethods":["generateContent"]}],"nextPageToken":"same"}`)
		}
	}))
	defer srv.Close()
	for _, p := range []interface {
		Models(context.Context) ([]string, error)
	}{
		NewAnthropicProvider(srv.URL, "k", "m", "", time.Second),
		NewGeminiProvider(srv.URL+"/gemini", "k", "m", "", time.Second),
	} {
		requests = 0
		if _, err := p.Models(context.Background()); err != nil || requests != 2 {
			t.Fatalf("%T: %d requests, %v; want 2", p, requests, err)
		}
	}
}

func TestCheckModelListsAtMost60(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		ids := make([]string, 70)
		for i := range ids {
			ids[i] = fmt.Sprintf(`{"id":"m%02d"}`, i)
		}
		fmt.Fprintf(w, `{"data":[%s]}`, strings.Join(ids, ","))
	}))
	defer srv.Close()
	err := CheckModel(context.Background(), NewOpenAIProvider(srv.URL, "k", "m", "", time.Second), "gpt-typo")
	if !errors.Is(err, ErrModelUnavailable) || !strings.Contains(err.Error(), "(70 models)") || !strings.Contains(err.Error(), "m59") || strings.Contains(err.Error(), "m60") {
		t.Fatalf("model list not capped at 60: %v", err)
	}
}
