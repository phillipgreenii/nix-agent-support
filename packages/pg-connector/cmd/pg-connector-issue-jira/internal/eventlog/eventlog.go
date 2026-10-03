// Package eventlog is pg-connector-issue-jira's own rotating JSONL event log
// (bead pg2-ltddq; contract OBS-1..OBS-3 from bead pg2-m482k's design, the
// Jira follow-up to pg-connector-pr-github's bead pg2-ph0o4 and
// pg-connector-thread-slack's bead pg2-kjdfi).
//
// Why this backend owns a log at all: it is a one-shot process, so
// Prometheus cannot scrape it, and a failed call used to leave nothing behind
// except the JSON error envelope on stdout (which pg-router and pg-desk
// consume and discard). Each call now appends ONE line to a file this backend
// owns, in the phillipgreenii JSONL logging standard (phillipgreenii-nix-
// support-apps ADR 0038: required time / level / msg), so Loki can ingest it
// and LogQL alert rules can read the signals out of it.
//
// Ownership (OBS-1/OBS-3): the path is this backend's own, resolved from the
// environment here -- NOT from pg-connector's config -- and the registration
// of the file as a Loki log source plus its alert rules live in this repo's
// nix module (darwin/modules/pg-connector-issue-jira), not in pg-connector.
// pg-connector stays unbound to OTel. The writer mechanics are shared with the
// other backends in pkg/eventlog; this package keeps what is specific to
// Jira-over-pjira: the Event shape and the per-call recorder.
//
// What this backend can honestly say about the three OBS-2 conditions:
//
//   - availability: every call whose pjira run could not produce a usable
//     answer is error_code=unavailable (pjira missing, a non-2xx status that
//     is not an auth or not-found one, a decode failure).
//   - authentication: two observations. A call that pjira answered with a
//     401/403 is error_code=unauthenticated. And the backend implements
//     auth_status through `pjira auth-status`, whose well-formed answer is
//     never a wire error (the state is in the result), so the event carries
//     auth_state with pjira's own state string. Both are tagged
//     failure_class=auth, so one field answers "is auth failing".
//   - quota: Jira Cloud signals throttling with HTTP 429, and pjira surfaces
//     that status in its error text. The backend reads NO remaining/limit
//     figure, header or Retry-After, so there is no budget to log and no
//     "remaining below X" rule; what it can see is the throttle itself, which
//     it tags failure_class=rate_limited (the wire error_code stays
//     unavailable).
//
// Failure policy: writing the event log is strictly best effort and never
// changes an op's result or exit code.
package eventlog

import (
	"context"
	"strings"
	"sync"
	"time"

	evlog "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/eventlog"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// ServiceName is this backend's Loki service_name (the logSources attribute
// name in darwin/modules/pg-connector-issue-jira, whose default glob is
// ${XDG_STATE_HOME}/<name>/*.jsonl) and the directory under the state home
// that holds the log.
const ServiceName = "pg-connector-issue-jira"

// FileName is the event log's name inside its directory; the rotated copy is
// FileName+".1" and the rotation lock FileName+".lock". Neither matches the
// registered *.jsonl glob.
const FileName = evlog.FileName

// MaxBytes is the size past which the log is rotated (one rotated copy is
// kept, so the worst case on disk is about twice this).
const MaxBytes = evlog.MaxBytes

// EnvPath overrides the log path (used by tests and for relocating the log);
// the value "off" disables the event log. It sits beside the backend's other
// env vars, PG_CONNECTOR_ISSUE_JIRA_BINARY and _PROJECT.
const EnvPath = "PG_CONNECTOR_ISSUE_JIRA_EVENTS_FILE"

// failure_class values.
const (
	// ClassAuth: Jira rejected the credential (a wire unauthenticated answer,
	// or an auth_status check that came back MISSING / UNAUTHENTICATED /
	// FORBIDDEN).
	ClassAuth = "auth"
	// ClassRateLimited: pjira's error text carried an HTTP 429 status.
	ClassRateLimited = "rate_limited"
)

// pjira auth-status states (phillipg-nix-repo-base modules/jira/pkg/pjira
// model.go AuthState). stateOK is the only healthy one; stateError is also
// what the backend records when pjira could not be run at all.
const (
	stateOK              = "OK"
	stateMissing         = "MISSING"
	stateUnauthenticated = "UNAUTHENTICATED"
	stateForbidden       = "FORBIDDEN"
	stateError           = "ERROR"
)

// NormalizeAuthState maps pjira's auth-status stdout (and whether running it
// failed) to the state recorded on the event: pjira's own state when it
// printed a known one, otherwise ERROR (pjira missing, a transport failure, an
// unrecognised line).
func NormalizeAuthState(stdout string, runFailed bool) string {
	s := strings.ToUpper(strings.TrimSpace(stdout))
	switch s {
	case stateOK, stateMissing, stateUnauthenticated, stateForbidden, stateError:
		return s
	}
	if !runFailed && s != "" {
		return s
	}
	return stateError
}

// rateLimitMarkers are the lowercase substrings that mark pjira's error text
// as an HTTP 429. pjira formats a non-2xx as "status <resp.Status>" and Go's
// resp.Status for 429 is "429 Too Many Requests" (client.go GetIssue / Search
// / CreateIssue / Transition / AddComment). "status 429" is matched rather
// than a bare "429" so an issue key such as PROJ-429 never reads as a throttle.
var rateLimitMarkers = []string{"status 429", "too many requests"}

// ClassifyFailure returns ClassRateLimited when text (a pjira error message)
// reads as an HTTP 429, else "".
func ClassifyFailure(text string) string {
	lower := strings.ToLower(text)
	for _, m := range rateLimitMarkers {
		if strings.Contains(lower, m) {
			return ClassRateLimited
		}
	}
	return ""
}

// Event is one line of events.jsonl: one backend call.
//
// The embedded evlog.Base carries time/level/msg (the JSONL standard's
// required fields), service, version, pid, op, duration_ms and the wire
// taxonomy error_code/error. The remaining fields are what only this backend
// observes.
type Event struct {
	evlog.Base

	// PjiraCalls is how many pjira executions the call made (list makes up to
	// two). Absent when the call ran none.
	PjiraCalls int `json:"pjira_calls,omitempty"`
	// AuthState is pjira's auth-status state (OK, MISSING, UNAUTHENTICATED,
	// FORBIDDEN, ERROR); present only on auth_status calls.
	AuthState string `json:"auth_state,omitempty"`
	// FailureClass is ClassAuth or ClassRateLimited; absent otherwise.
	FailureClass string `json:"failure_class,omitempty"`
}

// LevelForCode maps a wire error code to a log level (see evlog.LevelForCode).
func LevelForCode(code string) string { return evlog.LevelForCode(code) }

// Sink receives finished events.
type Sink interface {
	Write(Event)
}

// ---------------------------------------------------------------------
// Per-call recorder, carried on the context.
// ---------------------------------------------------------------------

// recorder collects what the backend learned while running that the wrapper
// cannot see from outside.
type recorder struct {
	mu        sync.Mutex
	calls     int
	class     string
	authState string
}

type recorderKey struct{}

func recorderFrom(ctx context.Context) *recorder {
	r, _ := ctx.Value(recorderKey{}).(*recorder)
	return r
}

// RecordPjiraCall notes one pjira execution on the in-flight call's event. It
// is a no-op when ctx carries no recorder (every unit test that calls the
// backend directly), so callers never need to guard it.
func RecordPjiraCall(ctx context.Context) {
	r := recorderFrom(ctx)
	if r == nil {
		return
	}
	r.mu.Lock()
	r.calls++
	r.mu.Unlock()
}

// RecordPjiraFailure notes the text of a failed pjira run, to classify a
// throttle (ClassRateLimited). The text is never written to the event itself --
// the call's error already carries it, truncated, as error. A no-op when ctx
// carries no recorder.
func RecordPjiraFailure(ctx context.Context, text string) {
	r := recorderFrom(ctx)
	if r == nil {
		return
	}
	class := ClassifyFailure(text)
	r.mu.Lock()
	r.class = class
	r.mu.Unlock()
}

// RecordAuthState notes the result of an auth-status check (see
// NormalizeAuthState). A no-op when ctx carries no recorder.
func RecordAuthState(ctx context.Context, state string) {
	r := recorderFrom(ctx)
	if r == nil {
		return
	}
	r.mu.Lock()
	r.authState = state
	r.mu.Unlock()
}

// ---------------------------------------------------------------------
// Instrumenting a dispatch table.
// ---------------------------------------------------------------------

// Instrument returns a copy of table whose every handler appends one Event to
// sink after the call returns. The wrapped handler's result and error are
// passed through untouched. now is injectable for tests.
func Instrument(table scriptout.DispatchTable, sink Sink, version string, now func() time.Time) scriptout.DispatchTable {
	if sink == nil {
		return table
	}
	return evlog.Wrap(table, now, func(ctx context.Context, op string) (context.Context, evlog.Finish) {
		rec := &recorder{}
		ctx = context.WithValue(ctx, recorderKey{}, rec)
		return ctx, func(start, end time.Time, err error) {
			sink.Write(buildEvent(op, version, start, end, err, rec))
		}
	})
}

func buildEvent(op, version string, start, end time.Time, err error, rec *recorder) Event {
	ev := Event{Base: evlog.NewBase(ServiceName, op, version, start, end, err)}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	ev.PjiraCalls = rec.calls
	if err != nil {
		// A wire unauthenticated answer is an auth failure by definition; any
		// other failed call is tagged only when its pjira text read as a
		// throttle.
		if ev.ErrorCode == "unauthenticated" {
			ev.FailureClass = ClassAuth
		} else {
			ev.FailureClass = rec.class
		}
		return ev
	}
	if rec.authState != "" {
		// auth_status answers with a well-formed result even when the
		// credential is bad, so the event is the only place the failure shows.
		ev.AuthState = rec.authState
		switch rec.authState {
		case stateOK:
		case stateMissing, stateUnauthenticated, stateForbidden:
			ev.FailureClass = ClassAuth
			ev.Level = evlog.LevelError
			ev.Msg = op + " failed: " + rec.authState
		default:
			ev.Level = evlog.LevelWarn
			ev.Msg = op + " failed: " + rec.authState
		}
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

// Line is ev as one line of JSON ending in a newline.
func Line(ev Event) ([]byte, error) { return evlog.Line(ev) }

// Path resolves the event log path from the environment: EnvPath if set, else
// $XDG_STATE_HOME/pg-connector-issue-jira/events.jsonl, else the XDG default
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
