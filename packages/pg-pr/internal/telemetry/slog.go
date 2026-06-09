// Package telemetry — slog↔OTel glue.
//
// Fanout lets the daemon keep its stderr logging while additionally exporting
// every record over OTLP through the otelslog bridge. NewSlogHandler builds
// that bridge bound to the global LoggerProvider Init installs.
package telemetry

import (
	"context"
	"errors"
	"log/slog"

	"go.opentelemetry.io/contrib/bridges/otelslog"
)

// fanoutHandler dispatches each record to every wrapped handler. A child is
// only invoked when it reports Enabled for the record's level, so per-child
// level thresholds are respected.
type fanoutHandler struct {
	handlers []slog.Handler
}

// Fanout returns a slog.Handler that forwards to every handler passed in.
func Fanout(handlers ...slog.Handler) slog.Handler {
	return fanoutHandler{handlers: handlers}
}

func (f fanoutHandler) Enabled(ctx context.Context, level slog.Level) bool {
	for _, h := range f.handlers {
		if h.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

func (f fanoutHandler) Handle(ctx context.Context, r slog.Record) error {
	var errs []error
	for _, h := range f.handlers {
		if !h.Enabled(ctx, r.Level) {
			continue
		}
		// Clone per the slog.Handler contract: a Record must not be shared
		// across handlers that may mutate it.
		if err := h.Handle(ctx, r.Clone()); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (f fanoutHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	next := make([]slog.Handler, len(f.handlers))
	for i, h := range f.handlers {
		next[i] = h.WithAttrs(attrs)
	}
	return fanoutHandler{handlers: next}
}

func (f fanoutHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return f
	}
	next := make([]slog.Handler, len(f.handlers))
	for i, h := range f.handlers {
		next[i] = h.WithGroup(name)
	}
	return fanoutHandler{handlers: next}
}

// NewSlogHandler returns an slog.Handler that exports records to the global
// OTel LoggerProvider (installed by Init). When no OTLP endpoint is
// configured the global provider is a no-op, so this handler is a cheap
// no-op and is safe to include unconditionally. The instrumentation scope
// name is TracerName; it does NOT set service_name — that comes from the
// resource (OTEL_SERVICE_NAME) configured in Init.
func NewSlogHandler() slog.Handler {
	return otelslog.NewHandler(TracerName)
}
