package logs

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/eventlog"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/router"
	evlog "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/eventlog"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// RequestRow is one kind=request row: one request answered (the event log,
// written by the daemon) or one request the client could not hand to the daemon
// (the client log, outcome daemon_unreachable).
//
// The embedded evlog.Base carries time/level/msg, service, version, pid, op,
// duration_ms and the wire taxonomy error_code/error. FailureClass is auth or
// rate_limited when the answer was a rejected credential or a throttle (the two
// classes the stateless backend's log used, so the same alerts port over) and is
// absent otherwise. ServedFrom, Stale and AgeSeconds are present only when the
// handler attached them (router.Annotate). WaitMS is the request's wait before
// its handler ran.
type RequestRow struct {
	evlog.Base
	Kind     string `json:"kind"`
	Instance string `json:"instance"`
	Outcome  string `json:"outcome"`

	FailureClass string `json:"failure_class,omitempty"`

	ServedFrom string `json:"served_from,omitempty"`
	Stale      *bool  `json:"stale,omitempty"`
	AgeSeconds *int64 `json:"age_seconds,omitempty"`
	WaitMS     int64  `json:"wait_ms"`
}

// failureClassOf is the failure_class of a failed answer: auth for an
// unauthenticated code, rate_limited when the error text reads as an HTTP 429,
// "" otherwise.
func failureClassOf(e *scriptout.Error) string {
	if e == nil {
		return ""
	}
	if e.Code == codeUnauthenticated {
		return eventlog.ClassAuth
	}
	return eventlog.ClassifyFailure(e.Message)
}

// notApplicable reports whether e is the soft answer to a request the umbrella
// fan-out offered to a backend that cannot own it: a query this backend does not
// define (query_not_recognized), or an id that is not a Jira key (not_found
// carrying eventlog.NotJiraKeyText). Neither is a fault of the daemon or of Jira.
func notApplicable(e *scriptout.Error) bool {
	if e == nil {
		return false
	}
	switch e.Code {
	case codeQueryNotRecognized:
		return true
	case codeNotFound:
		return strings.Contains(e.Message, eventlog.NotJiraKeyText)
	}
	return false
}

// newRequestRow builds the row for one finished request. A nil e is success.
func newRequestRow(service, version, op, instance string, start, end time.Time, e *scriptout.Error) RequestRow {
	b := evlog.NewBase(service, op, version, start, end, nil)
	row := RequestRow{Base: b, Kind: KindRequest, Instance: instance, Outcome: OutcomeOK}
	switch {
	case notApplicable(e):
		row.Outcome = OutcomeNotApplicable
		row.ErrorCode = e.Code
		row.Level = evlog.LevelInfo
		row.Error = evlog.TruncateHeadTail(e.Message, evlog.MaxErrorBytes, evlog.ErrorHeadBytes)
		row.Msg = fmt.Sprintf("%s not applicable: %s", op, e.Code)
	case e != nil:
		row.Outcome = OutcomeError
		row.ErrorCode = e.Code
		row.Level = evlog.LevelForCode(e.Code)
		row.Error = evlog.TruncateHeadTail(e.Message, evlog.MaxErrorBytes, evlog.ErrorHeadBytes)
		row.Msg = fmt.Sprintf("%s failed: %s", op, e.Code)
		row.FailureClass = failureClassOf(e)
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

// OriginRow is one kind=origin row: one call to the origin (Jira, through
// pjira), written for every spend the governor records, whether a request
// caused it or not. Cost is what the call spent from its bucket; Remaining is
// present only when the bucket had a reading in force. FailureClass is auth
// for an unauthenticated outcome and rate_limited for a rate_limited one, and
// absent otherwise.
type OriginRow struct {
	header
	Kind      string `json:"kind"`
	Purpose   string `json:"purpose"`
	Group     string `json:"group"`
	BatchSize int    `json:"batch_size"`
	Bucket    string `json:"bucket"`
	Cost      int64  `json:"cost"`
	Remaining *int64 `json:"remaining,omitempty"`

	Outcome      string `json:"outcome"`
	FailureClass string `json:"failure_class,omitempty"`
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
		Cost:      ev.Cost,
		Outcome:   outcome,
	}
	if ev.Remaining >= 0 {
		r := ev.Remaining
		row.Remaining = &r
	}
	switch outcome {
	case OutcomeUnauthenticated:
		row.FailureClass = eventlog.ClassAuth
	case OutcomeRateLimited:
		row.FailureClass = eventlog.ClassRateLimited
	}
	return row
}

// EventLog is the daemon's event log (events.jsonl, Loki service_name
// pg-connector-issue-jira-daemon). The zero value is not usable: build one with
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
