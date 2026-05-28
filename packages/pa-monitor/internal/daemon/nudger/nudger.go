package nudger

import "time"

// Nudger is the top-level façade. Owns the pending store and the three
// producers; runs Tick on every daemon tick.
type Nudger struct {
	store       *PendingStore
	dispatcher  *Dispatcher
	windowProd  *WindowResetProducer
	disruptProd *DisruptProducer
	manualProd  *ManualProducer
}

// New constructs a Nudger ready for Tick.
func New(signaler Signaler, recorder Recorder) *Nudger {
	return &Nudger{
		store:       NewPendingStore(),
		dispatcher:  &Dispatcher{Signaler: signaler, Recorder: recorder},
		windowProd:  &WindowResetProducer{},
		disruptProd: NewDisruptProducer(),
		manualProd:  &ManualProducer{},
	}
}

// Reconcile runs all producers (window_reset, disrupted, manual) but does
// NOT dispatch. Use this to inspect the pending store between reconcile
// and fire.
func (n *Nudger) Reconcile(ctx TickContext) {
	n.windowProd.Reconcile(ctx, n.store)
	n.disruptProd.Reconcile(ctx, n.store)
	// Manual is RPC-driven; Reconcile is a no-op but called for symmetry.
	n.manualProd.Reconcile(ctx, n.store)
}

// Dispatch runs the dispatcher against the current pending store.
func (n *Nudger) Dispatch(ctx TickContext) {
	n.dispatcher.Dispatch(ctx, n.store)
}

// Tick is a convenience: Reconcile then Dispatch. Existing tests use Tick.
func (n *Nudger) Tick(ctx TickContext) {
	n.Reconcile(ctx)
	n.Dispatch(ctx)
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
