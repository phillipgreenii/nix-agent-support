// Package eventlog is pr-pool's own structured per-run event log (JSONL). It is
// NOT Claude's transcript. Safe for concurrent emitters (a mutex serializes
// marshal+write so lines never interleave).
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

// Emit writes one JSON object as a line. `ts` (RFC3339Nano, UTC) and `kind` are
// always stamped by Emit; fields are merged in but fields named "ts" or "kind"
// are ignored so they cannot override the stamped values.
func (w *Writer) Emit(kind string, fields map[string]any) error {
	rec := make(map[string]any, len(fields)+2)
	for k, v := range fields {
		if k != "kind" && k != "ts" {
			rec[k] = v
		}
	}
	rec["ts"] = w.now().UTC().Format(time.RFC3339Nano)
	rec["kind"] = kind
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
