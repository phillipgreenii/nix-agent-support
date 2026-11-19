// Package eventlog is pg-router's own structured per-run event log (JSONL). It is
// NOT Claude's transcript. Each line conforms to the phillipgreenii JSONL
// standard (`time`/`level`/`msg`, with `kind` kept as an ordinary event-type
// field). Safe for concurrent emitters (a mutex serializes marshal+write so
// lines never interleave) and for several processes appending to one path:
// the file is size-and-age bounded by rotation (see rotate.go).
//
// Duplicated verbatim from packages/pg-router/internal/eventlog (still needed
// there by its own orchestrator/run.go, which stay in-core): a stdlib-only
// leaf, so a byte-for-byte copy costs nothing and neither side may import the
// other's internal/* (docket pg2-oju6w Task 5.2/5.3, folded per the
// operator's 2026-09-11 decision; docs/adr/0065's Addendum).
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
	mu   sync.Mutex
	f    *os.File
	path string
	// Retention bounds the file by rotation; New sets DefaultRetention().
	// Set it before the first Emit (a zero value disables rotation).
	Retention Retention
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
	return &Writer{f: f, path: path, Retention: DefaultRetention(), Now: time.Now}, nil
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
	// Rotation is housekeeping: a failure must not drop the event.
	_ = w.rotateLocked()
	_, err = w.f.Write(b)
	return err
}

// rotateLocked rotates the file when it has reached the size cap and reopens
// it when another process (or this one) rotated it from under the open
// descriptor, so appends always land in the live path and never in a rotated
// generation. Caller holds w.mu.
func (w *Writer) rotateLocked() error {
	if w.path == "" {
		return nil
	}
	if fi, err := os.Stat(w.path); err == nil && !fi.Mode().IsRegular() {
		return nil // /dev/null fallback and the like
	}
	err := rotateIfNeeded(w.path, w.Retention, w.now())
	if sameFile(w.f, w.path) {
		return err
	}
	nf, oerr := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if oerr != nil {
		return oerr
	}
	_ = w.f.Close()
	w.f = nf
	return err
}

// Close closes the underlying file.
func (w *Writer) Close() error { return w.f.Close() }
