package nudger

import (
	"testing"
	"time"
)

func TestManualProducerQueueAndCancel(t *testing.T) {
	p := &ManualProducer{}
	store := NewPendingStore()
	now := time.Now()
	p.Queue(store, []string{"sid-1", "sid-2"}, "continue", now)
	for _, sid := range []string{"sid-1", "sid-2"} {
		if !store.HasAny(sid) {
			t.Errorf("HasAny(%q) = false after Queue, want true", sid)
		}
	}
	p.Cancel(store, []string{"sid-1"})
	if store.HasAny("sid-1") {
		t.Error("HasAny(sid-1) = true after Cancel, want false")
	}
	if !store.HasAny("sid-2") {
		t.Error("HasAny(sid-2) = false, want true (not in cancel set)")
	}
}

func TestManualProducerQueueIdempotent(t *testing.T) {
	p := &ManualProducer{}
	store := NewPendingStore()
	now := time.Now()
	p.Queue(store, []string{"sid-1"}, "continue", now)
	p.Queue(store, []string{"sid-1"}, "different text", now.Add(time.Minute))
	got := store.List()
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	if got[0].Text != "continue" {
		t.Errorf("Text = %q, want %q (first wins; idempotent)", got[0].Text, "continue")
	}
}

func TestManualProducerReconcileIsNoop(t *testing.T) {
	// Manual is RPC-driven; Reconcile must be a no-op (does NOT cancel
	// manual intents on per-tick conditions).
	p := &ManualProducer{}
	store := NewPendingStore()
	store.Add(NudgeIntent{Key: IntentKey{"sid-1", SourceManual}, EmittedAt: time.Now()})
	p.Reconcile(TickContext{Now: time.Now()}, store)
	if !store.HasAny("sid-1") {
		t.Error("Reconcile cancelled a manual intent; should be no-op")
	}
}
