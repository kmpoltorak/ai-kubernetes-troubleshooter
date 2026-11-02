package main

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestParsePurge(t *testing.T) {
	args := []string{"--before", "2000-01-01T02:00:00+02:00"}
	got, err := parsePurge(args)
	if err != nil || got.Limit != 100 || got.Confirm != "" || !got.Before.Equal(time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("got %+v: %v", got, err)
	}
	token := strings.Repeat("a", 64)
	got, err = parsePurge(append(args, "--limit", "2", "--confirm", token))
	if err != nil || got.Limit != 2 || got.Confirm != token {
		t.Fatalf("got %+v: %v", got, err)
	}
	for _, args := range [][]string{
		{}, {"--before", "invalid"}, {"--before", "2999-01-01T00:00:00Z"},
		{"--before", "2000-01-01"}, {"--before", "0001-01-01T00:00:00Z"},
		{"--before", "2000-01-01T00:00:00Z", "extra"},
		{"--before", "2000-01-01T00:00:00Z", "--limit", "0"},
		{"--before", "2000-01-01T00:00:00Z", "--limit", "-1"},
		{"--before", "2000-01-01T00:00:00Z", "--limit", "1001"},
		{"--before", "2000-01-01T00:00:00Z", "--limit", "many"},
		{"--before", "2000-01-01T00:00:00Z", "--confirm", "yes"},
		{"--before", "2000-01-01T00:00:00Z", "--confirm", strings.Repeat("x", 64)},
		{"--before", "2000-01-01T00:00:00Z", "--execute"},
	} {
		if _, err := parsePurge(args); err == nil {
			t.Errorf("accepted %v", args)
		}
	}
}

func TestPurgeHelpNeedsNoConfiguration(t *testing.T) {
	var out bytes.Buffer
	if err := purgeCommand([]string{"--help"}, "", &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"--before", "--limit", "--confirm", "Preview by default"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("help missing %q", want)
		}
	}
	if err := purgeCommand([]string{"--before", "2000-01-01T00:00:00Z"}, "", &out); err == nil || !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Fatalf("missing DB: %v", err)
	}
	// The command must route before unrelated SSO, model or Kubernetes validation.
	t.Setenv("AUTH_ENABLED", "invalid")
	t.Setenv("LLM_PROVIDER", "invalid")
	t.Setenv("DATABASE_URL", "")
	if err := run([]string{"purge", "--before", "2000-01-01T00:00:00Z"}); err == nil || !strings.Contains(err.Error(), "DATABASE_URL is required") {
		t.Fatalf("routing: %v", err)
	}
}
