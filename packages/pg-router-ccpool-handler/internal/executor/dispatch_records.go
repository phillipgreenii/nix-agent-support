package executor

import (
	"context"
	"errors"
	"sort"
	"unicode/utf8"

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/eventlog"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/failsig"
)

// The event-log kinds of the dispatch records (INV-CCH-35). dispatch_start is
// written per ACCEPTED dispatch (the executor's own, below), dispatch_decline per
// pre-accept decline (the cmd layer's, which owns the busyDecline value).
// Every accepted dispatch then gets exactly one OUTCOME record after its
// dispatch_start: dispatch_result when it FAILED (INV-CCH-9, the kind that
// always carried failure_signature) or dispatch_end for every other end,
// success included. A command role's busy exit 9 is a decline: its outcome is
// the cmd layer's dispatch_decline, so the executor writes none.
const (
	// DispatchStartKind is the eventlog kind of the per-accepted-dispatch record.
	DispatchStartKind = "dispatch_start"
	// DispatchDeclineKind is the eventlog kind of the per-decline record.
	DispatchDeclineKind = "dispatch_decline"
	// DispatchEndKind is the eventlog kind of the outcome record of an accepted
	// dispatch that did not fail: it carries an "outcome" field (Outcome*). A
	// failed dispatch writes dispatch_result instead (never both).
	DispatchEndKind = "dispatch_end"
)

// The "outcome" values of a dispatch_end record.
const (
	// OutcomeCompleted: the payload exited 0, or the session completed its work.
	OutcomeCompleted = "completed"
	// OutcomeUnclaimedEnd: the session ended without claiming its bead (INV-CCH-28).
	OutcomeUnclaimedEnd = "unclaimed-end"
	// OutcomePeerHeld: another actor holds the bead; nothing was written to it.
	OutcomePeerHeld = "peer-held"
	// OutcomeNeedsInputStalled: the needs_input stall bound closed the session (INV-CCH-29).
	OutcomeNeedsInputStalled = "needs-input-stalled"
	// OutcomeInterrupted: the dispatch's context ended (shutdown or a cancelled
	// payload) before an outcome was known; not a session failure.
	OutcomeInterrupted = "interrupted"
)

// dispatchEndOutcome names the dispatch_end outcome of an accepted dispatch's
// end cause (nil for success), and ok=false when the cause is a failure, which
// is written as dispatch_result instead.
func dispatchEndOutcome(cause error) (outcome string, ok bool) {
	switch {
	case cause == nil:
		return OutcomeCompleted, true
	case errors.Is(cause, ErrPeerHeld):
		return OutcomePeerHeld, true
	case errors.Is(cause, ErrUnclaimedEnd):
		return OutcomeUnclaimedEnd, true
	case errors.Is(cause, ErrNeedsInputStalled):
		return OutcomeNeedsInputStalled, true
	case errors.Is(cause, context.Canceled), errors.Is(cause, context.DeadlineExceeded):
		return OutcomeInterrupted, true
	}
	return "", false
}

// emitDispatchEnd writes the dispatch_end outcome record. session is the ccpool
// session's external id, "" for a command role (the field is then omitted).
func emitDispatchEnd(log *eventlog.Writer, d DispatchContext, session, outcome string) {
	if log == nil {
		return
	}
	fields := map[string]any{
		"role": d.Role.Name, "item_id": d.Item.ID, "item_type": d.Item.Type, "outcome": outcome,
	}
	if session != "" {
		fields["session"] = session
	}
	_ = log.Emit("info", DispatchEndKind, "dispatch ended: "+outcome, fields)
}

// emitDispatchFailed writes the dispatch_result failure record of INV-CCH-9 for
// a failure that has no session transcript to read (a command payload's
// failure, a failed send): the same fields as the ccpool session's record, with
// pool and session empty where the dispatch has none.
func emitDispatchFailed(log *eventlog.Writer, d DispatchContext, pool, session, text string) {
	res := classifyText(text)
	if log == nil {
		return
	}
	_ = log.Emit("error", dispatchResultKind, "dispatch failed", map[string]any{
		"role": d.Role.Name, "pool": pool, "bead": d.Item.ID, "session": session,
		"failure_signature": string(res.Signature), "signature_evidence": res.Evidence,
	})
}

// classifyText classifies a failure that has no session transcript with the
// shared classifier of INV-CCH-9. The evidence is the redacted, bounded matched
// line, and for text no rule matches (unknown) the redacted, bounded text
// itself, so it is never empty.
func classifyText(text string) failsig.Result {
	res := failsig.Classify(text)
	if res.Evidence != "" {
		return res
	}
	ev := failsig.Redact(text)
	if len(ev) > failsig.MaxEvidenceLen {
		ev = ev[:failsig.MaxEvidenceLen]
		for len(ev) > 0 && !utf8.ValidString(ev) {
			ev = ev[:len(ev)-1]
		}
	}
	if ev == "" {
		ev = "no error text"
	}
	res.Evidence = ev
	return res
}

// The two launch routes a ccpool-backed dispatch_start records. ccpool new does
// not report ccpool's internal route to its caller, so the record names the
// route the HANDLER took.
const (
	// RouteLaunched: this dispatch launched a fresh session through ccpool new
	// and it returned ready.
	RouteLaunched = "launched"
	// RouteAdopted: this dispatch adopted an already-live session of the same
	// event's turn instead of launching one (INV-CCH-30).
	RouteAdopted = "adopted"
)

// sessionEnvField returns env as the record's session_env value: never nil, so
// the field is present (and an empty object) for a role with no sessionEnv.
// sessionEnv carries no secret by INV-CCH-33, so values are written as-is.
func sessionEnvField(env map[string]string) map[string]string {
	out := make(map[string]string, len(env))
	for k, v := range env {
		out[k] = v
	}
	return out
}

// emitCommandStart writes the dispatch_start record of an accepted command-role
// dispatch, BEFORE its payload starts (so a payload that hangs or is killed
// still has one): the rendered argv, whether the strict switch is on and the base-env
// variable NAMES (nil when the switch is off). It never writes an env value.
func (r *commandRun) emitCommandStart(d DispatchContext, argv, baseNames []string) {
	if r.deps.Log == nil {
		return
	}
	names := append([]string{}, baseNames...)
	sort.Strings(names)
	_ = r.deps.Log.Emit("info", DispatchStartKind, "dispatch accepted: command payload starting",
		map[string]any{
			"role": d.Role.Name, "item_id": d.Item.ID, "item_type": d.Item.Type,
			"argv": argv, "strict_base_env": d.Role.StrictBaseEnv, "base_env_names": names,
		})
}

// emitCCPoolStart writes the dispatch_start record of an accepted ccpool-role
// dispatch: the route the handler took (RouteLaunched or RouteAdopted), the
// session's external id and the role's sessionEnv entries, values included.
// The claude env itself is the prefix tool's: an operator follows
// session_env.PG_WRAP_SPEC to pg-wrap show.
func (r *ccpoolRun) emitCCPoolStart(d DispatchContext, route, session string) {
	// The dispatch is accepted from here: Dispatch owes it exactly one outcome
	// record (recordOutcome), whether or not a log is configured.
	r.started, r.startSession = true, session
	if r.deps.Log == nil {
		return
	}
	_ = r.deps.Log.Emit("info", DispatchStartKind, "dispatch accepted: ccpool session ready",
		map[string]any{
			"role": d.Role.Name, "item_id": d.Item.ID, "item_type": d.Item.Type,
			"route": route, "session": session, "session_env": sessionEnvField(d.Role.SessionEnv),
		})
}
