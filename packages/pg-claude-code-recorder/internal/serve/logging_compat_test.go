package serve

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// legacyLogger is the daemon's hand-rolled logger from before it adopted
// x/jsonllogger, kept as the byte-compatibility reference.
func legacyLogger(buf *bytes.Buffer) *slog.Logger {
	h := slog.NewJSONHandler(buf, &slog.HandlerOptions{
		Level: slog.LevelInfo,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if len(groups) > 0 {
				return a
			}
			switch a.Key {
			case slog.TimeKey:
				a.Key = "ts"
			case slog.MessageKey:
				a.Key = "event"
			case slog.LevelKey:
				a.Value = slog.StringValue(strings.ToLower(a.Value.String()))
			case "file":
				a.Key = "file_id"
			case "gen":
				a.Key = "generation"
			}
			return a
		},
	})
	return slog.New(h).With("service", serviceName)
}

func emitFixed(t *testing.T, l *slog.Logger) {
	t.Helper()
	ts := time.Date(2026, 11, 15, 10, 20, 30, 123456000, time.UTC)
	for _, lvl := range []slog.Level{slog.LevelDebug, slog.LevelInfo, slog.LevelWarn, slog.LevelError} {
		r := slog.NewRecord(ts, lvl, "some_event", 0)
		r.AddAttrs(slog.String("file", "f1"), slog.Int("gen", 3), slog.String("path", "/a b"),
			slog.Group("g", slog.String("file", "kept"), slog.Int("gen", 9)))
		if err := l.Handler().Handle(context.Background(), r); err != nil {
			t.Fatal(err)
		}
	}
}

func TestNewLoggerByteCompatibleWithLegacyFormat(t *testing.T) {
	var got, want bytes.Buffer
	emitFixed(t, NewLogger(&got))
	emitFixed(t, legacyLogger(&want))
	if got.String() != want.String() {
		t.Fatalf("log output drifted\ngot:\n%s\nwant:\n%s", got.String(), want.String())
	}
	if !strings.Contains(got.String(), `{"ts":"2026-11-15T10:20:30.123456Z","level":"info","event":"some_event","service":"pg-claude-code-recorder","file_id":"f1","generation":3`) {
		t.Fatalf("unexpected line shape: %s", got.String())
	}
}
