// Package eventlog is pg-connector-pr-github's own rotating JSONL event log
// (bead pg2-ph0o4; contract OBS-1..OBS-3 from bead pg2-m482k's design).
//
// Why this backend owns a log at all: it is a one-shot process, so
// Prometheus cannot scrape it, and a failed call used to leave nothing
// behind except the JSON error envelope on stdout (which pg-router and
// pg-desk consume and discard). The GraphQL budget dipping under
// rate_reserve_points is what starved pg-desk's My Work panel, and nothing
// alerted on it. Each call now appends ONE line to a file this backend
// owns, in the phillipgreenii JSONL logging standard (phillipgreenii-nix-
// support-apps ADR 0038: required time / level / msg), so Loki can ingest it
// and LogQL alert rules can read the signals out of it.
//
// Ownership (OBS-1/OBS-3): the path is this backend's own, resolved from
// the environment here -- NOT from pg-connector's config -- and the
// registration of the file as a Loki log source plus its alert rules live in
// this repo's nix module (darwin/modules/pg-connector-pr-github), not in
// pg-connector. pg-connector stays unbound to OTel.
//
// Failure policy: writing the event log is strictly best effort. A failed
// write never changes the op's result or exit code (the log is telemetry,
// not part of the wire protocol); the backend's stderr is discarded by its
// callers, so there is nowhere useful to report it either.
package eventlog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// ServiceName is this backend's Loki service_name (the logSources attribute
// name in darwin/modules/pg-connector-pr-github, whose default glob is
// ${XDG_STATE_HOME}/<name>/*.jsonl) and the directory under the state home
// that holds the log.
const ServiceName = "pg-connector-pr-github"

// FileName is the event log's name inside its directory; the rotated copy is
// FileName+".1" and the rotation lock FileName+".lock". Neither matches the
// registered *.jsonl glob.
const FileName = "events.jsonl"

// MaxBytes is the size past which the log is rotated (one rotated copy is
// kept, so the worst case on disk is about twice this).
const MaxBytes = 5 << 20

// EnvPath overrides the log path (used by tests and for relocating the log);
// the value "off" disables the event log.
const EnvPath = "PG_CONNECTOR_PR_GITHUB_EVENTS_FILE"

// Log levels, per the JSONL standard's lowercase enum.
const (
	LevelInfo  = "info"
	LevelWarn  = "warn"
	LevelError = "error"
)

// Event is one line of events.jsonl: one backend call.
//
// time/level/msg are the JSONL standard's required fields. error_code is the
// wire taxonomy code (scriptout's closed set) and is absent on success. The
// graphql_* fields are present only on calls that read the GraphQL rate
// limit (every call guarded by the rate reserve: list, search,
// list_attention).
type Event struct {
	Time    string `json:"time"`
	Level   string `json:"level"`
	Msg     string `json:"msg"`
	Service string `json:"service"`
	Version string `json:"version,omitempty"`
	PID     int    `json:"pid"`

	Op         string `json:"op"`
	DurationMS int64  `json:"duration_ms"`
	// ErrorCode is the wire error.code the call answered with.
	ErrorCode string `json:"error_code,omitempty"`
	// Error is the (truncated) error message, for diagnosis.
	Error string `json:"error,omitempty"`

	// GraphQLRemaining / GraphQLResetAt are the rate-limit reading the call
	// took. GraphQLReserve is the configured rate_reserve_points in force,
	// GraphQLHeadroom is remaining-reserve (negative means below the
	// reserve; a LogQL rule unwraps it, since LogQL cannot subtract two
	// fields), and BelowReserve is true when the call was refused because of
	// it.
	GraphQLRemaining *int    `json:"graphql_remaining,omitempty"`
	GraphQLResetAt   *string `json:"graphql_reset_at,omitempty"`
	GraphQLReserve   *int    `json:"graphql_reserve,omitempty"`
	GraphQLHeadroom  *int    `json:"graphql_headroom,omitempty"`
	BelowReserve     bool    `json:"below_reserve,omitempty"`
}

// maxErrorBytes caps the error message copied into an event; gh's stderr can
// be long and the log is for triage, not forensics.
const maxErrorBytes = 300

// LevelForCode maps a wire error code to a log level: unauthenticated and
// unavailable mean the backend cannot do its job (error); the other codes
// describe the caller's request (warn); no code is success (info).
func LevelForCode(code string) string {
	switch code {
	case "":
		return LevelInfo
	case "unauthenticated", "unavailable":
		return LevelError
	default:
		return LevelWarn
	}
}

// Sink receives finished events.
type Sink interface {
	Write(Event)
}

// ---------------------------------------------------------------------
// Per-call recorder, carried on the context.
// ---------------------------------------------------------------------

type rateLimitReading struct {
	remaining int
	resetAt   string
	reserve   int
}

// recorder collects what a handler learned while running that the wrapper
// cannot see from outside. It is shared by the handler's goroutines
// (ListAttention fans out), hence the mutex.
type recorder struct {
	mu   sync.Mutex
	rate *rateLimitReading
}

type recorderKey struct{}

// RecordRateLimit notes a GraphQL rate-limit reading on the in-flight call's
// event. It is a no-op when ctx carries no recorder (every unit test that
// calls the backend directly), so callers never need to guard it.
func RecordRateLimit(ctx context.Context, remaining int, resetAt string, reserve int) {
	r, ok := ctx.Value(recorderKey{}).(*recorder)
	if !ok {
		return
	}
	r.mu.Lock()
	r.rate = &rateLimitReading{remaining: remaining, resetAt: resetAt, reserve: reserve}
	r.mu.Unlock()
}

// ---------------------------------------------------------------------
// Instrumenting a dispatch table.
// ---------------------------------------------------------------------

// Instrument returns a copy of table whose every handler appends one Event
// to sink after the call returns. The wrapped handler's result and error are
// passed through untouched. now is injectable for tests.
func Instrument(table scriptout.DispatchTable, sink Sink, version string, now func() time.Time) scriptout.DispatchTable {
	if sink == nil {
		return table
	}
	out := make(scriptout.DispatchTable, len(table))
	for op, h := range table {
		op, h := op, h
		inner := h.Handle
		h.Handle = func(ctx context.Context, args json.RawMessage) (any, error) {
			rec := &recorder{}
			ctx = context.WithValue(ctx, recorderKey{}, rec)
			start := now()
			result, err := inner(ctx, args)
			sink.Write(buildEvent(op, version, start, now(), err, rec))
			return result, err
		}
		out[op] = h
	}
	return out
}

func buildEvent(op, version string, start, end time.Time, err error, rec *recorder) Event {
	code := ""
	if err != nil {
		// The same classification the wire error envelope uses, so an
		// event's error_code always agrees with the response and exit code.
		code = scriptout.ErrorResponse(err).Error.Code
	}
	ev := Event{
		Time:       end.UTC().Format("2006-01-02T15:04:05.000Z07:00"),
		Level:      LevelForCode(code),
		Service:    ServiceName,
		Version:    version,
		PID:        os.Getpid(),
		Op:         op,
		DurationMS: end.Sub(start).Milliseconds(),
		ErrorCode:  code,
	}
	if err != nil {
		ev.Error = truncate(err.Error(), maxErrorBytes)
		ev.Msg = fmt.Sprintf("%s failed: %s", op, code)
	} else {
		ev.Msg = op + " ok"
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if r := rec.rate; r != nil {
		remaining, resetAt, reserve := r.remaining, r.resetAt, r.reserve
		headroom := remaining - reserve
		ev.GraphQLRemaining = &remaining
		ev.GraphQLReserve = &reserve
		ev.GraphQLHeadroom = &headroom
		if resetAt != "" {
			ev.GraphQLResetAt = &resetAt
		}
		ev.BelowReserve = headroom < 0
	}
	return ev
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	// Cut on a rune boundary so the line stays valid UTF-8.
	cut := max
	for cut > 0 && !isRuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "..."
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }

// ---------------------------------------------------------------------
// File sink with size-based rotation.
// ---------------------------------------------------------------------

// FileSink appends events to Path, rotating to Path+".1" once the file
// exceeds MaxBytes.
type FileSink struct {
	Path     string
	MaxBytes int64
}

// Write appends ev as one line. Errors are swallowed (see the package doc's
// failure policy).
func (s FileSink) Write(ev Event) {
	line, err := Line(ev)
	if err != nil {
		return
	}
	max := s.MaxBytes
	if max <= 0 {
		max = MaxBytes
	}
	_ = Append(s.Path, line, max)
}

// Line is ev as one line of JSON ending in a newline.
func Line(ev Event) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(ev); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// Append adds line to path with a single O_APPEND write, so concurrent
// backend processes (pg-router, pg-desk and SwiftBar each spawn their own)
// never interleave within a line. When the file already exceeds max it is
// first renamed to path+".1", replacing the previous rotated copy.
func Append(path string, line []byte, max int64) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := rotate(path, max); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	n, err := f.Write(line)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && n != len(line) {
		err = fmt.Errorf("short write to %s: %d of %d bytes", path, n, len(line))
	}
	return err
}

// rotate renames path to path+".1" when it is larger than max. Two processes
// that notice the same oversized file at once would each rename it, and the
// second would throw away the fresh log the first started; a flock on a
// sibling file makes the check-and-rename atomic between them.
func rotate(path string, max int64) error {
	fi, err := os.Lstat(path)
	if err != nil || fi.Size() <= max {
		return nil // nothing to rotate; a missing file is created by the append
	}
	lock, err := os.OpenFile(path+".lock", os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Close() }()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN) //nolint:errcheck
	fi, err = os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) || (err == nil && fi.Size() <= max) {
		return nil // another process rotated it first
	}
	if err != nil {
		return err
	}
	return os.Rename(path, path+".1")
}

// ---------------------------------------------------------------------
// Path resolution.
// ---------------------------------------------------------------------

// Path resolves the event log path from the environment: EnvPath if set,
// else $XDG_STATE_HOME/pg-connector-pr-github/events.jsonl, else the XDG default
// $HOME/.local/state. It returns "" when no home can be determined.
func Path(getenv func(string) string) string {
	if p := getenv(EnvPath); p != "" {
		return p
	}
	if state := getenv("XDG_STATE_HOME"); state != "" {
		return filepath.Join(state, ServiceName, FileName)
	}
	if home := getenv("HOME"); home != "" {
		return filepath.Join(home, ".local", "state", ServiceName, FileName)
	}
	return ""
}

// SinkFromEnv builds the production sink, or nil when the log is disabled
// (EnvPath=off) or no path can be determined.
func SinkFromEnv(getenv func(string) string) Sink {
	if getenv(EnvPath) == "off" {
		return nil
	}
	p := Path(getenv)
	if p == "" {
		return nil
	}
	return FileSink{Path: p, MaxBytes: MaxBytes}
}
