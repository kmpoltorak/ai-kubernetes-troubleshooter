package observability

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
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
