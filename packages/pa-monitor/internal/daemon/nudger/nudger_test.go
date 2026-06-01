package nudger

import (
	"context"
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/session"
	"github.com/phillipgreenii/pa-monitor/internal/core/transcript"
)

func TestNudgerTickDisruptedFlowEndToEnd(t *testing.T) {
	now := time.Date(2026, 5, 28, 15, 0, 0, 0, time.UTC)
	tree := treeWith(time.Time{}, sessionWithError(
		"sid-1", transcript.ErrUnknown, now.Add(-31*time.Second), true,
	))
	tree.Dirs[0].Sessions[0].PID = 4321
	tree.Dirs[0].Sessions[0].Status = session.Idle

	sig := &fakeSignaler{}
	rec := &fakeRecorder{}
	n := New(sig, rec, nil)

	// Tick 1: first sighting at now-31s; grace not yet elapsed against firstSeen=now.
	n.Tick(context.Background(), TickContext{
		Now: now.Add(-31 * time.Second), AutoResumeEnabled: true,
		AutoResumeMessage: "continue", DisruptGrace: 30 * time.Second,
		EscalationAfter: 60 * time.Second, Tree: tree, Watermarks: wmStub{},
	})
	if len(sig.sent) != 0 {
		t.Errorf("tick 1: sent = %d, want 0 (grace not elapsed)", len(sig.sent))
	}

	// Tick 2: 31s later — grace elapsed; dispatcher fires.
	n.Tick(context.Background(), TickContext{
		Now: now, AutoResumeEnabled: true,
		AutoResumeMessage: "continue", DisruptGrace: 30 * time.Second,
		EscalationAfter: 60 * time.Second, Tree: tree, Watermarks: wmStub{},
	})
	if len(sig.sent) != 1 {
		t.Fatalf("tick 2: sent = %d, want 1", len(sig.sent))
	}
	if sig.sent[0].PID != 4321 || sig.sent[0].Text != "continue" {
		t.Errorf("sent = %+v, want PID=4321 text=continue", sig.sent[0])
	}
}

func TestNudgerTickManualBypassesProducers(t *testing.T) {
	now := time.Now()
	tree := treeWith(time.Time{}, newSV("sid-1", 9999, session.Idle))
	sig := &fakeSignaler{}
	rec := &fakeRecorder{}
	n := New(sig, rec, nil)
	n.QueueManual([]string{"sid-1"}, "manual!", now)
	n.Tick(context.Background(), TickContext{
		Now: now, AutoResumeEnabled: false, // disabled disables auto producers
		Tree: tree, Watermarks: wmStub{},
	})
	if len(sig.sent) != 1 || sig.sent[0].Text != "manual!" {
		t.Errorf("sent = %+v, want manual fire even with auto disabled", sig.sent)
	}
}

func TestNudgerCancelManual(t *testing.T) {
	now := time.Now()
	n := New(&fakeSignaler{}, &fakeRecorder{}, nil)
	n.QueueManual([]string{"sid-1"}, "x", now)
	if !n.PendingFor("sid-1") {
		t.Error("expected pending after QueueManual")
	}
	n.CancelManual([]string{"sid-1"})
	if n.PendingFor("sid-1") {
		t.Error("expected no pending after CancelManual")
	}
}

// TestNudgerReconcileEmitsQueuedCounter verifies that Reconcile calls
// RecordQueued exactly once for each newly-added intent (the
// pa_monitor.nudge.queued_total counter path).
func TestNudgerReconcileEmitsQueuedCounter(t *testing.T) {
	now := time.Date(2026, 5, 28, 15, 0, 0, 0, time.UTC)
	// Session with a retryable terminal error; grace=0 so the intent is
	// added on tick 2 (tick 1 only primes firstSeen).
	tree := treeWith(time.Time{}, sessionWithError(
		"sid-q", transcript.ErrUnknown, now.Add(-31*time.Second), true,
	))
	tree.Dirs[0].Sessions[0].PID = 5555
	tree.Dirs[0].Sessions[0].Status = session.Idle

	rec := &fakeRecorder{}
	n := New(&fakeSignaler{}, rec, nil)

	// Tick 1: primes firstSeen — no intent added yet.
	n.Reconcile(TickContext{
		Now: now.Add(-31 * time.Second), AutoResumeEnabled: true,
		AutoResumeMessage: "continue", DisruptGrace: 30 * time.Second,
		EscalationAfter: 60 * time.Second, Tree: tree, Watermarks: wmStub{},
	})
	rec.mu.Lock()
	queuedAfterTick1 := len(rec.queuedOps)
	rec.mu.Unlock()
	if queuedAfterTick1 != 0 {
		t.Errorf("tick 1: RecordQueued called %d times, want 0 (grace not elapsed)", queuedAfterTick1)
	}

	// Tick 2: grace elapsed; intent added — RecordQueued should fire once.
	n.Reconcile(TickContext{
		Now: now, AutoResumeEnabled: true,
		AutoResumeMessage: "continue", DisruptGrace: 30 * time.Second,
		EscalationAfter: 60 * time.Second, Tree: tree, Watermarks: wmStub{},
	})
	rec.mu.Lock()
	queuedAfterTick2 := len(rec.queuedOps)
	rec.mu.Unlock()
	if queuedAfterTick2 != 1 {
		t.Errorf("tick 2: RecordQueued called %d times, want 1", queuedAfterTick2)
	}
	if rec.queuedOps[0] != "sid-q:disrupted" {
		t.Errorf("RecordQueued arg = %q, want sid-q:disrupted", rec.queuedOps[0])
	}

	// Tick 3: same intent already in store — RecordQueued must NOT fire again.
	n.Reconcile(TickContext{
		Now: now.Add(1 * time.Second), AutoResumeEnabled: true,
		AutoResumeMessage: "continue", DisruptGrace: 30 * time.Second,
		EscalationAfter: 60 * time.Second, Tree: tree, Watermarks: wmStub{},
	})
	rec.mu.Lock()
	queuedAfterTick3 := len(rec.queuedOps)
	rec.mu.Unlock()
	if queuedAfterTick3 != 1 {
		t.Errorf("tick 3: RecordQueued called %d times (total), want 1 (idempotent)", queuedAfterTick3)
	}
}
