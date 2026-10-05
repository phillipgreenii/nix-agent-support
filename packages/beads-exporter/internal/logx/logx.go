// Package logx builds the exporter's structured logger.
//
// The output is JSON lines on the given writer with the field contract shared
// by every in-house daemon: time (RFC3339, UTC), level (lowercase debug, info,
// warn or error), msg, and service.
package logx

import (
	"io"
	"log/slog"
	"strings"
	"time"
)

// ServiceName is stamped on every record.
const ServiceName = "beads-exporter"

// New returns a JSON logger writing to w.
func New(w io.Writer, level slog.Level) *slog.Logger {
	h := slog.NewJSONHandler(w, &slog.HandlerOptions{
		Level: level,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if len(groups) > 0 {
				return a
			}
			switch a.Key {
			case slog.LevelKey:
				if lvl, ok := a.Value.Any().(slog.Level); ok {
					return slog.String(slog.LevelKey, strings.ToLower(lvl.String()))
				}
			case slog.TimeKey:
				if t, ok := a.Value.Any().(time.Time); ok {
					return slog.String(slog.TimeKey, t.UTC().Format(time.RFC3339Nano))
				}
			}
			return a
		},
	})
	return slog.New(h).With("service", ServiceName)
}
