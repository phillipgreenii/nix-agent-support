package orchestrator

import (
	"context"

	"github.com/phillipgreenii/pr-pool/internal/discover"
	"github.com/phillipgreenii/pr-pool/internal/eventqueue"
	"github.com/phillipgreenii/pr-pool/internal/roles"
)

// roleListener bridges the durable event queue to executor.For(role).Dispatch
// (INTF-HANDLER, core side) — the queue->executor Listener bridge (bead
// pg2-f3mcb.2, queue-as-universal-intermediary). ID/Matches close over the
// configured role; Offer runs the dispatch INLINE and reports an INLINE
// completion (interfaces.md "Reply (sync)"): the offer call itself IS the
// handler session, so by the time Offer returns, the event has been fully
// worked, never merely accepted.
//
// *** It deliberately tracks NO count of its own — no in-flight tally, no busy
// threshold. *** INV-CONC-1 forbids the core (and this adapter is core-side)
// from holding any such number: capacity is the handler's own business. The
// ONLY concurrency control here is structural and comes free from
// eventqueue.Queue.Dispatch itself — one outstanding offer per registered
// Listener per pass (the per-handler serial FIFO, ADR 0031 / DEC-EVENT-2) — so
// this type MUST NOT grow an inflight/busy field. It always accepts: a
// synchronous handler that can always take custody has no legitimate reason to
// pre-accept decline, and declining here would only reintroduce a core-tracked
// capacity signal by another name — exactly what this bead's convergence
// removes (the former `n := r.Cap - bus.Inflight(r.Name)`).
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
}

// NewListener returns the eventqueue.Listener for role, run under ctx — the
// queue->executor bridge a real `run` / `run-until-idle` command path
// registers on the queue (not just a test double). ctx is retained for the
// life of the listener; cancel it to make every future Offer observe the
// cancellation through the executor's own ctx-aware waits.
func (o *Orchestrator) NewListener(ctx context.Context, role roles.Role) eventqueue.Listener {
	return &roleListener{o: o, role: role, ctx: ctx}
}

func (l *roleListener) ID() string { return l.role.Name }

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

// Offer dispatches the event through this role's executor and ALWAYS reports
// acceptance: the call is synchronous end-to-end (ensure -> send -> wait for a
// ccpool role, or run-to-completion for a command role), so "accepted" and
// "worked to completion" coincide here — there is no deferred/async form on
// this bridge. The dispatch's own report.Result (created/closed/handed-back)
// is logged/emitted exactly as it was under the retired drain()/DrainOnce
// path, via the SAME Orchestrator helpers.
func (l *roleListener) Offer(evt eventqueue.Event) bool {
	d := discover.DeriveContextFromQueueEvent(l.role, evt)
	pre, preOK := l.o.snapshotIDs(l.ctx)
	res, err := l.o.workOne(l.ctx, d)
	l.o.emitResult(l.ctx, l.role, d.Item.ID, l.o.buildResult(l.ctx, l.role, d, pre, preOK, res, err))
	return true
}
