// Package eventlog is the shared writer behind every pg-connector backend's
// OWN rotating JSONL event log (contract OBS-1..OBS-3 from bead pg2-m482k's
// design; first user pg-connector-pr-github, bead pg2-ph0o4, then
// pg-connector-thread-slack, bead pg2-kjdfi).
//
// What is shared and what is not. Ownership stays with each backend
// (OBS-1/OBS-3): a backend resolves ITS OWN log path from ITS OWN
// environment variable and state directory, defines ITS OWN event shape (the
// fields only it can observe: GraphQL budget, claude failure stage, ...), and
// registers the file as a Loki log source plus alert rules in ITS OWN nix
// module -- never through pg-connector's config, and pg-connector stays
// unbound to OTel. This package holds only the mechanics that would
// otherwise be copied verbatim into every backend: the fields every event
// carries (Base), the size-based rotation and the single-write append, the
// path resolution rule, the error-code to level mapping, and the dispatch
// table wrapper that times each call.
//
// Failure policy: writing the event log is strictly best effort. A failed
// write never changes the op's result or exit code (the log is telemetry, not
// part of the wire protocol); a backend's stderr is discarded by its callers,
// so there is nowhere useful to report it either.
package eventlog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// FileName is the event log's name inside its directory; the rotated copy is
// FileName+".1" and the rotation lock FileName+".lock". Neither matches a
// registered *.jsonl glob.
const FileName = "events.jsonl"

// MaxBytes is the size past which a log is rotated (one rotated copy is kept,
// so the worst case on disk is about twice this).
const MaxBytes = 5 << 20

// MaxErrorBytes caps the error message copied into an event; the log is for
// triage, not forensics. An over-long message is cut in the MIDDLE
// (TruncateHeadTail), keeping ErrorHeadBytes from the front and the rest of
// the budget from the back: a wrapped exec failure reads "<command line>:
// <exit status>: <stderr>", and the diagnostic (the stderr) is at the END, so
// the former head-only 300-byte cut dropped exactly the part that tells a
// connect failure from a 502/504 or a timeout (bead pg2-daktd).
const MaxErrorBytes = 1024

// ErrorHeadBytes is how much of the front of a truncated error is kept: enough
// to identify the failing command, leaving the remainder of MaxErrorBytes for
// the tail.
const ErrorHeadBytes = 256

// Off is the value of a backend's events-file environment variable that
// disables its event log.
const Off = "off"

// Log levels, per the phillipgreenii JSONL standard's lowercase enum
// (phillipgreenii-nix-support-apps ADR 0038: required time / level / msg).
const (
	LevelInfo  = "info"
	LevelWarn  = "warn"
	LevelError = "error"
)

// Base is the field set every backend's event carries. A backend's Event type
// embeds it (encoding/json flattens an embedded struct, so the line keeps one
// flat object with these fields first) and appends the fields only it can
// observe.
//
// time/level/msg are the JSONL standard's required fields. error_code is the
// wire taxonomy code (scriptout's closed set) and is absent on success.
type Base struct {
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
	// Error is the error message, for diagnosis; one longer than MaxErrorBytes
	// keeps its head and tail (TruncateHeadTail).
	Error string `json:"error,omitempty"`
}

// NewBase builds the Base for one finished call. The error code is the same
// classification the wire error envelope uses (scriptout.ErrorResponse), so an
// event's error_code always agrees with the response and the exit code.
func NewBase(service, op, version string, start, end time.Time, err error) Base {
	code := ""
	if err != nil {
		code = scriptout.ErrorResponse(err).Error.Code
	}
	b := Base{
		Time:       end.UTC().Format("2006-01-02T15:04:05.000Z07:00"),
		Level:      LevelForCode(code),
		Service:    service,
		Version:    version,
		PID:        os.Getpid(),
		Op:         op,
		DurationMS: end.Sub(start).Milliseconds(),
		ErrorCode:  code,
	}
	if err != nil {
		b.Error = TruncateHeadTail(err.Error(), MaxErrorBytes, ErrorHeadBytes)
		b.Msg = fmt.Sprintf("%s failed: %s", op, code)
	} else {
		b.Msg = op + " ok"
	}
	return b
}

// LevelForCode maps a wire error code to a log level: unauthenticated and
// unavailable mean the backend cannot do its job (error); the other codes
// describe the caller's request (warn); no code is success (info).
//
// query_not_recognized is the exception among the request-describing codes
// (bead pg2-6y4ot): INV-ERR-3 defines it as "not applicable to this backend".
// The umbrella's list fan-out asks EVERY registered backend of a type for a
// named query and skips the ones that do not define it, so a backend that
// answers it is behaving as designed (e.g. the Jira backend asked for the
// beads-only work-beads / escalated-work queries). It is logged at info, not
// warn, so the expected asymmetric fan-out does not read as a fault.
func LevelForCode(code string) string {
	switch code {
	case "", "query_not_recognized":
		return LevelInfo
	case "unauthenticated", "unavailable":
		return LevelError
	default:
		return LevelWarn
	}
}

// Truncate cuts s to at most max bytes (plus an ellipsis) on a rune boundary,
// so the log line stays valid UTF-8.
func Truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !isRuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "..."
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }

// elisionMarker is what replaces the dropped middle of a TruncateHeadTail
// result; n is the number of bytes dropped.
func elisionMarker(n int) string { return fmt.Sprintf(" ...[%d bytes elided]... ", n) }

// TruncateHeadTail cuts s to roughly max bytes by dropping its middle: the
// first head bytes and the last max-head bytes survive, joined by an elision
// marker that states how many bytes were dropped (so the result is at most
// max plus the marker's length). Both cut points land on rune boundaries, so
// the log line stays valid UTF-8. A string already within max is unchanged.
func TruncateHeadTail(s string, max, head int) string {
	if len(s) <= max {
		return s
	}
	if head < 0 {
		head = 0
	}
	if head > max {
		head = max
	}
	headEnd := head
	for headEnd > 0 && !isRuneStart(s[headEnd]) {
		headEnd--
	}
	tailStart := len(s) - (max - head)
	for tailStart < len(s) && !isRuneStart(s[tailStart]) {
		tailStart++
	}
	return s[:headEnd] + elisionMarker(tailStart-headEnd) + s[tailStart:]
}

// ---------------------------------------------------------------------
// Timing every call of a dispatch table.
// ---------------------------------------------------------------------

// Finish is called once per call, after the wrapped handler returned, with the
// call's start and end times and its error (nil on success).
type Finish func(start, end time.Time, err error)

// Around is called once per call BEFORE the wrapped handler runs. It returns
// the context to run the handler with (a backend attaches its per-call
// recorder to it) and the Finish to call afterwards.
type Around func(ctx context.Context, op string) (context.Context, Finish)

// Wrap returns a copy of table whose every handler runs inside around. The
// wrapped handler's result and error are passed through untouched. now is
// injectable for tests.
func Wrap(table scriptout.DispatchTable, now func() time.Time, around Around) scriptout.DispatchTable {
	return WrapProgress(table, now, around, nil, 0)
}

// ---------------------------------------------------------------------
// In-flight (start / heartbeat) rows [bead pg2-5dyz2].
// ---------------------------------------------------------------------

// Phases of an in-flight row.
const (
	// PhaseStart is written when a call begins.
	PhaseStart = "start"
	// PhaseHeartbeat is written every heartbeat interval while a call is
	// still running.
	PhaseHeartbeat = "heartbeat"
)

// HeartbeatInterval is how often a still-running call writes a heartbeat row.
// Shorter than a typical deadline so even a call SIGKILLed mid-flight leaves
// a last heartbeat that bounds how long it ran, yet long enough that a call
// finishing in a few seconds writes none.
const HeartbeatInterval = 10 * time.Second

// ProgressEvent is one in-flight row: a call's start, or a heartbeat while it
// is still running. It exists because a backend SIGKILLed by its caller's
// deadline never reaches the code that writes the final row, so a call that
// was killed used to appear nowhere in the log (bead pg2-5dyz2: 67 killed
// source calls on 2026-10-06, none attributable). A call with a start row
// and no final row (the same pid, op and args) was killed or crashed, and its
// last heartbeat's elapsed_ms is a lower bound on how long it ran.
//
// It is deliberately NOT a Base: it carries no duration_ms or error_code, so
// a latency or error-rate query over the final rows is not diluted by
// in-flight ones. The fields time/level/msg are the JSONL standard's required
// ones; Level is always info.
type ProgressEvent struct {
	Time    string `json:"time"`
	Level   string `json:"level"`
	Msg     string `json:"msg"`
	Service string `json:"service"`
	Version string `json:"version,omitempty"`
	PID     int    `json:"pid"`

	Op string `json:"op"`
	// Phase is PhaseStart or PhaseHeartbeat.
	Phase string `json:"phase"`
	// ElapsedMS is how long the call had been running when the row was
	// written (0 on the start row).
	ElapsedMS int64 `json:"elapsed_ms"`
	// Args is scriptout.SummarizeArgs of the request's args: the closest the
	// backend has to the call's argv.
	Args string `json:"args,omitempty"`
}

// NewProgressEvent builds the row for one in-flight call.
func NewProgressEvent(service, op, version, phase string, args json.RawMessage, start, now time.Time) ProgressEvent {
	msg := op + " started"
	if phase == PhaseHeartbeat {
		msg = op + " still running"
	}
	return ProgressEvent{
		Time:      now.UTC().Format("2006-01-02T15:04:05.000Z07:00"),
		Level:     LevelInfo,
		Msg:       msg,
		Service:   service,
		Version:   version,
		PID:       os.Getpid(),
		Op:        op,
		Phase:     phase,
		ElapsedMS: now.Sub(start).Milliseconds(),
		Args:      scriptout.SummarizeArgs(args),
	}
}

// Progress is called with the start row (before the handler runs) and then
// with a heartbeat row every interval while it is still running. now is the
// time the row was produced; start is the call's start time. It runs on the
// call's goroutine (start) or a heartbeat goroutine (heartbeats), so it MUST
// be safe to call concurrently with a Finish.
type Progress func(op string, args json.RawMessage, phase string, start, now time.Time)

// WrapProgress is Wrap plus in-flight rows: when progress is non-nil it is
// called with PhaseStart before the handler runs and with PhaseHeartbeat every
// interval (HeartbeatInterval when interval <= 0) until the handler returns.
// The heartbeat goroutine has stopped before the handler's result is passed
// on, so no heartbeat is written after the final row.
func WrapProgress(table scriptout.DispatchTable, now func() time.Time, around Around, progress Progress, interval time.Duration) scriptout.DispatchTable {
	if interval <= 0 {
		interval = HeartbeatInterval
	}
	out := make(scriptout.DispatchTable, len(table))
	for op, h := range table {
		op, h := op, h
		inner := h.Handle
		h.Handle = func(ctx context.Context, args json.RawMessage) (any, error) {
			ctx, finish := around(ctx, op)
			start := now()
			stop := func() {}
			if progress != nil {
				progress(op, args, PhaseStart, start, start)
				stop = heartbeat(op, args, start, now, interval, progress)
			}
			result, err := inner(ctx, args)
			stop()
			finish(start, now(), err)
			return result, err
		}
		out[op] = h
	}
	return out
}

// heartbeat starts the goroutine that writes a heartbeat every interval and
// returns the function that stops it and waits for it to exit.
func heartbeat(op string, args json.RawMessage, start time.Time, now func() time.Time, interval time.Duration, progress Progress) (stop func()) {
	done := make(chan struct{})
	exited := make(chan struct{})
	go func() {
		defer close(exited)
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				progress(op, args, PhaseHeartbeat, start, now())
			}
		}
	}()
	return func() {
		close(done)
		<-exited
	}
}

// ---------------------------------------------------------------------
// Appending with size-based rotation.
// ---------------------------------------------------------------------

// Line is v as one line of JSON ending in a newline.
func Line(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// WriteBestEffort appends v as one line to path, rotating past max (MaxBytes
// when max <= 0). Errors are swallowed (see the package doc's failure policy).
func WriteBestEffort(path string, max int64, v any) {
	line, err := Line(v)
	if err != nil {
		return
	}
	if max <= 0 {
		max = MaxBytes
	}
	_ = Append(path, line, max)
}

// Append adds line to path with a single O_APPEND write, so concurrent backend
// processes (pg-router, pg-desk and SwiftBar each spawn their own) never
// interleave within a line. When the file already exceeds max it is first
// renamed to path+".1", replacing the previous rotated copy.
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

// Path resolves a backend's event log path from the environment: envVar if
// set, else $XDG_STATE_HOME/<service>/events.jsonl, else the XDG default
// $HOME/.local/state/<service>/events.jsonl. It returns "" when no home can be
// determined. The <service> directory name is also the backend's Loki
// service_name and its logSources attribute name in its nix module, whose
// default glob is ${XDG_STATE_HOME}/<service>/*.jsonl.
func Path(getenv func(string) string, envVar, service string) string {
	if p := getenv(envVar); p != "" {
		return p
	}
	if state := getenv("XDG_STATE_HOME"); state != "" {
		return filepath.Join(state, service, FileName)
	}
	if home := getenv("HOME"); home != "" {
		return filepath.Join(home, ".local", "state", service, FileName)
	}
	return ""
}

// Resolve is the path a production sink writes to, or "" when the log is
// disabled (envVar=off) or no path can be determined.
func Resolve(getenv func(string) string, envVar, service string) string {
	if getenv(envVar) == Off {
		return ""
	}
	return Path(getenv, envVar, service)
}
