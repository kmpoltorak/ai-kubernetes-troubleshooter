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
	for _, code := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		status = code
		if err := CheckModel(context.Background(), p, "gpt-6-luna"); !errors.Is(err, ErrModelUnavailable) || !strings.Contains(err.Error(), "rejected the API key") {
			t.Fatalf("status %d: rejected key not fatal: %v", code, err)
		}
	}
	status = http.StatusServiceUnavailable
	if err := CheckModel(context.Background(), p, "gpt-6-luna"); err == nil || errors.Is(err, ErrModelUnavailable) {
		t.Fatalf("outage reported as a wrong model: %v", err)
	}
	if err := CheckModel(context.Background(), RulesProvider{}, "rules-v2"); err != nil {
		t.Fatalf("rules analyzer checked: %v", err)
	}
}
