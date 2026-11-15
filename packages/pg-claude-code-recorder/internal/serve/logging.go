package serve

import (
	"io"
	"log/slog"
	"strings"
	"sync"

	"github.com/phillipgreenii/x/jsonllogger"
)

// serviceName is the JSONL log's service field.
const serviceName = "pg-claude-code-recorder"

// NewLogger returns the daemon's JSONL logger writing to w. Every line has
// ts, level, event and service; the store and copier's short attribute names
// are renamed to the log's common fields (file to file_id, gen to generation).
// It is x/jsonllogger configured to keep the daemon's historical line format.
func NewLogger(w io.Writer) *slog.Logger {
	return jsonllogger.NewLogger(
		w,
		jsonllogger.WithService(serviceName),
		jsonllogger.WithKeyRenames(map[string]string{
			slog.TimeKey:    "ts",
			slog.MessageKey: "event",
			"file":          "file_id",
			"gen":           "generation",
		}),
	)
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
