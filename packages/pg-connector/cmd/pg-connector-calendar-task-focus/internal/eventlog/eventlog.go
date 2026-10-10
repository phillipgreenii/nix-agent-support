// Package eventlog is pg-connector-calendar-task-focus's own rotating JSONL
// event log (bead pg2-t7me1.4; contract OBS-1..OBS-3 from bead pg2-m482k's
// design, the pg-task-focus follow-up to pg-connector-issue-beads's bead
// pg2-5dyz2).
//
// Why this backend owns a log at all: it is launched once per call (see the
// backend's behavior doc, "Process model"), so Prometheus cannot scrape it,
// and a call the umbrella SIGKILLs at its deadline would otherwise leave
// nothing behind. Each call appends a start row, heartbeat rows while it runs
// long, and ONE final row to a file this backend owns, in the phillipgreenii
// JSONL logging standard (phillipgreenii-nix-support-apps ADR 0038: required
// time / level / msg), so a call that never reaches its final row is still on
// record. The daemon's own telemetry already counts these calls
// (`http_requests_total{client="connector"}`), so this log answers only the
// question the daemon cannot: why a call never arrived or never finished.
//
// Ownership (OBS-1/OBS-3): the path is this backend's own, resolved from the
// environment here, NOT from pg-connector's config, and the registration of
// the file as a Loki log source lives in this repo's nix module
// (darwin/modules/pg-connector-calendar-task-focus). The writer mechanics are
// shared with the other backends in pkg/eventlog; this package keeps what is
// specific to a daemon-backed backend: the Event shape and the per-call
// recorder of daemon requests.
//
// No alert rules ship with this log: the daemon's own "Daemon down" alert
// covers the cause, and the umbrella's fan-out reports the effect (an
// unavailable source). The generic error-rate alert is switched off in the nix
// module for the same reason the siblings do.
//
// Failure policy: writing the event log is strictly best effort and never
// changes an op's result or exit code.
package eventlog

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	evlog "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/eventlog"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// ServiceName is this backend's Loki service_name (the logSources attribute
// name in darwin/modules/pg-connector-calendar-task-focus, whose default glob
// is ${XDG_STATE_HOME}/<name>/*.jsonl) and the directory under the state home
// that holds the log.
const ServiceName = "pg-connector-calendar-task-focus"

// FileName is the event log's name inside its directory; the rotated copy is
// FileName+".1" and the rotation lock FileName+".lock". Neither matches the
// registered *.jsonl glob.
const FileName = evlog.FileName

// MaxBytes is the size past which the log is rotated (one rotated copy is
// kept, so the worst case on disk is about twice this).
const MaxBytes = evlog.MaxBytes

// EnvPath overrides the log path (used by tests and for relocating the log);
// the value "off" disables the event log.
const EnvPath = "PG_CONNECTOR_CALENDAR_TASK_FOCUS_EVENTS_FILE"

// Where in a daemon request a failed call broke (failure_stage).
const (
	// StageConnect: the daemon could not be reached (connection refused, DNS,
	// timeout, a body that could not be read).
	StageConnect = "connect"
	// StageStatus: the daemon answered an HTTP status other than 200 (not
	// ready, refused window, an internal error).
	StageStatus = "status"
	// StageDecode: the daemon answered 200 with a body that did not decode.
	StageDecode = "decode"
)

// Event is one line of events.jsonl: one backend call.
//
// The embedded evlog.Base carries time/level/msg (the JSONL standard's
// required fields), service, version, pid, op, duration_ms and the wire
// taxonomy error_code/error. The remaining fields are what only this backend
// observes.
type Event struct {
	evlog.Base

	// DaemonRequests is how many HTTP requests the call made to the daemon.
	// Absent when the call made none (a call refused before reaching it).
	DaemonRequests int `json:"daemon_requests,omitempty"`
	// DaemonStatus is the HTTP status of the last request the daemon answered;
	// absent when no request was answered.
	DaemonStatus int `json:"daemon_status,omitempty"`
	// FailureStage is where the call broke (the Stage* constants); absent on
	// success and on a call that failed before any request.
	FailureStage string `json:"failure_stage,omitempty"`
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
	mu       sync.Mutex
	requests int
	status   int
	stage    string
}

type recorderKey struct{}

func recorderFrom(ctx context.Context) *recorder {
	r, _ := ctx.Value(recorderKey{}).(*recorder)
	return r
}

// RecordRequest notes one daemon request on the in-flight call's event: status
// is the HTTP status the daemon answered (0 when it never did) and stage is
// the Stage* constant where the request broke ("" on success). It is a no-op
// when ctx carries no recorder (every unit test that calls the backend
// directly), so callers never need to guard it.
func RecordRequest(ctx context.Context, status int, stage string) {
	r := recorderFrom(ctx)
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requests++
	if status != 0 {
		r.status = status
	}
	if stage != "" {
		r.stage = stage
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
	ev.DaemonRequests = rec.requests
	ev.DaemonStatus = rec.status
	// A failure stage is only meaningful on a failed call: a call that
	// recovered must not look like a failure to a LogQL rule.
	if err != nil {
		ev.FailureStage = rec.stage
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
// $XDG_STATE_HOME/pg-connector-calendar-task-focus/events.jsonl, else the XDG
// default $HOME/.local/state. It returns "" when no home can be determined.
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
