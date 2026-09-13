// Package obs holds logging and request instrumentation shared across the service.
package obs

import (
	"context"
	"io"
	"log/slog"
	"strings"
)

type (
	requestIDKey struct{}
	traceKey     struct{}
)

func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey{}, id)
}

func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

// WithTrace records the Cloud Trace id from X-Cloud-Trace-Context ("TRACE_ID/SPAN_ID;o=1").
func WithTrace(ctx context.Context, header string) context.Context {
	traceID, _, _ := strings.Cut(header, "/")
	if traceID == "" {
		return ctx
	}
	return context.WithValue(ctx, traceKey{}, traceID)
}

// NewLogger returns a JSON logger whose records automatically carry the request id from
// the context. Keys follow Cloud Logging's conventions (severity, message), and when
// project is set each line is linked to its Cloud Trace so a request's logs group together.
func NewLogger(w io.Writer, project string) *slog.Logger {
	h := slog.NewJSONHandler(w, &slog.HandlerOptions{ReplaceAttr: cloudLoggingKeys})
	return slog.New(contextHandler{Handler: h, project: project})
}

type contextHandler struct {
	slog.Handler
	project string
}

func (h contextHandler) Handle(ctx context.Context, r slog.Record) error {
	if id := RequestID(ctx); id != "" {
		r.AddAttrs(slog.String("request_id", id))
	}
	if traceID, ok := ctx.Value(traceKey{}).(string); ok && h.project != "" {
		r.AddAttrs(slog.String("logging.googleapis.com/trace", "projects/"+h.project+"/traces/"+traceID))
	}
	return h.Handler.Handle(ctx, r)
}

func (h contextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return contextHandler{Handler: h.Handler.WithAttrs(attrs), project: h.project}
}

func (h contextHandler) WithGroup(name string) slog.Handler {
	return contextHandler{Handler: h.Handler.WithGroup(name), project: h.project}
}

func cloudLoggingKeys(groups []string, a slog.Attr) slog.Attr {
	if len(groups) > 0 {
		return a
	}
	switch a.Key {
	case slog.LevelKey:
		a.Key = "severity"
		if lvl, ok := a.Value.Any().(slog.Level); ok && lvl == slog.LevelWarn {
			a.Value = slog.StringValue("WARNING")
		}
	case slog.MessageKey:
		a.Key = "message"
	}
	return a
}

// Event logs a domain event. The name is both the message and an "event" attribute, so
// logs can be filtered on it.
func Event(ctx context.Context, name string, attrs ...any) {
	slog.InfoContext(ctx, name, append([]any{"event", name}, attrs...)...)
}
