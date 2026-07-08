package nudger

import (
	"context"
	"time"
)

// Nudger is the top-level façade. Owns the pending store and the three
// producers; runs Tick on every daemon tick.
type Nudger struct {
	store          *PendingStore
	dispatcher     *Dispatcher
	windowProd     *WindowResetProducer
	limitPauseProd *LimitPauseProducer
	disruptProd    *DisruptProducer
	manualProd     *ManualProducer
}

// New constructs a Nudger ready for Tick. historyErrLog, when non-nil, receives
// a one-line message whenever a nudge_history write fails, so a failed capture
// is surfaced to a durable local sink instead of being silently discarded (see
// Dispatcher.HistoryErrLog). Pass nil to disable (early-startup / tests).
//
// New takes a Deliverer directly: the caller (lifecycle.go) is responsible for
// composing the right Deliverer — a compositeDeliverer that routes cmux-hosted
// targets through a cmux-bridge stream and everything else through the
// existing in-daemon signal layer. See signalerDeliverer below for adapting a
// plain Signaler where a Deliverer is required (e.g. in tests).
func New(deliverer Deliverer, recorder Recorder, nudgeRecorder NudgeRecorder, historyErrLog func(msg string)) *Nudger {
	return &Nudger{
		store:          NewPendingStore(),
		dispatcher:     &Dispatcher{Deliverer: deliverer, Recorder: recorder, NudgeRecorder: nudgeRecorder, HistoryErrLog: historyErrLog},
		windowProd:     &WindowResetProducer{},
		limitPauseProd: &LimitPauseProducer{},
		disruptProd:    NewDisruptProducer(),
		manualProd:     &ManualProducer{},
	}
}

// signalerDeliverer adapts a synchronous Signaler to the Deliverer interface.
// New itself no longer uses this shim (it takes a Deliverer directly), but
// tests in this package construct a Dispatcher/Nudger from a plain
// Signaler-shaped fake and need a Deliverer to hand it — signalerDeliverer is
// kept for that purpose.
type signalerDeliverer struct{ sig Signaler }

// Deliver satisfies Deliverer by delegating to the wrapped Signaler.Send. It
// never returns ErrNoBridge — the synchronous signal layer has no bridge
// concept — so the no-bridge drop-after-window path in Dispatch is inert
// until a real async Deliverer is wired in.
func (s signalerDeliverer) Deliver(_ context.Context, pid int, text string) error {
	return s.sig.Send(pid, text)
}

// snapshotKeySet captures the current set of intent keys in the store.
func snapshotKeySet(s *PendingStore) map[IntentKey]struct{} {
	intents := s.List()
	out := make(map[IntentKey]struct{}, len(intents))
	for _, in := range intents {
		out[in.Key] = struct{}{}
	}
	return out
}

// Reconcile runs all producers (window_reset, disrupted, manual) but does
// NOT dispatch. Use this to inspect the pending store between reconcile
// and fire.
func (n *Nudger) Reconcile(ctx TickContext) {
	pre := snapshotKeySet(n.store)
	n.windowProd.Reconcile(ctx, n.store)
	n.disruptProd.Reconcile(ctx, n.store)
	n.limitPauseProd.Reconcile(ctx, n.store)
	// Manual is RPC-driven; Reconcile is a no-op but called for symmetry.
	n.manualProd.Reconcile(ctx, n.store)
	// Emit queued_total counter for each newly-added intent.
	post := snapshotKeySet(n.store)
	for k := range post {
		if _, was := pre[k]; !was {
			n.dispatcher.Recorder.RecordQueued(k.SessionID, k.Source)
		}
	}
}

// Dispatch runs the dispatcher against the current pending store.
func (n *Nudger) Dispatch(goCtx context.Context, ctx TickContext) {
	n.dispatcher.Dispatch(goCtx, ctx, n.store)
}

// Tick is a convenience: Reconcile then Dispatch. Existing tests use Tick.
func (n *Nudger) Tick(goCtx context.Context, ctx TickContext) {
	n.Reconcile(ctx)
	n.Dispatch(goCtx, ctx)
}

// QueueManual enqueues manual nudges for the given session IDs.
func (n *Nudger) QueueManual(sids []string, text string, now time.Time) {
	n.manualProd.Queue(n.store, sids, text, now)
}

// CancelManual cancels pending manual nudges for the given session IDs.
func (n *Nudger) CancelManual(sids []string) {
	n.manualProd.Cancel(n.store, sids)
}

// PendingFor reports whether any intent is queued for sid (any source).
func (n *Nudger) PendingFor(sid string) bool {
	return n.store.HasAny(sid)
}

// PendingForSource reports whether an intent of the given source exists for sid.
func (n *Nudger) PendingForSource(sid string, src Source) bool {
	for _, s := range n.store.SourcesFor(sid) {
		if s == src {
			return true
		}
	}
	return false
}

// SourcesFor returns the queued sources for sid.
func (n *Nudger) SourcesFor(sid string) []Source {
	return n.store.SourcesFor(sid)
}

// PendingSourcesForSession returns the pending nudge sources for the given
// session, or nil if none. Used by sharedState.snapshot to annotate the
// DB-materialised tree with live pending-nudge state.
func (n *Nudger) PendingSourcesForSession(sid string) []Source {
	return n.store.SourcesFor(sid)
}

// SnapshotStore returns a copy of all pending intents (for persistence).
func (n *Nudger) SnapshotStore() []NudgeIntent {
	return n.store.List()
}

// LoadStore replaces the pending store with the given intents (for
// persistence restore on startup).
func (n *Nudger) LoadStore(intents []NudgeIntent) {
	n.store = NewPendingStore()
	for _, in := range intents {
		n.store.Add(in)
	}
}
