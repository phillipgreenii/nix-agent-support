// Package eventlog is pg-connector-issue-beads's own rotating JSONL event log
// (bead pg2-5dyz2; contract OBS-1..OBS-3 from bead pg2-m482k's design, the
// beads follow-up to pg-connector-pr-github's bead pg2-ph0o4,
// pg-connector-thread-slack's bead pg2-kjdfi and pg-connector-issue-jira's
// bead pg2-ltddq).
//
// Why this backend owns a log at all: it is a one-shot process, so
// Prometheus cannot scrape it, and a failed or killed call used to leave
// nothing behind. On 2026-10-06 the umbrella SIGKILLed 67 backend calls at
// its 30s deadline (a beads-backed query and a GitHub-backed one timing out in
// the same millisecond, during a host-wide load spike), and none of them
// appeared in any log, so the cluster could not be attributed to a command or
// a duration. Each call now appends ONE final line to a file this backend
// owns, in the phillipgreenii JSONL logging standard (phillipgreenii-nix-
// support-apps ADR 0038: required time / level / msg), preceded by a start row
// and followed (while it runs long) by heartbeat rows, so a call that never
// reaches its final row is still on record with how long it had run.
//
// Ownership (OBS-1/OBS-3): the path is this backend's own, resolved from the
// environment here -- NOT from pg-connector's config -- and the registration
// of the file as a Loki log source lives in this repo's nix module
// (darwin/modules/pg-connector-issue-beads), not in pg-connector. The writer
// mechanics are shared with the other backends in pkg/eventlog; this package
// keeps what is specific to the bd CLI: the Event shape and the per-call
// recorder of bd executions.
//
// No alert rules ship with this log. The diagnostic need is attribution
// (which bd command, how long), which the rows answer when queried; the
// generic error-rate alert is switched off in the nix module so the first
// registration does not introduce paging.
//
// Failure policy: writing the event log is strictly best effort and never
// changes an op's result or exit code.
package eventlog

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	evlog "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/eventlog"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// ServiceName is this backend's Loki service_name (the logSources attribute
// name in darwin/modules/pg-connector-issue-beads, whose default glob is
// ${XDG_STATE_HOME}/<name>/*.jsonl) and the directory under the state home
// that holds the log.
const ServiceName = "pg-connector-issue-beads"

// FileName is the event log's name inside its directory; the rotated copy is
// FileName+".1" and the rotation lock FileName+".lock". Neither matches the
// registered *.jsonl glob.
const FileName = evlog.FileName

// MaxBytes is the size past which the log is rotated (one rotated copy is
// kept, so the worst case on disk is about twice this).
const MaxBytes = evlog.MaxBytes

// EnvPath overrides the log path (used by tests and for relocating the log);
// the value "off" disables the event log. It sits beside the backend's other
// env vars, PG_CONNECTOR_ISSUE_BEADS_DIR and _ACTOR.
const EnvPath = "PG_CONNECTOR_ISSUE_BEADS_EVENTS_FILE"

// maxArgvBytes caps the bd argv copied into an event.
const maxArgvBytes = 256

// Event is one line of events.jsonl: one backend call.
//
// The embedded evlog.Base carries time/level/msg (the JSONL standard's
// required fields), service, version, pid, op, duration_ms and the wire
// taxonomy error_code/error. The remaining fields are what only this backend
// observes.
type Event struct {
	evlog.Base

	// BDCalls is how many bd executions the call made. Absent when the call
	// ran none.
	BDCalls int `json:"bd_calls,omitempty"`
	// BDSlowestMS and BDSlowestArgv are the longest single bd execution of
	// the call and its argv (capped), so a slow or killed call names the
	// command that was slow. Both are absent when the call ran no bd.
	BDSlowestMS   *int64 `json:"bd_slowest_ms,omitempty"`
	BDSlowestArgv string `json:"bd_slowest_argv,omitempty"`
}

// Sink receives finished events and in-flight rows.
type Sink interface {
	Write(Event)
	WriteProgress(evlog.ProgressEvent)
}

// ---------------------------------------------------------------------
// Per-call recorder, carried on the context.
// ---------------------------------------------------------------------

// recorder collects what the backend learned while running that the wrapper
// cannot see from outside.
type recorder struct {
	mu          sync.Mutex
	calls       int
	slowest     time.Duration
	slowestArgv string
}

type recorderKey struct{}

// RecordBDCall notes one bd execution (its argv and how long it ran,
// whether it succeeded or not) on the in-flight call's event. It is a no-op
// when ctx carries no recorder (every unit test that calls the backend
// directly), so callers never need to guard it.
func RecordBDCall(ctx context.Context, argv []string, elapsed time.Duration) {
	r, ok := ctx.Value(recorderKey{}).(*recorder)
	if !ok {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	if r.calls == 1 || elapsed > r.slowest {
		r.slowest = elapsed
		r.slowestArgv = evlog.Truncate(strings.Join(argv, " "), maxArgvBytes)
	}
}

// ---------------------------------------------------------------------
// Instrumenting a dispatch table.
// ---------------------------------------------------------------------

// heartbeatInterval is how often a still-running call writes a heartbeat row;
// tests swap it to a few milliseconds.
var heartbeatInterval = evlog.HeartbeatInterval

// Instrument returns a copy of table whose every handler writes a start row
// before it runs, a heartbeat row every evlog.HeartbeatInterval while it is
// still running, and one Event to sink after it returns. The wrapped
// handler's result and error are passed through untouched. now is injectable
// for tests.
func Instrument(table scriptout.DispatchTable, sink Sink, version string, now func() time.Time) scriptout.DispatchTable {
	if sink == nil {
		return table
	}
	progress := func(op string, args json.RawMessage, phase string, start, at time.Time) {
		sink.WriteProgress(evlog.NewProgressEvent(ServiceName, op, version, phase, args, start, at))
	}
	return evlog.WrapProgress(table, now, func(ctx context.Context, op string) (context.Context, evlog.Finish) {
		rec := &recorder{}
		ctx = context.WithValue(ctx, recorderKey{}, rec)
		return ctx, func(start, end time.Time, err error) {
			sink.Write(buildEvent(op, version, start, end, err, rec))
		}
	}, progress, heartbeatInterval)
}

func buildEvent(op, version string, start, end time.Time, err error, rec *recorder) Event {
	ev := Event{Base: evlog.NewBase(ServiceName, op, version, start, end, err)}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if rec.calls > 0 {
		ms := rec.slowest.Milliseconds()
		ev.BDCalls = rec.calls
		ev.BDSlowestMS = &ms
		ev.BDSlowestArgv = rec.slowestArgv
	}
	return ev
}

// ---------------------------------------------------------------------
// File sink.
// ---------------------------------------------------------------------

// FileSink appends events to Path, rotating to Path+".1" once the file
// exceeds MaxBytes.
type FileSink struct {
	Path     string
	MaxBytes int64
}

// Write appends ev as one line. Errors are swallowed (see the package doc's
// failure policy).
func (s FileSink) Write(ev Event) { evlog.WriteBestEffort(s.Path, s.MaxBytes, ev) }

// WriteProgress appends an in-flight row. Errors are swallowed like Write's.
func (s FileSink) WriteProgress(ev evlog.ProgressEvent) {
	evlog.WriteBestEffort(s.Path, s.MaxBytes, ev)
}

// Line is ev as one line of JSON ending in a newline.
func Line(ev Event) ([]byte, error) { return evlog.Line(ev) }

// Path resolves the event log path from the environment: EnvPath if set, else
// $XDG_STATE_HOME/pg-connector-issue-beads/events.jsonl, else the XDG default
// $HOME/.local/state. It returns "" when no home can be determined.
func Path(getenv func(string) string) string { return evlog.Path(getenv, EnvPath, ServiceName) }

// SinkFromEnv builds the production sink, or nil when the log is disabled
// (EnvPath=off) or no path can be determined.
func SinkFromEnv(getenv func(string) string) Sink {
	p := evlog.Resolve(getenv, EnvPath, ServiceName)
	if p == "" {
		return nil
	}
	return FileSink{Path: p, MaxBytes: MaxBytes}
}
