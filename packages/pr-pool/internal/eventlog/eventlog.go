// Package eventlog is pr-pool's own structured per-run event log (JSONL). It is
// NOT Claude's transcript. Each line conforms to the phillipgreenii JSONL
// standard (`time`/`level`/`msg`, with `kind` kept as an ordinary event-type
// field). Safe for concurrent emitters (a mutex serializes marshal+write so
// lines never interleave).
package eventlog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Writer is a JSONL event log writer. Safe for concurrent use.
type Writer struct {
	mu sync.Mutex
	f  *os.File
	// Now is an injectable clock seam (mirrors watchdog.Watchdog.Now); New
	// defaults it to time.Now. Each emitted record is stamped with it.
	Now func() time.Time
}

// New opens (creating parent dirs) the JSONL log at path in append mode.
func New(path string) (*Writer, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	return &Writer{f: f, Now: time.Now}, nil
}

func (w *Writer) now() time.Time {
	if w.Now != nil {
		return w.Now()
	}
	return time.Now()
}

// Emit writes one JSON object as a line. `time` (RFC3339Nano, UTC), `level`,
// `kind`, and `msg` are always stamped by Emit; fields are merged in but any of
// those four reserved keys present in fields is ignored so it cannot override the
// stamped value. `level` should be one of debug/info/warn/error (the JSONL
// standard); `kind` is an event-type, not a severity, and is kept as a normal
// field.
func (w *Writer) Emit(level, kind, msg string, fields map[string]any) error {
	rec := make(map[string]any, len(fields)+4)
	for k, v := range fields {
		switch k {
		case "time", "level", "kind", "msg":
			// reserved — stamped below
		default:
			rec[k] = v
		}
	}
	rec["time"] = w.now().UTC().Format(time.RFC3339Nano)
	rec["level"] = level
	rec["kind"] = kind
	rec["msg"] = msg
	b, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	w.mu.Lock()
	defer w.mu.Unlock()
	_, err = w.f.Write(b)
	return err
}

// Close closes the underlying file.
func (w *Writer) Close() error { return w.f.Close() }
