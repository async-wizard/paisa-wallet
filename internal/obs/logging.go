// Package obs holds logging and request instrumentation shared across the service.
package obs

import (
	"context"
	"io"
	"log/slog"
)

type requestIDKey struct{}

func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey{}, id)
}

func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

// NewLogger returns a JSON logger whose records automatically carry the request id from
// the context, so every line of a request shares one correlation id.
func NewLogger(w io.Writer) *slog.Logger {
	return slog.New(contextHandler{slog.NewJSONHandler(w, nil)})
}

type contextHandler struct {
	slog.Handler
}

func (h contextHandler) Handle(ctx context.Context, r slog.Record) error {
	if id := RequestID(ctx); id != "" {
		r.AddAttrs(slog.String("request_id", id))
	}
	return h.Handler.Handle(ctx, r)
}

func (h contextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return contextHandler{h.Handler.WithAttrs(attrs)}
}

func (h contextHandler) WithGroup(name string) slog.Handler {
	return contextHandler{h.Handler.WithGroup(name)}
}

// Event logs a domain event. The name is both the message and an "event" attribute, so
// logs can be filtered on it.
func Event(ctx context.Context, name string, attrs ...any) {
	slog.InfoContext(ctx, name, append([]any{"event", name}, attrs...)...)
}
