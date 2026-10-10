package obs

import (
	"context"
	"errors"
	"io"
	"log/slog"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/otel/trace"
)

// ScopeName is the instrumentation scope of the daemon's logs and traces.
const ScopeName = "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus"

// Component is the value of the component field of every log line.
const Component = "pg-task-focus"

// NewLogger builds the daemon's logger. Lines are JSON on w at Info and
// above, each carrying component and, when the context has a span,
// trace_id and span_id; the same records go to the OTLP bridge, which keeps
// WARN and above. No handler is ever given a free-text field: that is the
// caller's contract, enforced by the privacy canary test.
//
// Loki labels are only service_name and level (set by the collector from the
// resource and the record): ids, routes and event types are fields here,
// never labels.
func NewLogger(w io.Writer, otlp slog.Handler) *slog.Logger {
	file := slog.NewJSONHandler(w, &slog.HandlerOptions{Level: slog.LevelInfo})
	handlers := []slog.Handler{traceFields{file}}
	if otlp != nil {
		handlers = append(handlers, levelFilter{h: otlp, min: slog.LevelWarn})
	}
	return slog.New(Fanout(handlers...)).With("component", Component)
}

// OTLPHandler returns the otelslog bridge bound to the global LoggerProvider.
func OTLPHandler() slog.Handler { return otelslog.NewHandler(ScopeName) }

// traceFields adds trace_id and span_id to a record whose context carries a
// span, so handlers that use the *Context calls log them.
type traceFields struct{ slog.Handler }

func (t traceFields) Handle(ctx context.Context, r slog.Record) error {
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		r.AddAttrs(slog.String("trace_id", sc.TraceID().String()), slog.String("span_id", sc.SpanID().String()))
	}
	return t.Handler.Handle(ctx, r)
}

func (t traceFields) WithAttrs(a []slog.Attr) slog.Handler {
	return traceFields{t.Handler.WithAttrs(a)}
}

func (t traceFields) WithGroup(n string) slog.Handler { return traceFields{t.Handler.WithGroup(n)} }

// levelFilter forwards records at or above min.
type levelFilter struct {
	h   slog.Handler
	min slog.Level
}

func (f levelFilter) Enabled(ctx context.Context, l slog.Level) bool {
	return l >= f.min && f.h.Enabled(ctx, l)
}

func (f levelFilter) Handle(ctx context.Context, r slog.Record) error {
	if r.Level < f.min {
		return nil
	}
	return f.h.Handle(ctx, r)
}

func (f levelFilter) WithAttrs(a []slog.Attr) slog.Handler {
	return levelFilter{h: f.h.WithAttrs(a), min: f.min}
}

func (f levelFilter) WithGroup(n string) slog.Handler {
	return levelFilter{h: f.h.WithGroup(n), min: f.min}
}

type fanout struct{ handlers []slog.Handler }

// Fanout forwards each record to every handler that is enabled for it.
func Fanout(handlers ...slog.Handler) slog.Handler { return fanout{handlers: handlers} }

func (f fanout) Enabled(ctx context.Context, l slog.Level) bool {
	for _, h := range f.handlers {
		if h.Enabled(ctx, l) {
			return true
		}
	}
	return false
}

func (f fanout) Handle(ctx context.Context, r slog.Record) error {
	var errs []error
	for _, h := range f.handlers {
		if !h.Enabled(ctx, r.Level) {
			continue
		}
		if err := h.Handle(ctx, r.Clone()); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (f fanout) WithAttrs(a []slog.Attr) slog.Handler {
	next := make([]slog.Handler, len(f.handlers))
	for i, h := range f.handlers {
		next[i] = h.WithAttrs(a)
	}
	return fanout{next}
}

func (f fanout) WithGroup(n string) slog.Handler {
	if n == "" {
		return f
	}
	next := make([]slog.Handler, len(f.handlers))
	for i, h := range f.handlers {
		next[i] = h.WithGroup(n)
	}
	return fanout{next}
}
