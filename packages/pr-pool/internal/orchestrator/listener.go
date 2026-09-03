package orchestrator

import (
	"context"
	"errors"
	"time"

	"github.com/phillipgreenii/pr-pool/internal/backoff"
	"github.com/phillipgreenii/pr-pool/internal/core"
	"github.com/phillipgreenii/pr-pool/internal/discover"
	"github.com/phillipgreenii/pr-pool/internal/eventqueue"
	"github.com/phillipgreenii/pr-pool/internal/executor"
	"github.com/phillipgreenii/pr-pool/internal/roles"
)

// roleListener implements eventqueue.BackoffListener (INV-FAIL-2, Task 1.3):
// a compile-time check that a future signature drift on either interface
// fails the build here rather than silently degrading a role back to the
// queue's own WithRetryBackoff default.
var _ eventqueue.BackoffListener = (*roleListener)(nil)

// roleListener bridges the durable event queue to executor.For(role).Dispatch
// (INTF-HANDLER, core side) — the queue->executor Listener bridge (bead
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
func (o *Orchestrator) NewListener(ctx context.Context, role roles.Role) eventqueue.Listener {
	return &roleListener{o: o, role: role, ctx: ctx, poolDefault: o.Cfg.RetryBackoff, reg: o.Registry}
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

// Offer dispatches the event through this role's executor. The call is
// synchronous end-to-end (ensure -> send -> wait for a ccpool role, or
// run-to-completion for a command role), so an ACCEPTED OfferResult and
// "worked to completion" coincide here — there is no deferred/async form on
// this bridge. The dispatch's own report.Result (created/closed/handed-back)
// is logged/emitted exactly as it was under the retired drain()/DrainOnce
// path, via the SAME Orchestrator helpers. Task 2.2 widened the signature to
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
//  2. A busy command exit: workOne's error, when it resolves to
//     executor.ErrBusy through the existing %w chain (errors.Is), means the
//     dispatch attempt itself signaled "not right now" rather than
//     completing — so it is reported as a decline, not run through the
//     normal buildResult/emitResult completed-dispatch accounting (nothing
//     meaningful happened to the bead for the caller to record).
func (l *roleListener) Offer(o eventqueue.Offering) eventqueue.OfferResult {
	if l.reg != nil && !l.reg.Available(l.role.Name) {
		return eventqueue.OfferResult{Accepted: false, Decline: eventqueue.DeclineUnavailable}
	}
	evt := o.Event
	d := discover.DeriveContextFromQueueEvent(l.role, evt)
	pre, preOK := l.o.snapshotIDs(l.ctx)
	res, err := l.o.workOne(l.ctx, d)
	if errors.Is(err, executor.ErrBusy) {
		return eventqueue.OfferResult{Accepted: false, Decline: eventqueue.DeclineBusy}
	}
	l.o.emitResult(l.ctx, l.role, d.Item.ID, l.o.buildResult(l.ctx, l.role, d, pre, preOK, res, err))
	return eventqueue.OfferResult{Accepted: true, Decline: eventqueue.DeclineNone}
}
