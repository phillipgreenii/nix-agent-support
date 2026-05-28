package nudger

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/session"
	"github.com/phillipgreenii/pa-monitor/internal/core/transcript"
)

type fakeSignaler struct {
	mu   sync.Mutex
	sent []struct {
		PID  int
		Text string
	}
	err error
}

func (f *fakeSignaler) Send(pid int, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.sent = append(f.sent, struct {
		PID  int
		Text string
	}{pid, text})
	return nil
}

type fakeRecorder struct {
	mu               sync.Mutex
	suppressed       []string
	sent             []string
	watermarkOps     []string
	windowLatchOps   []time.Time
}

func (r *fakeRecorder) RecordSuppressed(sid string, sources []Source, cause string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.suppressed = append(r.suppressed, sid)
}
func (r *fakeRecorder) RecordSent(sid string, sources []Source, errorKind string, escalated bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sent = append(r.sent, sid)
}
func (r *fakeRecorder) UpdateWatermarks(sid string, now time.Time, cause *transcript.ErrorRecord, escalated bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.watermarkOps = append(r.watermarkOps, sid)
}
func (r *fakeRecorder) AdvanceWindowResetFiredFor(at time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.windowLatchOps = append(r.windowLatchOps, at)
}

func TestDispatcherFiresOnceAndClears(t *testing.T) {
	store := NewPendingStore()
	now := time.Date(2026, 5, 28, 15, 0, 0, 0, time.UTC)
	store.Add(NudgeIntent{Key: IntentKey{"sid-1", SourceWindowReset}, Text: "continue", EmittedAt: now})
	store.Add(NudgeIntent{Key: IntentKey{"sid-1", SourceDisrupted}, Text: "continue", EmittedAt: now,
		Cause: &transcript.ErrorRecord{Kind: transcript.ErrUnknown}})
	tree := treeWith(time.Time{}, newSV("sid-1", 1234, session.Idle))
	sig := &fakeSignaler{}
	rec := &fakeRecorder{}
	d := &Dispatcher{Signaler: sig, Recorder: rec}
	d.Dispatch(TickContext{Now: now, Tree: tree, Watermarks: wmStub{}}, store)
	if len(sig.sent) != 1 {
		t.Fatalf("len(sent) = %d, want 1 (one signal per session)", len(sig.sent))
	}
	if sig.sent[0].PID != 1234 || sig.sent[0].Text != "continue" {
		t.Errorf("sent = %+v, want PID=1234 Text=continue", sig.sent[0])
	}
	if store.HasAny("sid-1") {
		t.Error("store not cleared for sid-1 after fire")
	}
	if len(rec.sent) != 1 {
		t.Errorf("recorder.sent count = %d, want 1", len(rec.sent))
	}
}

func TestDispatcherSuppressesWorking(t *testing.T) {
	store := NewPendingStore()
	now := time.Now()
	store.Add(NudgeIntent{Key: IntentKey{"sid-1", SourceManual}, Text: "x", EmittedAt: now})
	tree := treeWith(time.Time{}, newSV("sid-1", 1234, session.Working))
	sig := &fakeSignaler{}
	rec := &fakeRecorder{}
	d := &Dispatcher{Signaler: sig, Recorder: rec}
	d.Dispatch(TickContext{Now: now, Tree: tree, Watermarks: wmStub{}}, store)
	if len(sig.sent) != 0 {
		t.Errorf("len(sent) = %d, want 0 (suppressed)", len(sig.sent))
	}
	if store.HasAny("sid-1") {
		t.Error("store not cleared after suppression")
	}
	if len(rec.suppressed) != 1 {
		t.Errorf("recorder.suppressed = %d, want 1", len(rec.suppressed))
	}
}

func TestDispatcherSendFailureLeavesIntent(t *testing.T) {
	store := NewPendingStore()
	now := time.Now()
	store.Add(NudgeIntent{Key: IntentKey{"sid-1", SourceManual}, Text: "x", EmittedAt: now})
	tree := treeWith(time.Time{}, newSV("sid-1", 1234, session.Idle))
	sig := &fakeSignaler{err: errors.New("no signaler for pid")}
	rec := &fakeRecorder{}
	d := &Dispatcher{Signaler: sig, Recorder: rec}
	d.Dispatch(TickContext{Now: now, Tree: tree, Watermarks: wmStub{}}, store)
	if !store.HasAny("sid-1") {
		t.Error("store cleared after send failure; should retry next tick")
	}
}

func TestDispatcherSessionMissingSilently(t *testing.T) {
	store := NewPendingStore()
	now := time.Now()
	store.Add(NudgeIntent{Key: IntentKey{"missing-sid", SourceManual}, Text: "x", EmittedAt: now})
	tree := treeWith(time.Time{}) // no sessions
	sig := &fakeSignaler{}
	rec := &fakeRecorder{}
	d := &Dispatcher{Signaler: sig, Recorder: rec}
	d.Dispatch(TickContext{Now: now, Tree: tree, Watermarks: wmStub{}}, store)
	if store.HasAny("missing-sid") {
		t.Error("intent not dropped for missing session")
	}
	if len(sig.sent) != 0 {
		t.Errorf("len(sent) = %d, want 0", len(sig.sent))
	}
}

func TestDispatcherTextPrecedenceManualWins(t *testing.T) {
	store := NewPendingStore()
	now := time.Now()
	store.Add(NudgeIntent{Key: IntentKey{"sid", SourceWindowReset}, Text: "auto", EmittedAt: now})
	store.Add(NudgeIntent{Key: IntentKey{"sid", SourceManual}, Text: "manual-override", EmittedAt: now})
	tree := treeWith(time.Time{}, newSV("sid", 1, session.Idle))
	sig := &fakeSignaler{}
	rec := &fakeRecorder{}
	d := &Dispatcher{Signaler: sig, Recorder: rec}
	d.Dispatch(TickContext{Now: now, Tree: tree, Watermarks: wmStub{}}, store)
	if len(sig.sent) != 1 || sig.sent[0].Text != "manual-override" {
		t.Errorf("sent = %+v, want manual text override", sig.sent)
	}
}

// TestDispatcherWindowLatchAdvancesOnWindowResetDispatch verifies that
// AdvanceWindowResetFiredFor is called exactly once when a SourceWindowReset
// intent is dispatched, and NOT called when only SourceDisrupted or
// SourceManual intents are dispatched.
func TestDispatcherWindowLatchAdvancesOnWindowResetDispatch(t *testing.T) {
	resetsAt := time.Date(2026, 5, 28, 20, 0, 0, 0, time.UTC)
	now := resetsAt.Add(-5 * time.Minute)

	t.Run("window_reset dispatched advances latch", func(t *testing.T) {
		store := NewPendingStore()
		store.Add(NudgeIntent{Key: IntentKey{"sid-wr", SourceWindowReset}, Text: "continue", EmittedAt: now})
		tree := treeWith(resetsAt, newSV("sid-wr", 1111, session.Idle))
		sig := &fakeSignaler{}
		rec := &fakeRecorder{}
		d := &Dispatcher{Signaler: sig, Recorder: rec}
		d.Dispatch(TickContext{Now: now, Tree: tree, Watermarks: wmStub{}}, store)
		rec.mu.Lock()
		ops := rec.windowLatchOps
		rec.mu.Unlock()
		if len(ops) != 1 {
			t.Fatalf("windowLatchOps = %d, want 1", len(ops))
		}
		if !ops[0].Equal(resetsAt) {
			t.Errorf("windowLatchOps[0] = %v, want %v", ops[0], resetsAt)
		}
	})

	t.Run("only disrupted dispatched does NOT advance latch", func(t *testing.T) {
		store := NewPendingStore()
		store.Add(NudgeIntent{Key: IntentKey{"sid-d", SourceDisrupted}, Text: "continue", EmittedAt: now})
		tree := treeWith(resetsAt, newSV("sid-d", 2222, session.Idle))
		sig := &fakeSignaler{}
		rec := &fakeRecorder{}
		d := &Dispatcher{Signaler: sig, Recorder: rec}
		d.Dispatch(TickContext{Now: now, Tree: tree, Watermarks: wmStub{}}, store)
		rec.mu.Lock()
		ops := rec.windowLatchOps
		rec.mu.Unlock()
		if len(ops) != 0 {
			t.Errorf("windowLatchOps = %d, want 0 (no window_reset dispatched)", len(ops))
		}
	})

	t.Run("only manual dispatched does NOT advance latch", func(t *testing.T) {
		store := NewPendingStore()
		store.Add(NudgeIntent{Key: IntentKey{"sid-m", SourceManual}, Text: "hey", EmittedAt: now})
		tree := treeWith(resetsAt, newSV("sid-m", 3333, session.Idle))
		sig := &fakeSignaler{}
		rec := &fakeRecorder{}
		d := &Dispatcher{Signaler: sig, Recorder: rec}
		d.Dispatch(TickContext{Now: now, Tree: tree, Watermarks: wmStub{}}, store)
		rec.mu.Lock()
		ops := rec.windowLatchOps
		rec.mu.Unlock()
		if len(ops) != 0 {
			t.Errorf("windowLatchOps = %d, want 0 (no window_reset dispatched)", len(ops))
		}
	})
}
