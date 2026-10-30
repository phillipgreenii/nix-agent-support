package serve

import (
	"io"
	"log/slog"
	"strings"
	"sync"
)

// serviceName is the JSONL log's service field.
const serviceName = "pg-claude-code-recorder"

// NewLogger returns the daemon's JSONL logger writing to w. Every line has
// ts, level, event and service; the store and copier's short attribute names
// are renamed to the log's common fields (file to file_id, gen to generation).
func NewLogger(w io.Writer) *slog.Logger {
	h := slog.NewJSONHandler(w, &slog.HandlerOptions{
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

// SyncBuffer is a goroutine-safe log sink that can be read while the daemon
// still writes (tests read request log lines back).
type SyncBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

// Write appends p.
func (s *SyncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

// String returns what was written so far.
func (s *SyncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}
