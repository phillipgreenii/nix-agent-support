// Package eventlog is an append-only JSONL event log recording, per session, the
// ordered sequence of (a) state transitions (from→to) and (b) input actions
// (Escape bursts, paste, Enter, clear-input). It is distinct from the plain-text
// hook.log: the store overwrites the current state (no history), but the ordered
// sequence is recoverable from this log, and tests/contract asserts can parse it.
//
// Canonical location: <state-dir>/events.jsonl (beside hook.log; see
// config.Config.EventLogPath). One JSON object per line, schema:
//
//	{"ts":"<RFC3339Nano UTC>","name":"<session>","kind":"transition",
//	 "from":"<state>","to":"<state>","uuid":"<claude session id>",
//	 "line_ref":"<transcript path / claude-session line ref>"}
//	{"ts":"<RFC3339Nano UTC>","name":"<session>","kind":"input",
//	 "action":"escape-burst|paste|enter|clear-input","detail":"<short note>"}
//
// Kind-specific fields are omitempty, so a transition line never carries
// action/detail and an input line never carries from/to/uuid/line_ref.
//
// Writes use O_APPEND|O_CREATE|O_WRONLY and the fd is NOT held open between
// writes: O_APPEND keeps the small single-line writes atomic across processes
// (the hook process vs the reply process both append to the same file). A nil
// *Logger is a valid no-op so callers can treat it as an optional dependency,
// mirroring the never-fail policy of the hook's diagnostics logging.
package eventlog

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Event is one JSONL line. Kind is "transition" or "input"; the kind-specific
// fields are omitempty so each line carries only the fields its kind uses.
type Event struct {
	Ts   string `json:"ts"`   // RFC3339Nano, UTC
	Name string `json:"name"` // session name
	Kind string `json:"kind"` // "transition" | "input"

	// Transition fields.
	From    string `json:"from,omitempty"`
	To      string `json:"to,omitempty"`
	UUID    string `json:"uuid,omitempty"`
	LineRef string `json:"line_ref,omitempty"` // optional claude-session/transcript ref

	// Input fields.
	Action string `json:"action,omitempty"` // e.g. "escape-burst", "paste", "enter", "clear-input"
	Detail string `json:"detail,omitempty"`
}

// Logger appends Events to a JSONL file. The fd is opened per-write (O_APPEND),
// never held; the mutex serializes in-process writes. ALL methods are nil-safe:
// a method on a nil *Logger is a no-op returning nil, so the Logger can be an
// optional dependency wired in only when configured.
type Logger struct {
	path string
	mu   sync.Mutex
}

// Open ensures the parent dir of path exists (0o700) and records the path. It
// does NOT open the file — Append opens it per-write so cross-process O_APPEND
// writes stay atomic.
func Open(path string) (*Logger, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("mkdir event-log parent: %w", err)
	}
	return &Logger{path: path}, nil
}

// Append writes one Event as a single JSON line + "\n". Nil-safe no-op.
func (l *Logger) Append(e Event) error {
	if l == nil {
		return nil
	}
	line, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	f, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open event log: %w", err)
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("write event: %w", err)
	}
	return nil
}

// Transition records a state transition. ts is passed in explicitly (no
// time.Now inside the package) for deterministic tests; it is normalized to
// RFC3339Nano UTC. lineRef is the optional claude-session/transcript ref.
// Nil-safe no-op.
func (l *Logger) Transition(ts time.Time, name, from, to, uuid, lineRef string) error {
	if l == nil {
		return nil
	}
	return l.Append(Event{
		Ts: stamp(ts), Name: name, Kind: "transition",
		From: from, To: to, UUID: uuid, LineRef: lineRef,
	})
}

// Input records an input action (e.g. escape-burst, paste, enter, clear-input).
// detail is a short note — NOT the prompt body. Nil-safe no-op.
func (l *Logger) Input(ts time.Time, name, action, detail string) error {
	if l == nil {
		return nil
	}
	return l.Append(Event{
		Ts: stamp(ts), Name: name, Kind: "input",
		Action: action, Detail: detail,
	})
}

// stamp normalizes a timestamp to RFC3339Nano UTC.
func stamp(ts time.Time) string { return ts.UTC().Format(time.RFC3339Nano) }

// Read parses the JSONL log back into ordered Events. A missing file yields an
// empty slice and no error (the log may not exist yet).
func Read(path string) ([]Event, error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open event log: %w", err)
	}
	defer f.Close()
	var out []Event
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var e Event
		if err := json.Unmarshal(line, &e); err != nil {
			return nil, fmt.Errorf("parse event line: %w", err)
		}
		out = append(out, e)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("scan event log: %w", err)
	}
	return out, nil
}
