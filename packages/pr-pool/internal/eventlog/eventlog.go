// Package eventlog is pr-pool's own structured per-run event log (JSONL). It is
// NOT Claude's transcript. Safe for concurrent emitters (a mutex serializes
// marshal+write so lines never interleave).
package eventlog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// Writer is a JSONL event log writer. Safe for concurrent use.
type Writer struct {
	mu sync.Mutex
	f  *os.File
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
	return &Writer{f: f}, nil
}

// Emit writes one JSON object as a line. `kind` is always present; fields are
// merged in (fields named "kind" are ignored).
func (w *Writer) Emit(kind string, fields map[string]any) error {
	rec := make(map[string]any, len(fields)+1)
	for k, v := range fields {
		if k != "kind" {
			rec[k] = v
		}
	}
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
