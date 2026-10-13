package logs

import (
	"context"
	"fmt"
	"time"

	evlog "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/eventlog"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/router"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// RequestRow is one kind=request row: one request answered (the event log,
// written by the daemon) or one request the client could not hand to the daemon
// (the client log, outcome daemon_unreachable).
//
// The embedded evlog.Base carries time/level/msg, service, version, pid, op,
// duration_ms and the wire taxonomy error_code/error. ServedFrom, Stale and
// AgeSeconds are present only when the handler attached them (router.Annotate);
// a request refused before any handler ran (migrating, unknown op, bad config)
// carries none. WaitMS is the request's wait before its handler ran (see
// router.Annotations.Wait); for a request answered from the store it is the
// router's own measurement, normally 0.
type RequestRow struct {
	evlog.Base
	Kind     string `json:"kind"`
	Instance string `json:"instance"`
	Outcome  string `json:"outcome"`

	ServedFrom string `json:"served_from,omitempty"`
	Stale      *bool  `json:"stale,omitempty"`
	AgeSeconds *int64 `json:"age_seconds,omitempty"`
	WaitMS     int64  `json:"wait_ms"`
}

// newRequestRow builds the row for one finished request. A nil e is success.
func newRequestRow(service, version, op, instance string, start, end time.Time, e *scriptout.Error) RequestRow {
	b := evlog.NewBase(service, op, version, start, end, nil)
	row := RequestRow{Base: b, Kind: KindRequest, Instance: instance, Outcome: OutcomeOK}
	if e != nil {
		row.Outcome = OutcomeError
		row.ErrorCode = e.Code
		row.Level = evlog.LevelForCode(e.Code)
		row.Error = evlog.TruncateHeadTail(e.Message, evlog.MaxErrorBytes, evlog.ErrorHeadBytes)
		row.Msg = fmt.Sprintf("%s failed: %s", op, e.Code)
	}
	return row
}

// applyAnnotations copies what the handler attached onto the row. Nothing is
// attached (no served_from, not stale, no age) leaves the optional fields out;
// the wait is always reported.
func (r *RequestRow) applyAnnotations(a router.Annotations) {
	r.WaitMS = a.Wait.Milliseconds()
	if a.ServedFrom == "" && !a.Stale && a.AgeSeconds == 0 {
		return
	}
	r.ServedFrom = a.ServedFrom
	stale, age := a.Stale, a.AgeSeconds
	r.Stale = &stale
	r.AgeSeconds = &age
}

// OriginRow is one kind=origin row: one call to the origin (GitHub), written
// for every spend the governor records, whether a request caused it or not.
//
// graphql_cost and rest_requests are always present (0 for the bucket the call
// did not spend from), so a LogQL sum over either is exact; graphql_remaining
// and rest_remaining are present only when the call's own bucket had a
// reading in force. Bucket says which bucket was spent (graphql, rest_core or
// rest_search), which the two rest_* fields do not distinguish.
type OriginRow struct {
	header
	Kind      string `json:"kind"`
	Purpose   string `json:"purpose"`
	Group     string `json:"group"`
	BatchSize int    `json:"batch_size"`
	Bucket    string `json:"bucket"`

	GraphQLCost      int64  `json:"graphql_cost"`
	GraphQLRemaining *int64 `json:"graphql_remaining,omitempty"`
	RESTRequests     int64  `json:"rest_requests"`
	RESTRemaining    *int64 `json:"rest_remaining,omitempty"`

	Outcome string `json:"outcome"`
}

// newOriginRow turns one spend observation into its row. An empty outcome
// means the call finished without a failure word and is written as "ok".
func newOriginRow(version string, ev governor.SpendEvent) OriginRow {
	outcome := ev.Outcome
	if outcome == "" {
		outcome = OutcomeOK
	}
	level := evlog.LevelInfo
	if outcome != OutcomeOK {
		level = evlog.LevelWarn
	}
	row := OriginRow{
		header:    newHeader(ev.At, level, fmt.Sprintf("origin %s %s", ev.Bucket, outcome), version),
		Kind:      KindOrigin,
		Purpose:   string(ev.Purpose),
		Group:     ev.Group,
		BatchSize: ev.BatchSize,
		Bucket:    string(ev.Bucket),
		Outcome:   outcome,
	}
	var remaining *int64
	if ev.Remaining >= 0 {
		r := ev.Remaining
		remaining = &r
	}
	if ev.Bucket == governor.BucketGraphQL {
		row.GraphQLCost, row.GraphQLRemaining = ev.Cost, remaining
	} else {
		row.RESTRequests, row.RESTRemaining = ev.Cost, remaining
	}
	return row
}

// EventLog is the daemon's event log (events.jsonl, Loki service_name
// pg-connector-github). The zero value is not usable: build one with
// NewEventLog.
type EventLog struct {
	// Path is the file written; MaxBytes the size past which it rotates to
	// Path+".1".
	Path     string
	MaxBytes int64
	// Version is stamped into every row.
	Version string
	// Now is the clock; nil uses time.Now.
	Now func() time.Time
}

// NewEventLog returns the event log inside stateDir, rotating at DaemonMaxBytes.
func NewEventLog(stateDir, version string) *EventLog {
	return &EventLog{Path: EventLogPath(stateDir), MaxBytes: DaemonMaxBytes, Version: version}
}

func (l *EventLog) now() time.Time {
	if l.Now != nil {
		return l.Now()
	}
	return time.Now()
}

// Middleware writes one kind=request row per request: install it with
// router.Use. It reads what the handler attached from the router's annotation
// carrier after the request was answered, and never changes the answer.
func (l *EventLog) Middleware() router.Middleware {
	return func(next router.Func) router.Func {
		return func(ctx context.Context, c router.Call) scriptout.Response {
			start := l.now()
			resp := next(ctx, c)
			row := newRequestRow(ServiceName, l.Version, c.Request.Op, c.Instance, start, l.now(), resp.Error)
			row.applyAnnotations(router.AnnotationsFrom(ctx))
			write(l.Path, l.MaxBytes, row)
			return resp
		}
	}
}

// OnSpend writes one kind=origin row for a spend observation. It is the
// governor's Options.OnSpend.
func (l *EventLog) OnSpend(ev governor.SpendEvent) {
	if ev.At.IsZero() {
		ev.At = l.now()
	}
	write(l.Path, l.MaxBytes, newOriginRow(l.Version, ev))
}
