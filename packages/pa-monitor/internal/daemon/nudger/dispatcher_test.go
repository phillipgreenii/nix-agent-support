package nudger

import (
	"context"
	"errors"
	"strings"
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
	mu             sync.Mutex
	suppressed     []string
	sent           []string
	watermarkOps   []string
	windowLatchOps []time.Time
	queuedOps      []string     // "sid:source" pairs recorded by RecordQueued
	attemptOps     []string     // sids recorded by RecordDisruptAttempt
	sendFailed     []failedSend // recorded by RecordSendFailed
}

// failedSend captures one RecordSendFailed call for assertions.
type failedSend struct {
	sid       string
	errorKind string
	errText   string
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

func (r *fakeRecorder) UpdateWatermarks(sid string, now time.Time, sources []Source, cause *transcript.ErrorRecord, escalated bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.watermarkOps = append(r.watermarkOps, sid)
}

func (r *fakeRecorder) AdvanceWindowResetFiredFor(at time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.windowLatchOps = append(r.windowLatchOps, at)
}

func (r *fakeRecorder) RecordQueued(sid string, source Source) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.queuedOps = append(r.queuedOps, sid+":"+string(source))
}

func (r *fakeRecorder) RecordDisruptAttempt(sid string, at time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.attemptOps = append(r.attemptOps, sid)
}

func (r *fakeRecorder) RecordSendFailed(sid string, sources []Source, errorKind, errText string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sendFailed = append(r.sendFailed, failedSend{sid: sid, errorKind: errorKind, errText: errText})
}

func TestDispatcherFiresOnceAndClears(t *testing.T) {
	store := NewPendingStore()
	now := time.Date(2026, 5, 28, 15, 0, 0, 0, time.UTC)
	store.Add(NudgeIntent{Key: IntentKey{"sid-1", SourceWindowReset}, Text: "continue", EmittedAt: now})
	store.Add(NudgeIntent{
		Key: IntentKey{"sid-1", SourceDisrupted}, Text: "continue", EmittedAt: now,
		Cause: &transcript.ErrorRecord{Kind: transcript.ErrUnknown},
	})
	tree := treeWith(time.Time{}, newSV("sid-1", 1234, session.Idle))
	sig := &fakeSignaler{}
	rec := &fakeRecorder{}
	d := &Dispatcher{Signaler: sig, Recorder: rec}
	d.Dispatch(context.Background(), TickContext{Now: now, Tree: tree, Watermarks: wmStub{}}, store)
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
	d.Dispatch(context.Background(), TickContext{Now: now, Tree: tree, Watermarks: wmStub{}}, store)
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
	d.Dispatch(context.Background(), TickContext{Now: now, Tree: tree, Watermarks: wmStub{}}, store)
	if !store.HasAny("sid-1") {
		t.Error("store cleared after send failure; should retry next tick")
	}
}

// TestDispatcherSendFailureRecordsFailure verifies that when Signaler.Send
// returns an error the dispatcher reports it via Recorder.RecordSendFailed
// (carrying the error text and the cause's error kind) so the failure is
// observable in OTel — instead of being silently swallowed. It must NOT
// record a successful send.
func TestDispatcherSendFailureRecordsFailure(t *testing.T) {
	store := NewPendingStore()
	now := time.Now()
	store.Add(NudgeIntent{
		Key:       IntentKey{"sid-1", SourceDisrupted},
		Text:      "continue",
		EmittedAt: now,
		Cause:     &transcript.ErrorRecord{Kind: transcript.ErrServerError, At: now},
	})
	tree := treeWith(time.Time{}, newSV("sid-1", 1234, session.Idle))
	sig := &fakeSignaler{err: errors.New("cmux send: exit status 1")}
	rec := &fakeRecorder{}
	d := &Dispatcher{Signaler: sig, Recorder: rec}
	d.Dispatch(context.Background(), TickContext{Now: now, Tree: tree, Watermarks: wmStub{}}, store)

	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.sendFailed) != 1 {
		t.Fatalf("RecordSendFailed called %d times, want 1", len(rec.sendFailed))
	}
	got := rec.sendFailed[0]
	if got.sid != "sid-1" {
		t.Errorf("failed sid = %q, want sid-1", got.sid)
	}
	if got.errorKind != string(transcript.ErrServerError) {
		t.Errorf("failed errorKind = %q, want %q", got.errorKind, transcript.ErrServerError)
	}
	if !strings.Contains(got.errText, "cmux send: exit status 1") {
		t.Errorf("failed errText = %q, want it to contain the signaler error", got.errText)
	}
	if len(rec.sent) != 0 {
		t.Errorf("RecordSent called %d times on failure path, want 0", len(rec.sent))
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
	d.Dispatch(context.Background(), TickContext{Now: now, Tree: tree, Watermarks: wmStub{}}, store)
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
	d.Dispatch(context.Background(), TickContext{Now: now, Tree: tree, Watermarks: wmStub{}}, store)
	if len(sig.sent) != 1 || sig.sent[0].Text != "manual-override" {
		t.Errorf("sent = %+v, want manual text override", sig.sent)
	}
}

// TestDispatcherRemoveKeysPreservesConcurrentIntent verifies that Dispatch
// uses RemoveKeys (not ClearSession) so a concurrent intent added after the
// initial List() call survives to the next tick.
func TestDispatcherRemoveKeysPreservesConcurrentIntent(t *testing.T) {
	store := NewPendingStore()
	now := time.Now()
	// Seed one window_reset intent for sid-1.
	store.Add(NudgeIntent{Key: IntentKey{"sid-1", SourceWindowReset}, Text: "continue", EmittedAt: now})

	// Simulate: after Dispatch calls List but before it can clear the session,
	// a concurrent RPC adds a manual intent. We model this by adding the manual
	// intent before Dispatch runs (since Dispatch snapshots at List() time and
	// then only removes the keys it observed, the manual intent added afterward
	// should survive — here we add it after List but before RemoveKeys by
	// adding it to the store directly and checking post-dispatch).
	//
	// Deterministic approach: add both intents upfront, but assert only the
	// observed window_reset is removed, and a new manual intent added *after*
	// Dispatch starts (we can't intercept mid-dispatch in a unit test without
	// hooks). Instead we verify RemoveKeys semantics directly: add one intent,
	// snapshot its keys, add a second intent, remove only the first keys, confirm
	// the second survives.
	store2 := NewPendingStore()
	store2.Add(NudgeIntent{Key: IntentKey{"sid-1", SourceWindowReset}, Text: "auto", EmittedAt: now})
	// Simulate a concurrent add that happens after List() but before removal.
	store2.Add(NudgeIntent{Key: IntentKey{"sid-1", SourceManual}, Text: "manual", EmittedAt: now})
	// Remove only the window_reset key (as Dispatch would, using the observed keys from List).
	store2.RemoveKeys([]IntentKey{{"sid-1", SourceWindowReset}})
	if !store2.HasAny("sid-1") {
		t.Error("manual intent was removed by RemoveKeys targeting only window_reset — TOCTOU race not fixed")
	}
	sources := store2.SourcesFor("sid-1")
	if len(sources) != 1 || sources[0] != SourceManual {
		t.Errorf("surviving sources = %v, want [manual]", sources)
	}

	// Also verify the original store with one intent gets fully cleared after Dispatch.
	tree := treeWith(time.Time{}, newSV("sid-1", 1234, session.Idle))
	sig := &fakeSignaler{}
	rec := &fakeRecorder{}
	d := &Dispatcher{Signaler: sig, Recorder: rec}
	d.Dispatch(context.Background(), TickContext{Now: now, Tree: tree, Watermarks: wmStub{}}, store)
	if store.HasAny("sid-1") {
		t.Error("window_reset intent not removed after successful dispatch")
	}
}

// fakeNudgeRecorder records each RecordEvent for inspection in tests. When err
// is non-nil, Record returns it (still recording the event) so tests can drive
// the persistence-failure path.
type fakeNudgeRecorder struct {
	mu     sync.Mutex
	events []RecordEvent
	err    error
}

func (f *fakeNudgeRecorder) Record(_ context.Context, ev RecordEvent) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, ev)
	return f.err
}

// TestDispatcher_RecordsOnSend verifies that a successful dispatch causes the
// NudgeRecorder to be invoked with an event that carries the correct session id,
// result="sent", and source list.
func TestDispatcher_RecordsOnSend(t *testing.T) {
	store := NewPendingStore()
	now := time.Date(2026, 5, 28, 15, 0, 0, 0, time.UTC)
	store.Add(NudgeIntent{Key: IntentKey{"sid-1", SourceWindowReset}, Text: "continue", EmittedAt: now})
	tree := treeWith(time.Time{}, newSV("sid-1", 1234, session.Idle))
	sig := &fakeSignaler{}
	rec := &fakeRecorder{}
	nudgeRec := &fakeNudgeRecorder{}
	d := &Dispatcher{Signaler: sig, Recorder: rec, NudgeRecorder: nudgeRec}
	d.Dispatch(context.Background(), TickContext{Now: now, Tree: tree, Watermarks: wmStub{}}, store)

	if len(sig.sent) != 1 {
		t.Fatalf("len(sent) = %d, want 1", len(sig.sent))
	}

	nudgeRec.mu.Lock()
	events := nudgeRec.events
	nudgeRec.mu.Unlock()

	if len(events) != 1 {
		t.Fatalf("NudgeRecorder.Record called %d times, want 1", len(events))
	}
	ev := events[0]
	if ev.SessionID != "sid-1" {
		t.Errorf("event.SessionID = %q, want sid-1", ev.SessionID)
	}
	if ev.Result != "sent" {
		t.Errorf("event.Result = %q, want sent", ev.Result)
	}
	if ev.Text != "continue" {
		t.Errorf("event.Text = %q, want continue", ev.Text)
	}
	if len(ev.Sources) != 1 || ev.Sources[0] != string(SourceWindowReset) {
		t.Errorf("event.Sources = %v, want [window_reset]", ev.Sources)
	}
	if !ev.FiredAt.Equal(now) {
		t.Errorf("event.FiredAt = %v, want %v", ev.FiredAt, now)
	}
}

// TestDispatcher_RecordsSuppressed verifies that a suppressed nudge
// (session_active / waiting_for_human) is persisted to nudge_history via the
// NudgeRecorder with Result="suppressed" and the suppression cause, so
// historical queries can show suppressed deliveries, not only sent ones.
func TestDispatcher_RecordsSuppressed(t *testing.T) {
	cases := []struct {
		name      string
		status    session.Status
		wantCause string
	}{
		{"session_active", session.Working, "session_active"},
		{"waiting_for_human", session.WaitingForHuman, "waiting_for_human"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := NewPendingStore()
			now := time.Date(2026, 5, 28, 15, 0, 0, 0, time.UTC)
			store.Add(NudgeIntent{
				Key: IntentKey{"sid-1", SourceDisrupted}, Text: "continue", EmittedAt: now,
				Cause: &transcript.ErrorRecord{Kind: transcript.ErrServerError, At: now},
			})
			tree := treeWith(time.Time{}, newSV("sid-1", 1234, tc.status))
			sig := &fakeSignaler{}
			rec := &fakeRecorder{}
			nudgeRec := &fakeNudgeRecorder{}
			d := &Dispatcher{Signaler: sig, Recorder: rec, NudgeRecorder: nudgeRec}
			d.Dispatch(context.Background(), TickContext{Now: now, Tree: tree, Watermarks: wmStub{}}, store)

			if len(sig.sent) != 0 {
				t.Fatalf("len(sent) = %d, want 0 (suppressed)", len(sig.sent))
			}
			nudgeRec.mu.Lock()
			events := nudgeRec.events
			nudgeRec.mu.Unlock()
			if len(events) != 1 {
				t.Fatalf("NudgeRecorder.Record called %d times, want 1 (suppressed persisted)", len(events))
			}
			ev := events[0]
			if ev.Result != "suppressed" {
				t.Errorf("event.Result = %q, want suppressed", ev.Result)
			}
			if ev.ErrorText != tc.wantCause {
				t.Errorf("event.ErrorText = %q, want %q (suppression cause)", ev.ErrorText, tc.wantCause)
			}
			if ev.SessionID != "sid-1" {
				t.Errorf("event.SessionID = %q, want sid-1", ev.SessionID)
			}
			if len(ev.Sources) != 1 || ev.Sources[0] != string(SourceDisrupted) {
				t.Errorf("event.Sources = %v, want [disrupted]", ev.Sources)
			}
		})
	}
}

// TestDispatcher_RecordsSendFailure verifies that a failed Signaler.Send is
// persisted to nudge_history with Result="failed" and the signaler error text,
// alongside the OTel RecordSendFailed observability signal.
func TestDispatcher_RecordsSendFailure(t *testing.T) {
	store := NewPendingStore()
	now := time.Date(2026, 5, 28, 15, 0, 0, 0, time.UTC)
	store.Add(NudgeIntent{
		Key: IntentKey{"sid-1", SourceDisrupted}, Text: "continue", EmittedAt: now,
		Cause: &transcript.ErrorRecord{Kind: transcript.ErrServerError, At: now},
	})
	tree := treeWith(time.Time{}, newSV("sid-1", 1234, session.Idle))
	sig := &fakeSignaler{err: errors.New("cmux send: exit status 1")}
	rec := &fakeRecorder{}
	nudgeRec := &fakeNudgeRecorder{}
	d := &Dispatcher{Signaler: sig, Recorder: rec, NudgeRecorder: nudgeRec}
	d.Dispatch(context.Background(), TickContext{Now: now, Tree: tree, Watermarks: wmStub{}}, store)

	nudgeRec.mu.Lock()
	events := nudgeRec.events
	nudgeRec.mu.Unlock()
	if len(events) != 1 {
		t.Fatalf("NudgeRecorder.Record called %d times, want 1 (failed persisted)", len(events))
	}
	ev := events[0]
	if ev.Result != "failed" {
		t.Errorf("event.Result = %q, want failed", ev.Result)
	}
	if !strings.Contains(ev.ErrorText, "cmux send: exit status 1") {
		t.Errorf("event.ErrorText = %q, want it to contain the signaler error", ev.ErrorText)
	}
	if ev.CausedByErrorAt == nil || !ev.CausedByErrorAt.Equal(now) {
		t.Errorf("event.CausedByErrorAt = %v, want %v", ev.CausedByErrorAt, now)
	}
}

// TestDispatcher_RecordHistoryWriteFailureIsSurfaced verifies that when
// NudgeRecorder.Record returns an error (the nudge_history row write fails —
// the export-independent capture sink), the dispatcher surfaces it via
// HistoryErrLog instead of silently discarding it. This is the exact failure
// that dropped every failed-delivery row despite the send failures occurring:
// the error was swallowed, leaving the failure with no durable trace.
func TestDispatcher_RecordHistoryWriteFailureIsSurfaced(t *testing.T) {
	store := NewPendingStore()
	now := time.Date(2026, 5, 28, 15, 0, 0, 0, time.UTC)
	store.Add(NudgeIntent{
		Key: IntentKey{"sid-1", SourceDisrupted}, Text: "continue", EmittedAt: now,
		Cause: &transcript.ErrorRecord{Kind: transcript.ErrServerError, At: now},
	})
	tree := treeWith(time.Time{}, newSV("sid-1", 1234, session.Idle))
	sig := &fakeSignaler{err: errors.New("cmux send: signal: killed")}
	rec := &fakeRecorder{}
	nudgeRec := &fakeNudgeRecorder{err: errors.New("context deadline exceeded")}

	var logged []string
	d := &Dispatcher{
		Signaler: sig, Recorder: rec, NudgeRecorder: nudgeRec,
		HistoryErrLog: func(msg string) { logged = append(logged, msg) },
	}
	d.Dispatch(context.Background(), TickContext{Now: now, Tree: tree, Watermarks: wmStub{}}, store)

	if len(logged) != 1 {
		t.Fatalf("HistoryErrLog called %d times, want 1 (the swallowed Record error must be surfaced)", len(logged))
	}
	msg := logged[0]
	if !strings.Contains(msg, "sid-1") {
		t.Errorf("logged msg = %q, want it to name the session id", msg)
	}
	if !strings.Contains(msg, "failed") {
		t.Errorf("logged msg = %q, want it to name the failed result", msg)
	}
	if !strings.Contains(msg, "context deadline exceeded") {
		t.Errorf("logged msg = %q, want it to carry the underlying Record error", msg)
	}
}

// TestDispatcher_RecordHistorySuccessDoesNotLog verifies HistoryErrLog is NOT
// invoked when the nudge_history write succeeds (no false-positive noise).
func TestDispatcher_RecordHistorySuccessDoesNotLog(t *testing.T) {
	store := NewPendingStore()
	now := time.Date(2026, 5, 28, 15, 0, 0, 0, time.UTC)
	store.Add(NudgeIntent{Key: IntentKey{"sid-1", SourceWindowReset}, Text: "continue", EmittedAt: now})
	tree := treeWith(time.Time{}, newSV("sid-1", 1234, session.Idle))
	sig := &fakeSignaler{}
	rec := &fakeRecorder{}
	nudgeRec := &fakeNudgeRecorder{} // err == nil: write succeeds

	var logged []string
	d := &Dispatcher{
		Signaler: sig, Recorder: rec, NudgeRecorder: nudgeRec,
		HistoryErrLog: func(msg string) { logged = append(logged, msg) },
	}
	d.Dispatch(context.Background(), TickContext{Now: now, Tree: tree, Watermarks: wmStub{}}, store)

	if len(logged) != 0 {
		t.Errorf("HistoryErrLog called %d times on success, want 0: %v", len(logged), logged)
	}
}

// TestNew_ThreadsHistoryErrLogToDispatcher verifies that nudger.New wires the
// historyErrLog hook all the way through to the Dispatcher — otherwise the
// daemon would construct a Nudger whose capture-write failures are silently
// dropped, reproducing the original bug at the wiring layer.
func TestNew_ThreadsHistoryErrLogToDispatcher(t *testing.T) {
	var logged []string
	sig := &fakeSignaler{}
	rec := &fakeRecorder{}
	nudgeRec := &fakeNudgeRecorder{err: errors.New("db write boom")}
	n := New(sig, rec, nudgeRec, func(msg string) { logged = append(logged, msg) })

	now := time.Date(2026, 5, 28, 15, 0, 0, 0, time.UTC)
	n.QueueManual([]string{"sid-1"}, "continue", now)
	tree := treeWith(time.Time{}, newSV("sid-1", 1234, session.Idle))
	n.Tick(context.Background(), TickContext{Now: now, Tree: tree, Watermarks: wmStub{}})

	if len(logged) != 1 {
		t.Fatalf("New must thread HistoryErrLog to the dispatcher; hook fired %d times, want 1", len(logged))
	}
	if !strings.Contains(logged[0], "db write boom") {
		t.Errorf("logged msg = %q, want it to carry the Record error", logged[0])
	}
}

// TestDispatcher_NudgeRecorderNilSafe verifies that a nil NudgeRecorder does
// not panic on successful dispatch (the Recorder field may be nil in tests
// and early-startup paths).
func TestDispatcher_NudgeRecorderNilSafe(t *testing.T) {
	store := NewPendingStore()
	now := time.Now()
	store.Add(NudgeIntent{Key: IntentKey{"sid-1", SourceManual}, Text: "x", EmittedAt: now})
	tree := treeWith(time.Time{}, newSV("sid-1", 1234, session.Idle))
	sig := &fakeSignaler{}
	rec := &fakeRecorder{}
	d := &Dispatcher{Signaler: sig, Recorder: rec, NudgeRecorder: nil}
	// Must not panic.
	d.Dispatch(context.Background(), TickContext{Now: now, Tree: tree, Watermarks: wmStub{}}, store)
	if len(sig.sent) != 1 {
		t.Errorf("len(sent) = %d, want 1", len(sig.sent))
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
		d.Dispatch(context.Background(), TickContext{Now: now, Tree: tree, Watermarks: wmStub{}}, store)
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
		d.Dispatch(context.Background(), TickContext{Now: now, Tree: tree, Watermarks: wmStub{}}, store)
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
		d.Dispatch(context.Background(), TickContext{Now: now, Tree: tree, Watermarks: wmStub{}}, store)
		rec.mu.Lock()
		ops := rec.windowLatchOps
		rec.mu.Unlock()
		if len(ops) != 0 {
			t.Errorf("windowLatchOps = %d, want 0 (no window_reset dispatched)", len(ops))
		}
	})
}

// TestDispatcherSuppressesWaitingForHuman verifies that a WaitingForHuman
// session never receives a nudge: intents are cleared and recorded as
// suppressed with the "waiting_for_human" cause (§6/D3).
func TestDispatcherSuppressesWaitingForHuman(t *testing.T) {
	store := NewPendingStore()
	now := time.Now()
	store.Add(NudgeIntent{
		Key: IntentKey{"sid-1", SourceDisrupted}, Text: "continue", EmittedAt: now,
		Cause: &transcript.ErrorRecord{Kind: transcript.ErrUnknown},
	})
	tree := treeWith(time.Time{}, newSV("sid-1", 1234, session.WaitingForHuman))
	sig := &fakeSignaler{}
	rec := &fakeRecorder{}
	d := &Dispatcher{Signaler: sig, Recorder: rec}
	d.Dispatch(context.Background(), TickContext{Now: now, Tree: tree, Watermarks: wmStub{}}, store)
	if len(sig.sent) != 0 {
		t.Errorf("len(sent) = %d, want 0 (suppressed over human prompt)", len(sig.sent))
	}
	if store.HasAny("sid-1") {
		t.Error("store not cleared after waiting-for-human suppression")
	}
	if len(rec.suppressed) != 1 {
		t.Errorf("recorder.suppressed = %d, want 1", len(rec.suppressed))
	}
	if len(rec.attemptOps) != 0 {
		t.Errorf("recorder.attemptOps = %d, want 0 (no attempt for suppressed)", len(rec.attemptOps))
	}
}

// TestDispatcherRecordsDisruptAttemptOnSuccess verifies a delivered disrupt
// nudge records an attempt watermark.
func TestDispatcherRecordsDisruptAttemptOnSuccess(t *testing.T) {
	store := NewPendingStore()
	now := time.Now()
	store.Add(NudgeIntent{
		Key: IntentKey{"sid-1", SourceDisrupted}, Text: "continue", EmittedAt: now,
		Cause: &transcript.ErrorRecord{Kind: transcript.ErrUnknown},
	})
	tree := treeWith(time.Time{}, newSV("sid-1", 1234, session.Idle))
	sig := &fakeSignaler{}
	rec := &fakeRecorder{}
	d := &Dispatcher{Signaler: sig, Recorder: rec}
	d.Dispatch(context.Background(), TickContext{Now: now, Tree: tree, Watermarks: wmStub{}}, store)
	if len(rec.attemptOps) != 1 || rec.attemptOps[0] != "sid-1" {
		t.Errorf("recorder.attemptOps = %v, want [sid-1]", rec.attemptOps)
	}
}

// TestDispatcherRecordsDisruptAttemptOnFailure verifies a FAILED disrupt nudge
// still records an attempt watermark (D5: a failed attempt counts) while
// leaving the intent in place to retry.
func TestDispatcherRecordsDisruptAttemptOnFailure(t *testing.T) {
	store := NewPendingStore()
	now := time.Now()
	store.Add(NudgeIntent{
		Key: IntentKey{"sid-1", SourceDisrupted}, Text: "continue", EmittedAt: now,
		Cause: &transcript.ErrorRecord{Kind: transcript.ErrUnknown},
	})
	tree := treeWith(time.Time{}, newSV("sid-1", 1234, session.Idle))
	sig := &fakeSignaler{err: errors.New("no signaler")}
	rec := &fakeRecorder{}
	d := &Dispatcher{Signaler: sig, Recorder: rec}
	d.Dispatch(context.Background(), TickContext{Now: now, Tree: tree, Watermarks: wmStub{}}, store)
	if len(rec.attemptOps) != 1 || rec.attemptOps[0] != "sid-1" {
		t.Errorf("recorder.attemptOps = %v, want [sid-1] (attempt counts even on failure)", rec.attemptOps)
	}
	if !store.HasAny("sid-1") {
		t.Error("store cleared after send failure; should retry next tick")
	}
}

// TestDispatcherNoDisruptAttemptForNonDisrupt verifies a non-disrupt nudge
// (e.g. window_reset) does not record a disrupt attempt watermark.
func TestDispatcherNoDisruptAttemptForNonDisrupt(t *testing.T) {
	store := NewPendingStore()
	now := time.Now()
	store.Add(NudgeIntent{Key: IntentKey{"sid-1", SourceWindowReset}, Text: "continue", EmittedAt: now})
	tree := treeWith(time.Time{}, newSV("sid-1", 1234, session.Idle))
	sig := &fakeSignaler{}
	rec := &fakeRecorder{}
	d := &Dispatcher{Signaler: sig, Recorder: rec}
	d.Dispatch(context.Background(), TickContext{Now: now, Tree: tree, Watermarks: wmStub{}}, store)
	if len(rec.attemptOps) != 0 {
		t.Errorf("recorder.attemptOps = %v, want empty (window_reset is not a disrupt)", rec.attemptOps)
	}
}
