package jsonlsink

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/phillipgreenii/x/jsonllogger"
)

// Sink is one daemon log file written through x/jsonllogger: a RotatingFile
// (reopens after an external rename or delete, caps the size by renaming to
// <file>.1) under the shared slog JSONL handler. Set Ctx (the log constructors do); the
// file is opened on the first write, so the Path and MaxBytes fields of the
// owning log can be set after construction, and a directory that cannot be
// written yet is retried on the next row. Writing is strictly best effort: a
// failure drops the row and never reaches the caller.
type Sink struct {
	// Service is the service_name stamped on every row (the Loki label).
	Service string
	mu      sync.Mutex
	h       slog.Handler
	// path and max are what h was opened with; a change reopens.
	path string
	max  int64
	f    *jsonllogger.RotatingFile
	// ctx is the context every row is handled under. A row is written
	// fire-and-forget, so it is the detached background context the owning
	// log constructor sets, never a request context that could cancel it.
	Ctx context.Context
}

// handler returns the open handler for path, opening (or reopening after the
// owner changed path or max) when needed. The caller holds s.mu.
func (s *Sink) handler(path string, max int64) slog.Handler {
	if s.h != nil && s.path == path && s.max == max {
		return s.h
	}
	if s.f != nil {
		_ = s.f.Close()
		s.f, s.h = nil, nil
	}
	f, err := jsonllogger.OpenRotatingFile(path, max)
	if err != nil {
		return nil
	}
	s.f, s.path, s.max = f, path, max
	s.h = jsonllogger.NewHandler(f, jsonllogger.WithService(s.Service))
	return s.h
}

// Emit appends v as one JSONL line to path, rotating past max. v is a typed row
// whose JSON form starts with time, level and msg (every row type of this
// package does); the three become the record's time, level and message, and
// every other field becomes a slog attribute in the row's own order.
func (s *Sink) Emit(path string, max int64, v any) {
	rec, ok := rowRecord(v)
	if !ok {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	h := s.handler(path, max)
	if h == nil {
		return
	}
	_ = h.Handle(s.Ctx, rec)
}

// rowRecord maps a typed row to a slog record. The row's own "time" (an
// injected clock stamps it, in the JSONL standard's millisecond layout) is kept
// as an attribute and the record's own time is left zero, which slog omits, so
// the line carries exactly one time. "service" is dropped: the handler stamps
// it.
func rowRecord(v any) (slog.Record, bool) {
	raw, err := json.Marshal(v)
	if err != nil {
		return slog.Record{}, false
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return slog.Record{}, false
	}
	var (
		level = slog.LevelInfo
		msg   string
		attrs []slog.Attr
	)
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return slog.Record{}, false
		}
		key, _ := kt.(string)
		var val json.RawMessage
		if err := dec.Decode(&val); err != nil {
			return slog.Record{}, false
		}
		switch key {
		case "level":
			var l string
			_ = json.Unmarshal(val, &l)
			level = levelOf(l)
		case "msg":
			_ = json.Unmarshal(val, &msg)
		case "service":
		default:
			attrs = append(attrs, slog.Any(key, val))
		}
	}
	rec := slog.NewRecord(zeroTime, level, msg, 0)
	rec.AddAttrs(attrs...)
	return rec, true
}

func levelOf(s string) slog.Level {
	switch s {
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	}
	return slog.LevelInfo
}

// zeroTime is the record time slog omits from a line.
var zeroTime time.Time
