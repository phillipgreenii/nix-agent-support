// Package wire is the handler-to-core wire layer shared by every adapter
// binary of this module (bead pg2-12gr4.10.3, the shared library the
// pg-router-handler-ccpool and pg-router-handler-command adapters import
// alongside the existing pg-router-ccpool-handler): the request shapes of a
// handler.dispatch and a lifecycle (postStartup/preShutdown) message, the
// reply writers, and the pre-accept decline vocabulary.
//
// The failure-reason strings (BusyReason*), the decline kind values
// (DeclineKind*) and exit code 9 (conformance.ExitBusy) are a
// handler-to-alert contract: they MUST stay byte for byte as they are.
//
// Moved verbatim out of package main of cmd/pg-router-handler-ccpool (a
// second main package cannot import it); no behavior change.
package wire

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/phillipgreenii/pg-router-handlers/internal/ccpool"
	"github.com/phillipgreenii/pg-router-handlers/internal/eventlog"
	"github.com/phillipgreenii/pg-router-handlers/internal/executor"
	"github.com/phillipgreenii/pg-router-handlers/internal/failsig"
	"github.com/phillipgreenii/pg-router-handlers/internal/roles"
	"github.com/phillipgreenii/pg-router/schemas"
)

// DispatchRequest/DispatchEvent decode the handler.dispatch wire message
// (packages/pg-router/schemas/handler.dispatch.schema.json /
// .../event.schema.json).
type DispatchRequest struct {
	SchemaVersion string        `json:"schemaVersion"`
	ID            string        `json:"id"`
	Event         DispatchEvent `json:"event"`
}

// DispatchEvent is the event member of a DispatchRequest.
type DispatchEvent struct {
	ID      string         `json:"id"`
	Type    string         `json:"type"`
	Payload map[string]any `json:"payload"`
}

// LifecycleRequest mirrors
// packages/pg-router/schemas/handler.postStartup.schema.json and
// .../handler.preShutdown.schema.json (pg2-oju6w.15) — both hooks share one
// wire shape (no event, no deferred branch), so one type backs both.
type LifecycleRequest struct {
	SchemaVersion string `json:"schemaVersion"`
	ID            string `json:"id"`
}

// OutcomeReply builds the inline-outcome handler.dispatch-reply. startedAt is
// the launch time of a session this dispatch adopted (report.Result.StartedAt,
// DEC-EVENT-5, INV-CCH-30): the reply carries it as the optional startedAt
// (RFC3339, UTC) so the core times the dispatch from its real start and not
// from the re-offer. A zero startedAt (a fresh session, or an adopted one that
// recorded no launch time) omits the field entirely.
func OutcomeReply(id, outcome string, startedAt time.Time) map[string]any {
	reply := map[string]any{
		"schemaVersion": schemas.SchemaVersion,
		"id":            id,
		"outcome":       outcome,
	}
	if !startedAt.IsZero() {
		reply["startedAt"] = ccpool.FormatMetaTime(startedAt)
	}
	return reply
}

// WriteReply marshals v and writes it to w. An undeliverable write is logged,
// never fatal.
func WriteReply(w io.Writer, v any) {
	b, _ := json.Marshal(v)
	if _, err := w.Write(b); err != nil {
		// Not fatal: the reader (the daemon) may be gone already, e.g. a dispatch
		// that outlived a daemon restart (ADR 0085). The dispatch's work is done.
		slog.Warn("reply undeliverable; the process that read this handler's stdout is gone", "err", err)
	}
}

// WriteErrorReply writes the schema-legal error reply carrying msg.
func WriteErrorReply(w io.Writer, msg string) {
	WriteReply(w, map[string]any{"schemaVersion": schemas.SchemaVersion, "error": msg})
}

// BusyReason* are the short, stable classification tags WriteBusyReply
// carries for executor's admission-gate sentinels (bead pg2-j4uwg). Kept as
// constants, never a free-text diagnostic sentence, so a wireclient caller on
// the other side of the wire can classify on the exact string rather than
// pattern-match prose (the slog.Warn/Info calls inside
// internal/executor/ccpool.go's run() already carry the human-readable
// detail for the operator's own logs).
const (
	BusyReasonCapacityUnknown = "capacity-unknown"
	BusyReasonAtCapacity      = "at-capacity"
	// BusyReasonLowDisk (bead pg2-8vn8t): the isolation step's `git worktree
	// add` failed because the filesystem ran out of room (executor.ErrLowDisk)
	// — a transient, system-wide condition, not a per-bead defect, so it is
	// mapped to the SAME busy-decline treatment as the two capacity reasons
	// above rather than an escalation (see ccpool.go's own doc comment at its
	// isolation-Ensure error branch).
	BusyReasonLowDisk = "low-disk"
	// BusyReasonCommandBusy (bead pg2-358u3): a "command" role's argv exited 9
	// (conformance.ExitBusy's own code — the command's "not right now" signal),
	// which internal/executor/command.go turns into executor.ErrBusy. It gets
	// its OWN tag, not at-capacity's, because the cause is opaque to this
	// module (the command, not ccpool, decided it was busy) and the Grafana
	// failure-rate rule excludes at-capacity as routine backpressure — a
	// command that stays busy MUST remain visible, not hide inside that
	// exclusion.
	BusyReasonCommandBusy = "command-busy"
	// BusyReasonSessionInFlight (bead pg2-qaeb8, INV-CCH-30, ADR 0094): an
	// earlier attempt of the same bead and role is in flight and this dispatch
	// may not absorb it (executor.ErrSessionInFlight). Routine backpressure like
	// a full pool: the Grafana failure-rate rule excludes this reason, and the
	// decline stays visible per reason on the declined counter.
	BusyReasonSessionInFlight = "session-in-flight"
	// BusyReasonPoolUnreadable (bead pg2-s3pjt, INV-CCH-30): the pool's sessions
	// cannot be read (executor.ErrPoolUnreadable). The dispatch still declines
	// as if an attempt were in flight, but this is NOT routine backpressure, so
	// it gets its own reason: the failure-rate rule does not exclude it, and a
	// persistently unreadable pool pages (under session-in-flight it was hidden
	// by that exclusion).
	BusyReasonPoolUnreadable = "pool-unreadable"
	// BusyReasonLaunchFailed (INV-CCH-34): a failure before ccpool new returned
	// ready (executor.ErrLaunchFailed). A pre-accept decline of kind fault; it
	// mutates no bead, and the failure-rate alert counts it.
	BusyReasonLaunchFailed = "launch-failed"
)

// The decline kinds of the wire's decline body (schemas' handler.dispatch-reply
// schema, DEC-WIRE-1): what core's progress gauges count. busy is routine
// backpressure, fault is a condition worth an operator's look, and neutral is a
// decline that says nothing about the dispatch's health.
const (
	DeclineKindBusy    = "busy"
	DeclineKindFault   = "fault"
	DeclineKindNeutral = "neutral"
)

// BusyDecline is one pre-accept decline: the wire body's reason, kind and the
// optional, redacted, bounded detail. Every decline goes through one before
// WriteBusyReply.
type BusyDecline struct{ Reason, Kind, Detail string }

// NeutralDecline is a decline of kind neutral (origin-unavailable and skipped-*):
// the dispatch was not wanted or could not run for a reason that is neither
// backpressure nor a fault. detail is redacted and bounded; "" omits it.
func NeutralDecline(reason, detail string) BusyDecline {
	return BusyDecline{Reason: reason, Kind: DeclineKindNeutral, Detail: BoundedDetail(detail)}
}

// BoundedDetail redacts text and truncates it, by bytes at a UTF-8 boundary, to
// the schema's maxLength (schemas.DeclineDetailMaxLen), keeping the START of the
// text. The schema bound is in characters, so a byte bound also satisfies it.
func BoundedDetail(text string) string {
	text = failsig.Redact(strings.TrimSpace(text))
	if len(text) <= schemas.DeclineDetailMaxLen {
		return text
	}
	cut := schemas.DeclineDetailMaxLen
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut]
}

// BoundedTailDetail is BoundedDetail keeping the END of the text. A ccpool new
// error reads "ccpool new <long argv>: <exit> (<stderr>)", so the diagnosis is at
// the end, and a head cut would keep only the argv.
func BoundedTailDetail(text string) string {
	text = failsig.Redact(strings.TrimSpace(text))
	if len(text) <= schemas.DeclineDetailMaxLen {
		return text
	}
	cut := len(text) - schemas.DeclineDetailMaxLen
	for cut < len(text) && !utf8.RuneStart(text[cut]) {
		cut++
	}
	return text[cut:]
}

// BusyDeclineFor maps err to the wire's pre-accept decline (reason, kind and
// detail) when err is one of executor's "decline and retry later" sentinels
// (bead pg2-j4uwg's capacity pair, widened by pg2-8vn8t's low-disk sentinel,
// pg2-358u3's command-busy sentinel, pg2-qaeb8's session-in-flight sentinel,
// pg2-s3pjt's pool-unreadable sentinel and INV-CCH-34's launch-failed): ok is
// false for any other err (including nil), matching none of them. All still map
// to the SAME wire-level conformance.ExitBusy, so the core's retry/backoff
// cadence never depends on which reason applied (INV-FAIL-1); the reason and the
// kind exist so pg-router core's declined metric, status breakdown and progress
// gauges can tell them apart.
//
// The kind per reason:
//
//	busy:    at-capacity (usage-limit refusals map here, so a usage window never
//	         pages), session-in-flight, command-busy
//	fault:   capacity-unknown, pool-unreadable, low-disk, launch-failed
//	neutral: origin-unavailable and skipped-* (NeutralDecline)
//
// Only launch-failed carries a detail: the redacted ccpool new error text, its
// last bytes up to the schema bound (the stderr diagnosis is at the end).
func BusyDeclineFor(err error) (BusyDecline, bool) {
	switch {
	case errors.Is(err, executor.ErrPoolCapacityUnknown):
		return BusyDecline{Reason: BusyReasonCapacityUnknown, Kind: DeclineKindFault}, true
	case errors.Is(err, executor.ErrPoolAtCapacity):
		return BusyDecline{Reason: BusyReasonAtCapacity, Kind: DeclineKindBusy}, true
	case errors.Is(err, executor.ErrLowDisk):
		return BusyDecline{Reason: BusyReasonLowDisk, Kind: DeclineKindFault}, true
	case errors.Is(err, executor.ErrBusy):
		return BusyDecline{Reason: BusyReasonCommandBusy, Kind: DeclineKindBusy}, true
	case errors.Is(err, executor.ErrSessionInFlight):
		return BusyDecline{Reason: BusyReasonSessionInFlight, Kind: DeclineKindBusy}, true
	case errors.Is(err, executor.ErrPoolUnreadable):
		return BusyDecline{Reason: BusyReasonPoolUnreadable, Kind: DeclineKindFault}, true
	case errors.Is(err, executor.ErrLaunchFailed):
		d := BusyDecline{Reason: BusyReasonLaunchFailed, Kind: DeclineKindFault}
		var lf *executor.LaunchFailedError
		if errors.As(err, &lf) {
			d.Detail = BoundedTailDetail(lf.Tail)
		}
		return d, true
	default:
		return BusyDecline{}, false
	}
}

// WriteBusyReply writes the OPTIONAL reply body DEC-WIRE-1 / interfaces.md's
// "Coarse outcome, rich reply" allows a participant to accompany an exit-9
// busy decline with ("a reply body, where the participant supplies one,
// still carries which case applies") — reusing WriteReply, the SAME
// lowest-level body-writing helper WriteErrorReply and the success path
// both already use. The body is the decline branch of the handler.dispatch-reply
// schema: a required reason, the kind (busy | fault | neutral), and an OPTIONAL
// bounded, redacted detail, written only when non-empty. wireclient.Dispatch on
// the core side reads it OPPORTUNISTICALLY on exit 9 and tolerates an
// absent/empty body (DEC-WIRE-1: "no body required").
func WriteBusyReply(w io.Writer, d BusyDecline) {
	body := map[string]any{"schemaVersion": schemas.SchemaVersion, "reason": d.Reason, "kind": d.Kind}
	if d.Detail != "" {
		body["detail"] = d.Detail
	}
	WriteReply(w, body)
}

// DeclineAndReply writes the dispatch_decline event-log record of d and then the
// decline's reply body: the one place every decline passes through (INV-CCH-35).
func DeclineAndReply(w io.Writer, log *eventlog.Writer, role roles.Role, itemID string, d BusyDecline) {
	EmitDispatchDecline(log, role, itemID, d)
	WriteBusyReply(w, d)
}

// EmitDispatchDecline writes the dispatch_decline record: the role, the item's
// id, the reason, the kind (as decline_kind: the event log's own kind field names
// the record), the role's sessionEnv (non-secret by rule, so values
// are written as-is; empty for a command-backed role), the redacted evidence tail
// and the failure signature. Only a launch-failed decline has evidence (the
// redacted tail of the ccpool new error, bounded like the wire detail) and a
// signature (launch-failed); both are "" for every other reason. A decline of
// kind fault is logged at warn, the others at info. It never returns an error: a
// log that cannot be written must not change the decline.
func EmitDispatchDecline(log *eventlog.Writer, role roles.Role, itemID string, d BusyDecline) {
	if log == nil {
		return
	}
	sessionEnv := make(map[string]string, len(role.SessionEnv))
	for k, v := range role.SessionEnv {
		sessionEnv[k] = v
	}
	evidence, signature := "", ""
	if d.Reason == BusyReasonLaunchFailed {
		evidence, signature = d.Detail, string(failsig.LaunchFailed)
	}
	level := "info"
	if d.Kind == DeclineKindFault {
		level = "warn"
	}
	_ = log.Emit(level, executor.DispatchDeclineKind, "dispatch declined: "+d.Reason, map[string]any{
		"role": role.Name, "item_id": itemID, "reason": d.Reason, "decline_kind": d.Kind,
		"session_env": sessionEnv, "evidence": evidence, "failure_signature": signature,
	})
}
