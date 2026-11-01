package redact

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestJSONMasksWithoutLosingData(t *testing.T) {
	raw := json.RawMessage(`{"password":"x","nested":[{"accessToken":"tiny","message":"timeout password=synthetic-secret"}],"count":18446744073709551615,"ok":true,"optional":null}`)
	got, err := JSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{`"x"`, "tiny", "synthetic-secret"} {
		if strings.Contains(string(got), secret) {
			t.Fatalf("leaked %q: %s", secret, got)
		}
	}
	for _, want := range []string{`18446744073709551615`, `"ok":true`, `"optional":null`, `timeout password=[REDACTED]`} {
		if !strings.Contains(string(got), want) {
			t.Fatalf("lost %q: %s", want, got)
		}
	}
	twice, err := JSON(got)
	if err != nil || string(twice) != string(got) {
		t.Fatalf("not idempotent: %s %v", twice, err)
	}
	if !strings.Contains(string(raw), "synthetic-secret") {
		t.Fatal("modified source")
	}
}

func TestJSONRejectsMalformedData(t *testing.T) {
	for _, raw := range []string{`{"password":"do-not-echo"`, `{} {"password":"do-not-echo"}`, `{} trailing`} {
		got, err := JSON(json.RawMessage(raw))
		if err == nil || got != nil || strings.Contains(err.Error(), "do-not-echo") {
			t.Fatalf("unsafe error: %s %v", got, err)
		}
	}
	if got, err := JSON(nil); err != nil || got != nil {
		t.Fatalf("nil: %s %v", got, err)
	}
}

func TestErrorKeepsClassification(t *testing.T) {
	sentinel := errors.New("provider failed")
	original := fmt.Errorf("password=synthetic-secret: %w", sentinel)
	got := Error(original)
	if !errors.Is(got, sentinel) || strings.Contains(got.Error(), "synthetic-secret") || Error(nil) != nil {
		t.Fatalf("got %v", got)
	}
	var target *json.SyntaxError
	originalSyntax := &json.SyntaxError{Offset: 42}
	if !errors.As(Error(originalSyntax), &target) || target.Offset != 42 {
		t.Fatal("lost error type")
	}
}
