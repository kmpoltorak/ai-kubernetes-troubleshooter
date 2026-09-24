// Package observability provides structured logging with request context and
// Prometheus metrics.
package observability

import (
	"context"
	"io"
	"log/slog"
)

type ctxKey int

const (
	requestIDKey ctxKey = iota
	traceIDKey
)

// WithRequestIDs returns a context carrying the request and trace IDs that
// every log record written with that context will include.
func WithRequestIDs(ctx context.Context, requestID, traceID string) context.Context {
	ctx = context.WithValue(ctx, requestIDKey, requestID)
	return context.WithValue(ctx, traceIDKey, traceID)
}

func RequestID(ctx context.Context) string {
	s, _ := ctx.Value(requestIDKey).(string)
	return s
}

// NewLogger returns a JSON logger that adds request_id and trace_id from the
// context of each record. Use the *Context logging methods to benefit.
func NewLogger(w io.Writer, level slog.Level) *slog.Logger {
	return slog.New(contextHandler{slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level})})
}

type contextHandler struct{ slog.Handler }

func (h contextHandler) Handle(ctx context.Context, r slog.Record) error {
	if id, ok := ctx.Value(requestIDKey).(string); ok {
		r.AddAttrs(slog.String("request_id", id))
	}
	if id, ok := ctx.Value(traceIDKey).(string); ok {
		r.AddAttrs(slog.String("trace_id", id))
	}
	return h.Handler.Handle(ctx, r)
}

func (h contextHandler) WithAttrs(a []slog.Attr) slog.Handler {
	return contextHandler{h.Handler.WithAttrs(a)}
}

func (h contextHandler) WithGroup(name string) slog.Handler {
	return contextHandler{h.Handler.WithGroup(name)}
}
