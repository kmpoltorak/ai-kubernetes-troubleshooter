package observability

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

func TestLoggerAddsContextIDs(t *testing.T) {
	var buf bytes.Buffer
	log := NewLogger(&buf, slog.LevelInfo).With("component", "test")
	ctx := WithRequestIDs(context.Background(), "req-1", "trace-1")
	log.InfoContext(ctx, "hello")

	var rec map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		t.Fatal(err)
	}
	if rec["request_id"] != "req-1" || rec["trace_id"] != "trace-1" || rec["component"] != "test" {
		t.Fatalf("missing fields: %v", rec)
	}
}

func TestLoggerMasksMessagesAndAttributes(t *testing.T) {
	var buf bytes.Buffer
	log := NewLogger(&buf, slog.LevelInfo).With("bound", "password=synthetic-secret").WithGroup("details")
	log.Error("password=synthetic-secret", "error", fmt.Errorf("password=synthetic-secret"), "password", "short-secret", "data", map[string]any{"token": "short-secret", "count": uint64(18446744073709551615)}, "healthy", true)
	raw := buf.String()
	if strings.Contains(raw, "synthetic-secret") || strings.Contains(raw, "short-secret") || !strings.Contains(raw, "18446744073709551615") {
		t.Fatalf("unsafe log: %s", raw)
	}
	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["level"] != "ERROR" || got["details"].(map[string]any)["healthy"] != true {
		t.Fatalf("lost log fields: %v", got)
	}
}
