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
//
// The writer mechanics (common fields, rotation, path rule, call timing) live
// in the shared pkg/eventlog, which pg-connector-thread-slack uses too (bead
// pg2-kjdfi); this package keeps what is specific to GitHub: the Event shape
// with its GraphQL budget fields and the per-call rate-limit recorder.
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
// name in darwin/modules/pg-connector-pr-github, whose default glob is
// ${XDG_STATE_HOME}/<name>/*.jsonl) and the directory under the state home
// that holds the log.
const ServiceName = "pg-connector-pr-github"

// FileName is the event log's name inside its directory; the rotated copy is
// FileName+".1" and the rotation lock FileName+".lock". Neither matches the
// registered *.jsonl glob.
const FileName = evlog.FileName

// MaxBytes is the size past which the log is rotated (one rotated copy is
// kept, so the worst case on disk is about twice this).
const MaxBytes = evlog.MaxBytes

// EnvPath overrides the log path (used by tests and for relocating the log);
// the value "off" disables the event log.
const EnvPath = "PG_CONNECTOR_PR_GITHUB_EVENTS_FILE"

// Log levels, per the JSONL standard's lowercase enum.
const (
	LevelInfo  = evlog.LevelInfo
	LevelWarn  = evlog.LevelWarn
	LevelError = evlog.LevelError
)

// Event is one line of events.jsonl: one backend call.
//
// The embedded evlog.Base carries time/level/msg (the JSONL standard's
// required fields), service, version, pid, op, duration_ms and the wire
// taxonomy error_code/error. The graphql_* fields are present only on calls
// that read the GraphQL rate limit (every call guarded by the rate reserve:
// list, search).
type Event struct {
	evlog.Base

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
	// GraphQLCost is the points an op=list call spent on its search requests,
	// summed over every page of every search string (bead pg2-x3h8c.2). Every
	// op=list row carries it, 0 included (the ids-only path spends none). The
	// row carries no query name, so a reader attributes cost to one query by
	// running a single list call at a time. The separate rate-limit probe of an
	// ids-only call is not counted (it is uncharged); the enriched path takes no
	// probe, its reading rides on the search response (bead pg2-cw6b3.3), so a
	// call refused below the reserve there logs the cost of the one search that
	// carried the reading.
	GraphQLCost  *int `json:"graphql_cost,omitempty"`
	BelowReserve bool `json:"below_reserve,omitempty"`
}

// LevelForCode maps a wire error code to a log level (see evlog.LevelForCode).
func LevelForCode(code string) string { return evlog.LevelForCode(code) }

// Sink receives finished events.
type Sink interface {
	Write(Event)
}

// heartbeatInterval is how often a still-running call writes a heartbeat row;
// tests swap it to a few milliseconds.
var heartbeatInterval = evlog.HeartbeatInterval

// ProgressSink is the optional extension of Sink that also receives in-flight
// rows (a call's start and its heartbeats; bead pg2-5dyz2). A Sink that does
// not implement it gets final rows only.
type ProgressSink interface {
	WriteProgress(evlog.ProgressEvent)
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
// (list_activity fans out), hence the mutex.
type recorder struct {
	mu   sync.Mutex
	rate *rateLimitReading
	cost int
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

// AddGraphQLCost adds points to the in-flight call's GraphQL cost (bead
// pg2-x3h8c.2): each search request calls it with that request's own
// rateLimit.cost, and the op=list row logs the sum as graphql_cost. It is a
// no-op when ctx carries no recorder.
func AddGraphQLCost(ctx context.Context, points int) {
	r, ok := ctx.Value(recorderKey{}).(*recorder)
	if !ok {
		return
	}
	r.mu.Lock()
	r.cost += points
	r.mu.Unlock()
}

// ---------------------------------------------------------------------
// Instrumenting a dispatch table.
// ---------------------------------------------------------------------

// Instrument returns a copy of table whose every handler appends one Event
// to sink after the call returns. When sink is also a ProgressSink, each call
// additionally writes a start row before it runs and a heartbeat row every
// evlog.HeartbeatInterval while it is still running (bead pg2-5dyz2): a call
// SIGKILLed by its caller's deadline never reaches the final row, and these
// are what make it show up in the log at all. The wrapped handler's result and
// error are passed through untouched. now is injectable for tests.
func Instrument(table scriptout.DispatchTable, sink Sink, version string, now func() time.Time) scriptout.DispatchTable {
	if sink == nil {
		return table
	}
	var progress evlog.Progress
	if ps, ok := sink.(ProgressSink); ok {
		progress = func(op string, args json.RawMessage, phase string, start, at time.Time) {
			ps.WriteProgress(evlog.NewProgressEvent(ServiceName, op, version, phase, args, start, at))
		}
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
	// Every op=list row carries a numeric graphql_cost, 0 when the call made
	// no search request (ids-only), so a reader
	// can tell "cost 0" from "cost not logged".
	if op == "list" {
		cost := rec.cost
		ev.GraphQLCost = &cost
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

// WriteProgress appends an in-flight row (see ProgressSink). Errors are
// swallowed like Write's.
func (s FileSink) WriteProgress(ev evlog.ProgressEvent) {
	evlog.WriteBestEffort(s.Path, s.MaxBytes, ev)
}

// Line is ev as one line of JSON ending in a newline.
func Line(ev Event) ([]byte, error) { return evlog.Line(ev) }

// Path resolves the event log path from the environment: EnvPath if set,
// else $XDG_STATE_HOME/pg-connector-pr-github/events.jsonl, else the XDG default
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
