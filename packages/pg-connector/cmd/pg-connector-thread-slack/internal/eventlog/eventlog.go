// Package eventlog is pg-connector-thread-slack's own rotating JSONL event log
// (bead pg2-kjdfi; contract OBS-1..OBS-3 from bead pg2-m482k's design, the
// Slack follow-up to pg-connector-pr-github's bead pg2-ph0o4).
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
// nix module (darwin/modules/pg-connector-thread-slack), not in pg-connector.
// pg-connector stays unbound to OTel. The writer mechanics are shared with the
// other backends in pkg/eventlog; this package keeps what is specific to
// Slack-over-claude: the Event shape and the failure recorder.
//
// What this backend can honestly say about the three OBS-2 conditions:
//
//   - availability: every call that could not produce a usable reply is
//     error_code=unavailable, with failure_stage saying where in the claude -p
//     round trip it broke (exec, envelope, claude_error, reply_decode,
//     reply_incomplete).
//   - authentication: the backend holds no Slack credential of its own, so it
//     has no auth check. It can only SEE an auth problem in the text claude
//     itself reports when the call fails (exec stderr, or an is_error result).
//     When that text looks like an auth failure the event carries
//     failure_class=auth. The wire error_code is left as it always was
//     (unavailable): classifying it as unauthenticated would change what
//     pg-connector and its callers see, which is out of scope for an
//     observability change.
//   - quota: NOT exposed. The backend never reads a Slack rate-limit header or
//     a Retry-After, and claude -p's envelope is decoded for result/is_error
//     only, so there is no quota reading to log and no quota alert.
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
// name in darwin/modules/pg-connector-thread-slack, whose default glob is
// ${XDG_STATE_HOME}/<name>/*.jsonl) and the directory under the state home
// that holds the log.
const ServiceName = "pg-connector-thread-slack"

// FileName is the event log's name inside its directory; the rotated copy is
// FileName+".1" and the rotation lock FileName+".lock". Neither matches the
// registered *.jsonl glob.
const FileName = evlog.FileName

// MaxBytes is the size past which the log is rotated (one rotated copy is
// kept, so the worst case on disk is about twice this).
const MaxBytes = evlog.MaxBytes

// EnvPath overrides the log path (used by tests and for relocating the log);
// the value "off" disables the event log. It sits beside the backend's other
// env var, PG_CONNECTOR_THREAD_SLACK_BINARY.
const EnvPath = "PG_CONNECTOR_THREAD_SLACK_EVENTS_FILE"

// Where in the claude -p round trip a failed call broke (failure_stage).
const (
	// StageExec: running the claude binary failed (not on PATH, non-zero
	// exit, timeout); the detail is claude's stderr tail.
	StageExec = "exec"
	// StageEnvelope: claude's own --output-format json outer envelope did not
	// decode.
	StageEnvelope = "envelope"
	// StageClaudeError: the envelope decoded but reported is_error:true; the
	// detail is its result text.
	StageClaudeError = "claude_error"
	// StageReplyDecode: the model's final reply was not the JSON shape the
	// prompt asked for.
	StageReplyDecode = "reply_decode"
	// StageReplyIncomplete: the reply decoded but lacked required fields.
	StageReplyIncomplete = "reply_incomplete"
)

// ClassAuth is the failure_class of a failure whose claude-reported text looks
// like an authentication problem (claude not logged in, an expired token, the
// Slack MCP needing authentication).
const ClassAuth = "auth"

// authMarkers are the lowercase substrings that mark claude-reported failure
// text as an authentication problem. They are matched against claude's own
// stderr / is_error text only (StageExec, StageClaudeError), never against
// Slack content. They come from the messages claude and Slack's API are known
// to use, NOT from a live capture (this backend has no provisioned Slack
// token and tests never call Slack), so this list is the one place to extend
// when a real auth failure turns out to read differently.
var authMarkers = []string{
	"not logged in",
	"please run /login",
	"invalid api key",
	"oauth token has expired",
	"authentication_error",
	"authentication failed",
	"needs authentication",
	"needs-auth",
	"invalid_auth",
	"not_authed",
	"token_revoked",
	"token_expired",
	"unauthorized",
	"unauthenticated",
	"api error: 401",
	"status 401",
	"http 401",
}

// ClassifyFailure returns ClassAuth when text (claude-reported failure text)
// looks like an authentication failure, else "".
func ClassifyFailure(text string) string {
	lower := strings.ToLower(text)
	for _, m := range authMarkers {
		if strings.Contains(lower, m) {
			return ClassAuth
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

	// ClaudeCalls is how many claude -p executions the call made (list makes
	// one per search modifier). Absent when the call failed before any.
	ClaudeCalls int `json:"claude_calls,omitempty"`
	// FailureStage is where the call broke (the Stage* constants); absent on
	// success and on well-formed outcomes such as not_found.
	FailureStage string `json:"failure_stage,omitempty"`
	// FailureClass is ClassAuth when the claude-reported failure text looks
	// like an authentication problem; absent otherwise.
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

// recorder collects what the handler learned while running that the wrapper
// cannot see from outside.
type recorder struct {
	mu     sync.Mutex
	calls  int
	stage  string
	class  string
	failed bool
}

type recorderKey struct{}

func recorderFrom(ctx context.Context) *recorder {
	r, _ := ctx.Value(recorderKey{}).(*recorder)
	return r
}

// RecordClaudeCall notes one claude -p execution on the in-flight call's
// event. It is a no-op when ctx carries no recorder (every unit test that
// calls the backend directly), so callers never need to guard it.
func RecordClaudeCall(ctx context.Context) {
	r := recorderFrom(ctx)
	if r == nil {
		return
	}
	r.mu.Lock()
	r.calls++
	r.mu.Unlock()
}

// RecordFailure notes where the in-flight call broke. detail is the text
// claude reported (used only for StageExec and StageClaudeError, to classify
// an authentication failure); it is never written to the event itself -- the
// call's error already carries it, truncated, as error. A no-op when ctx
// carries no recorder.
func RecordFailure(ctx context.Context, stage, detail string) {
	r := recorderFrom(ctx)
	if r == nil {
		return
	}
	class := ""
	if stage == StageExec || stage == StageClaudeError {
		class = ClassifyFailure(detail)
	}
	r.mu.Lock()
	r.stage, r.class, r.failed = stage, class, true
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
	ev.ClaudeCalls = rec.calls
	// A failure stage is only meaningful on a failed call: a call that
	// recovered (none does today) or that ended in a well-formed outcome must
	// not look like a failure to a LogQL rule.
	if err != nil && rec.failed {
		ev.FailureStage = rec.stage
		ev.FailureClass = rec.class
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
// $XDG_STATE_HOME/pg-connector-thread-slack/events.jsonl, else the XDG default
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
