package orchestrator

import (
	"context"
	"errors"
	"time"

	"github.com/phillipgreenii/pg-router/internal/backoff"
	"github.com/phillipgreenii/pg-router/internal/core"
	"github.com/phillipgreenii/pg-router/internal/discover"
	"github.com/phillipgreenii/pg-router/internal/eventqueue"
	"github.com/phillipgreenii/pg-router/internal/roles"
	"github.com/phillipgreenii/pg-router/internal/wireclient"
)

// ResourceLimitObserver is notified when one Offer's dispatch ends because
// the role hit its OWN resource ceiling — the glossary's "resource-limit"
// outcome (`packages/pg-router/docs/behavior/glossary.md`'s "Outcomes"
// section): "a capacity or quota ceiling was reached... not a defect, and
// the handler will be able again once the ceiling lifts." Before docket
// pg2-oju6w's Task 5.4 the only producer was the budget watchdog's hard
// stop (watchdog.ErrBudgetExceeded, internal/watchdog) racing a ccpool
// role's wait in-process (executor's workerWaitWithWatchdog); Task 5.2/5.3
// moved that entire mechanism out to the registered handler participant
// (packages/pg-router-ccpool-handler), so this hook currently has NO
// producer left to fire it at all — the wire protocol has no equivalent
// signal yet (docs/decisions/wire.md names no resource-limit exit code or
// reply field). This is not a NEW gap Task 5.4 introduces: it widens the
// SAME already-recorded realization gap the note below describes, one
// level further than "surfaced only on the handler's own surface" — for
// now, not surfaced at all. Offer would observe it INLINE, before it
// returns its (accepted) OfferResult — the same synchronous dispatch-reply
// path Offer already reports every other outcome through (its own doc: "no
// deferred/async form on this bridge"), never the async session-status
// callback internal/core dropped 2026-07-28 (that package's own doc) — so
// this hook does not reopen that decision, nor the internal/metrics
// post-accept scope-cut (metrics.go's package doc): it is a separate,
// ring-only signal cmd/pg-router/run.go's activityObserver wires into
// internal/activity.Ring, not a metrics counter.
//
// (Realization note, `packages/pg-router/docs/behavior/README.md`'s
// "Realization gaps" register: the generic contract's INV-FAIL-1 states a
// post-accept outcome is surfaced only "on the handler's own surface,"
// never back to the core. roleListener IS the INTF-HANDLER boundary from
// the queue's own point of view, so this hook is a recorded gap against
// that invariant — a similar-shaped core/handler-boundary gap to the one
// INV-WORKFLOW-1's work-outcome row used to carry against buildResult,
// before docket pg2-oju6w's Task 5.5 closed it (bead pg2-ctqo2); not a new
// exception invented here.)
type ResourceLimitObserver interface {
	// OnResourceLimit fires once per dispatch whose handler-side outcome
	// was a resource-limit hit. eventID is the accepted event's own ID —
	// the SAME id eventqueue.Observer.OnAccept receives moments later for
	// this identical accept (both derive from this one synchronous Offer
	// call) — so a consumer can correlate this signal with, and override,
	// the eventqueue's own default "delivered" recording for that accept.
	// evtType is the event's Type, matching every other per-event Observer
	// hook in this codebase (e.g. eventqueue.Observer.OnDispatchFailure).
	OnResourceLimit(eventID, evtType string)
}

// HandlerFailureObserver is notified when Offer's synchronous dispatch to
// the registered handler participant returns a genuine, non-panic error the
// handler itself reported — bead pg2-97539's gap. Per ADR 0056's "A
// queue->executor Listener bridge" (this file's own package doc pointer),
// Offer runs wireclient.Dispatch synchronously and "always reports
// acceptance": a handler-internal error (e.g. wireclient: role "review"
// exited 1: <bead>: session exited before completing) is neither the
// pre-accept eventqueue.Observer.OnDeclined case (a graceful busy/
// unavailable decline, checked BEFORE dispatch even runs) nor the
// eventqueue.Observer.OnDispatchFailure case (fed ONLY from a panic
// recovered by the queue's own offerSafely, never from this Offer's own
// error return) — so before this hook existed, that error was logged by
// emitResult and otherwise dropped: pg_router_failures_total never
// incremented for it. errBusy (wireclient.ErrBusy, mapped to
// eventqueue.DeclineBusy) is deliberately EXCLUDED from this hook — that
// case already lands on FailureClassDeclined via the eventqueue.Observer
// path, so routing it here too would double-count it under a second class.
// eventID/evtType match ResourceLimitObserver's own signature and
// capture-at-construction pattern (the same roleListener field group,
// nil-safe: no configured observer is a no-op, matching every other hook in
// this group).
type HandlerFailureObserver interface {
	OnHandlerFailure(eventID, evtType string)
}

// roleListener implements eventqueue.BackoffListener (INV-FAIL-2, Task 1.3):
// a compile-time check that a future signature drift on either interface
// fails the build here rather than silently degrading a role back to the
// queue's own WithRetryBackoff default.
var _ eventqueue.BackoffListener = (*roleListener)(nil)

// roleListener bridges the durable event queue to wireclient.Dispatch, which
// sends handler.dispatch to whichever handler participant is registered for
// role (INTF-HANDLER, core side; Task 5.4 replaces the retired in-process
// executor.For(role).Dispatch call here) — the queue->handler Listener
// bridge (bead
// pg2-f3mcb.2, queue-as-universal-intermediary). ID/Matches close over the
// configured role; Offer runs the dispatch INLINE and reports an INLINE
// completion (interfaces.md "Reply (sync)"): the offer call itself IS the
// handler session, so by the time Offer returns an ACCEPTED OfferResult, the
// event has been fully worked, never merely accepted.
//
// *** It deliberately tracks NO count of its own — no in-flight tally, no busy
// threshold. *** INV-CONC-1 forbids the core (and this adapter is core-side)
// from holding any such number: capacity is the handler's own business. The
// ONLY concurrency control here is structural and comes free from
// eventqueue.Queue.Dispatch itself — one outstanding offer per registered
// Listener per pass (the per-handler serial FIFO, ADR 0031 / DEC-EVENT-2) — so
// this type MUST NOT grow an inflight/busy field. Today (Task 2.2) it always
// reports OfferResult{Accepted: true, Decline: eventqueue.DeclineNone}: a
// synchronous handler that can always take custody has, as yet, no signal to
// decline on. Task 2.3 wires a genuine DeclineBusy through this SAME method,
// but from an OBSERVED command-exit signal (executor.ErrBusy), never from a
// core-tracked capacity number — the boundary this bead's convergence
// established (the former `n := r.Cap - bus.Inflight(r.Name)`) stays intact.
type roleListener struct {
	o    *Orchestrator
	role roles.Role
	// ctx is the run's own long-lived context (from `run` / `run-until-idle`),
	// carried here because eventqueue.Listener.Offer takes no context of its
	// own — the queue's Dispatch loop offers synchronously and does not thread
	// one through. Using the run's ctx (rather than context.Background()) keeps
	// a dispatch responsive to the run's own cancellation (e.g. SIGINT), since
	// the executor and its watchdog/budget polling already select on it.
	ctx context.Context
	// poolDefault is the pool-wide handler retry cadence (cfg.RetryBackoff,
	// INV-FAIL-2) this listener falls back to when its own role carries the
	// zero backoff.Policy — Task 1.3. Config decode already merges
	// [role.retry] onto the pool default for a CONFIG-decoded role (so
	// role.RetryBackoff is never zero there even absent an override), which
	// means in practice this fallback matters only for a BUILT-IN role
	// (roles.BuiltinRoleSet never sets RetryBackoff at all). Captured once at
	// construction (NewListener) rather than read from o.Cfg live, so a
	// listener's cadence stays stable for its whole life even if o.Cfg were
	// ever mutated after boot.
	poolDefault backoff.Policy
	// reg is the core.Registry Offer consults for this role's self-status /
	// lifecycle availability (Task 2.3, pg2-84o3m.22 Step 2.3.3) — captured
	// once at construction from o.Registry, the same capture-at-construction
	// pattern poolDefault uses. nil (o.Registry unset, every pre-Task-2.3
	// test) disables the check: Offer never consults it and behaves exactly
	// as before this field existed.
	reg *core.Registry
	// resourceLimitObs is notified of a resource-limit hit (this bead,
	// pg2-fm2gw) — captured once at construction from o.ResourceLimitObserver,
	// the same capture-at-construction pattern reg/poolDefault use. nil (the
	// default, and every pre-this-bead test) disables the notification
	// entirely: Offer still runs the identical errors.Is check but skips the
	// call, matching this package's behavior before this field existed.
	resourceLimitObs ResourceLimitObserver
	// handlerFailureObs is notified of a genuine, non-panic handler error
	// (this bead, pg2-97539) — captured once at construction from
	// o.HandlerFailureObserver, the same capture-at-construction pattern
	// reg/resourceLimitObs use. nil (the default, and every pre-this-bead
	// test) disables the notification entirely: Offer still runs the
	// identical error check but skips the call.
	handlerFailureObs HandlerFailureObserver
}

// NewListener returns the eventqueue.Listener for role, run under ctx — the
// queue->executor bridge a real `run` / `run-until-idle` command path
// registers on the queue (not just a test double). ctx is retained for the
// life of the listener; cancel it to make every future Offer observe the
// cancellation through the executor's own ctx-aware waits. It is injected
// with o.Cfg.RetryBackoff (the pool-wide default, RetryBackoff's fallback —
// Task 1.3) at construction, the same way bootCore threads cfg.RetryBackoff
// into eventqueue.WithRetryBackoff. o.Registry (Task 2.3) is captured the
// same way: a nil o.Registry disables the availability check entirely.
// o.ResourceLimitObserver (this bead, pg2-fm2gw) is captured identically.
// o.HandlerFailureObserver (bead pg2-97539) is captured identically.
func (o *Orchestrator) NewListener(ctx context.Context, role roles.Role) eventqueue.Listener {
	return &roleListener{
		o: o, role: role, ctx: ctx, poolDefault: o.Cfg.RetryBackoff,
		reg: o.Registry, resourceLimitObs: o.ResourceLimitObserver,
		handlerFailureObs: o.HandlerFailureObserver,
	}
}

func (l *roleListener) ID() string { return l.role.Name }

// RetryBackoff implements eventqueue.BackoffListener (INV-FAIL-2, Task 1.3):
// a role carrying its OWN non-zero backoff.Policy (a [role.retry] override,
// already merged onto the pool default at config decode) uses that; a role
// carrying the zero Policy — every BUILT-IN role, which decodes no retry
// table at all — uses the pool-wide default (poolDefault) instead of falling
// through to backoff.Default() via Policy.Duration's own sanitized(). Without
// this, a built-in role under a customized [pool.retry] would silently keep
// the package's hardcoded default cadence rather than the operator's own.
func (l *roleListener) RetryBackoff() backoff.Policy {
	if l.role.RetryBackoff != (backoff.Policy{}) {
		return l.role.RetryBackoff
	}
	return l.poolDefault
}

// BackoffState exposes this listener's live backoff streak/next-eligible
// state as a queryable (streak int, nextEligible time.Time, ok bool) —
// Task 4.1's schema-generality requirement for listeners[].backoff (spec
// §5): the plumbing must exist even though ok is always false in the
// shipped production listener today. RetryBackoff above names only the
// retry POLICY (the schedule to use IF a streak were ever accruing); it is
// eventqueue.Queue's own Dispatch loop — not this type — that would own
// any actual running streak, since Queue is what schedules a listener's
// re-offer after a decline (see queue.go's lock-order/backoff doc). This
// roleListener never tracks such a streak itself, so ok is unconditionally
// false: there is nothing live to report, corr-6's stated conclusion for
// the shipped production listener, which today either accepts outright or
// declines instantaneously (busy/unavailable) rather than ever entering an
// observable backoff wait of its own.
func (l *roleListener) BackoffState() (streak int, nextEligible time.Time, ok bool) {
	return 0, time.Time{}, false
}

// Matches implements the dispatch flowchart's binding check (INV-DISP-1): the
// event's type MUST match one of the role's declared Binds.
//
// A binding's optional payload-path narrowing predicate (interfaces.md,
// INV-DISP-1's "a binding MAY then carry one narrowing predicate over a
// payload path") is NOT modeled here: no config surface names one yet
// (OQ-CONFIG, the full config schema, and OQ-EVT-CATALOG, a declared per-type
// payload shape, are both still open), so every binding this package builds is
// a bare type match. When a payload path IS declared, the rule to add here is
// exactly INV-DISP-1's: a named path ABSENT on the event is a NON-MATCH, never
// an error.
func (l *roleListener) Matches(evt eventqueue.Event) bool {
	for _, b := range l.role.Binds {
		if b == evt.Type {
			return true
		}
	}
	return false
}

// Offer dispatches the event to this role's registered handler participant
// over the wire (internal/wireclient.Dispatch, Task 5.4 — before it, this
// ran the OLD in-process executor, ensure -> send -> wait for a ccpool
// role, or run-to-completion for a command role). The call is still
// synchronous end-to-end, so an ACCEPTED OfferResult and "worked to
// completion" coincide here — there is no deferred/async form on this
// bridge. The dispatch's own report.Result — the handler's opaque
// reply.Outcome, stored verbatim rather than re-derived from bead status
// (Task 5.5) — is logged/emitted exactly as it was under the retired
// drain()/DrainOnce path, via the SAME Orchestrator helpers. Task 2.2 widened the signature to
// Offering/OfferResult (a dispatch tracking id in, an Accepted/DeclineReason
// pair out); before Task 2.3 this method always accepted.
//
// Task 2.3 (pg2-84o3m.22) adds the two genuine pre-accept decline paths, both
// checked here in Dispatch's UNLOCKED phase 2 — never in Matches/ID, which
// run under q.mu via headFor (perf-F11's lock-order pin):
//
//  1. Unavailable self-status / lifecycle: l.reg (nil unless o.Registry is
//     set) is consulted FIRST, before any actual dispatch work, so a
//     currently-unavailable participant costs nothing beyond the registry
//     lookup.
//  2. A busy handler exit: workOne's error, when it resolves to
//     wireclient.ErrBusy through the existing %w chain (errors.Is) — DEC-
//     WIRE-1's coarse exit code 9 — means the dispatch attempt itself
//     signaled "not right now" rather than completing — so it is reported
//     as a decline, not run through the normal buildResult/emitResult
//     completed-dispatch accounting (nothing meaningful happened to the
//     bead for the caller to record). Before Task 5.4 this was the retired
//     in-process executor.ErrBusy.
//
// This bead (pg2-fm2gw) added a THIRD, post-accept signal, checked after the
// two pre-accept declines above: a budget hard-stop was a genuine ACCEPT,
// never a decline. As of Task 5.4 that signal (watchdog.ErrBudgetExceeded)
// has no wire-level successor — ResourceLimitObserver's own doc comment
// above records why — so l.resourceLimitObs is never notified today; the
// field and the doc below describe the pre-Task-5.4 behavior this hook is
// meant to widen back to once the wire protocol carries an equivalent
// signal.
//
// bead pg2-97539 added a FOURTH signal, checked after the busy decline: any
// OTHER non-nil err (not wireclient.ErrBusy) is a genuine, non-panic
// handler-internal error — the offer is still an ACCEPT (this bridge's
// "always reports acceptance," unchanged), but l.handlerFailureObs (nil-safe,
// same idiom as l.resourceLimitObs) is notified so
// pg_router_failures_total can record it under its own class rather than
// falling through both existing failure classes uncounted, as it did before
// this bead (see HandlerFailureObserver's own doc comment above).
func (l *roleListener) Offer(o eventqueue.Offering) eventqueue.OfferResult {
	if l.reg != nil && !l.reg.Available(l.role.Name) {
		return eventqueue.OfferResult{Accepted: false, Decline: eventqueue.DeclineUnavailable}
	}
	evt := o.Event
	d := discover.DeriveContextFromQueueEvent(l.role, evt)
	reply, err := l.o.workOne(l.ctx, d, evt)
	if errors.Is(err, wireclient.ErrBusy) {
		// Decline stays eventqueue.DeclineBusy regardless of detail (INV-
		// FAIL-1: every DeclineReason re-offers alike) — DeclineDetail is a
		// SECOND, additive, OPAQUE field (bead pg2-j4uwg): whatever reason
		// tag the participant's OPTIONAL exit-9 reply body carried
		// (wireclient.BusyDecline.Reason — "" when it supplied none),
		// forwarded verbatim. This package interprets nothing about what
		// the tag means (GOAL-MIN-1: the core stays agnostic of any one
		// handler's own busy-decline vocabulary); it only relays a string a
		// downstream consumer (metrics/status) may show as-is.
		var detail string
		var bd *wireclient.BusyDecline
		if errors.As(err, &bd) {
			detail = bd.Reason
		}
		return eventqueue.OfferResult{Accepted: false, Decline: eventqueue.DeclineBusy, DeclineDetail: detail}
	}
	// A resource-limit hit (l.resourceLimitObs) has no wire-level signal to
	// detect it from anymore — see ResourceLimitObserver's own doc comment
	// above for why this is a widened, already-recorded gap rather than a
	// regression this task introduces.
	if err != nil && l.handlerFailureObs != nil {
		l.handlerFailureObs.OnHandlerFailure(evt.ID, evt.Type)
	}
	l.o.emitResult(l.ctx, l.role, d.Item.ID, l.o.buildResult(d, reply, err), err)
	return eventqueue.OfferResult{Accepted: true, Decline: eventqueue.DeclineNone}
}
