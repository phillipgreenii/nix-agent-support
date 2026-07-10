package nudger

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/aggregate"
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
	mu              sync.Mutex
	suppressed      []string
	sent            []string
	watermarkOps    []string
	windowLatchOps  []time.Time
	limitLatchOps   []time.Time       // recorded by AdvanceLimitPauseFiredFor
	queuedOps       []string          // "sid:source" pairs recorded by RecordQueued
	attemptOps      []string          // sids recorded by RecordDisruptAttempt
	sendFailed      []failedSend      // recorded by RecordSendFailed
	droppedNoBridge []droppedNoBridge // recorded by RecordDroppedNoBridge
}

// failedSend captures one RecordSendFailed call for assertions.
type failedSend struct {
	sid       string
	errorKind string
	errText   string
}

// droppedNoBridge captures one RecordDroppedNoBridge call for assertions.
type droppedNoBridge struct {
	sid     string
	sources []Source
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

func (r *fakeRecorder) AdvanceLimitPauseFiredFor(at time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.limitLatchOps = append(r.limitLatchOps, at)
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

func (r *fakeRecorder) RecordDroppedNoBridge(sid string, sources []Source) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.droppedNoBridge = append(r.droppedNoBridge, droppedNoBridge{sid: sid, sources: sources})
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
	d := &Dispatcher{Deliverer: signalerDeliverer{sig}, Recorder: rec}
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
	d := &Dispatcher{Deliverer: signalerDeliverer{sig}, Recorder: rec}
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
	d := &Dispatcher{Deliverer: signalerDeliverer{sig}, Recorder: rec}
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
	d := &Dispatcher{Deliverer: signalerDeliverer{sig}, Recorder: rec}
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

// TestDispatcherNoCmuxSurfaceSuppressesNotFails covers the ghost-session bug
// (bead pg2-...): a session whose cmux surface is gone (pane closed / process
// orphaned) can never receive a nudge, so Send returns "no cmux surface found
// for pid N". The old behavior recorded a send-failure and LEFT the intent
// queued, so it retried and spammed signal_send_failures_total forever (~61/15min
// observed live). Instead, a no-surface outcome must be recorded as a
// SUPPRESSION (not a failure) and the intent DROPPED — the target is
// unreachable, not transiently erroring. The error is matched by string because
// it crosses the cmux-bridge as a plain string (DeliverResult.Error).
func TestDispatcherNoCmuxSurfaceSuppressesNotFails(t *testing.T) {
	store := NewPendingStore()
	now := time.Now()
	store.Add(NudgeIntent{Key: IntentKey{"sid-1", SourceWindowReset}, Text: "continue", EmittedAt: now})
	tree := treeWith(time.Time{}, newSV("sid-1", 45600, session.Idle))
	sig := &fakeSignaler{err: errors.New("signal: no cmux surface found for pid 45600")}
	rec := &fakeRecorder{}
	d := &Dispatcher{Deliverer: signalerDeliverer{sig}, Recorder: rec}
	d.Dispatch(context.Background(), TickContext{Now: now, Tree: tree, Watermarks: wmStub{}}, store)

	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.sendFailed) != 0 {
		t.Errorf("no-surface must NOT record send-failed (that spams the failure counter forever); got %d", len(rec.sendFailed))
	}
	if len(rec.suppressed) != 1 || rec.suppressed[0] != "sid-1" {
		t.Errorf("no-surface should record ONE suppression for sid-1; got %v", rec.suppressed)
	}
	if store.HasAny("sid-1") {
		t.Errorf("no-surface intent should be dropped (an unreachable surface won't heal by retrying)")
	}
}

func TestDispatcherSessionMissingSilently(t *testing.T) {
	store := NewPendingStore()
	now := time.Now()
	store.Add(NudgeIntent{Key: IntentKey{"missing-sid", SourceManual}, Text: "x", EmittedAt: now})
	tree := treeWith(time.Time{}) // no sessions
	sig := &fakeSignaler{}
	rec := &fakeRecorder{}
	d := &Dispatcher{Deliverer: signalerDeliverer{sig}, Recorder: rec}
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
	d := &Dispatcher{Deliverer: signalerDeliverer{sig}, Recorder: rec}
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
	d := &Dispatcher{Deliverer: signalerDeliverer{sig}, Recorder: rec}
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
	d := &Dispatcher{Deliverer: signalerDeliverer{sig}, Recorder: rec, NudgeRecorder: nudgeRec}
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
		sv        *aggregate.SessionView
		wantCause string
	}{
		{"session_active", newSV("sid-1", 1234, session.Working), "session_active"},
		{"waiting_for_human", newSVBlocked("sid-1", 1234, session.HumanInput), "waiting_for_human"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := NewPendingStore()
			now := time.Date(2026, 5, 28, 15, 0, 0, 0, time.UTC)
			store.Add(NudgeIntent{
				Key: IntentKey{"sid-1", SourceDisrupted}, Text: "continue", EmittedAt: now,
				Cause: &transcript.ErrorRecord{Kind: transcript.ErrServerError, At: now},
			})
			tree := treeWith(time.Time{}, tc.sv)
			sig := &fakeSignaler{}
			rec := &fakeRecorder{}
			nudgeRec := &fakeNudgeRecorder{}
			d := &Dispatcher{Deliverer: signalerDeliverer{sig}, Recorder: rec, NudgeRecorder: nudgeRec}
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
	d := &Dispatcher{Deliverer: signalerDeliverer{sig}, Recorder: rec, NudgeRecorder: nudgeRec}
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
		Deliverer: signalerDeliverer{sig}, Recorder: rec, NudgeRecorder: nudgeRec,
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
		Deliverer: signalerDeliverer{sig}, Recorder: rec, NudgeRecorder: nudgeRec,
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
	n := New(signalerDeliverer{sig}, rec, nudgeRec, func(msg string) { logged = append(logged, msg) })

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
	d := &Dispatcher{Deliverer: signalerDeliverer{sig}, Recorder: rec, NudgeRecorder: nil}
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
		d := &Dispatcher{Deliverer: signalerDeliverer{sig}, Recorder: rec}
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
		d := &Dispatcher{Deliverer: signalerDeliverer{sig}, Recorder: rec}
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
		d := &Dispatcher{Deliverer: signalerDeliverer{sig}, Recorder: rec}
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
	tree := treeWith(time.Time{}, newSVBlocked("sid-1", 1234, session.HumanInput))
	sig := &fakeSignaler{}
	rec := &fakeRecorder{}
	d := &Dispatcher{Deliverer: signalerDeliverer{sig}, Recorder: rec}
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
	d := &Dispatcher{Deliverer: signalerDeliverer{sig}, Recorder: rec}
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
	d := &Dispatcher{Deliverer: signalerDeliverer{sig}, Recorder: rec}
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
	d := &Dispatcher{Deliverer: signalerDeliverer{sig}, Recorder: rec}
	d.Dispatch(context.Background(), TickContext{Now: now, Tree: tree, Watermarks: wmStub{}}, store)
	if len(rec.attemptOps) != 0 {
		t.Errorf("recorder.attemptOps = %v, want empty (window_reset is not a disrupt)", rec.attemptOps)
	}
}

// TestDispatcherDeliverer_NilErrRecordsSent verifies that a nil error from
// Deliverer.Deliver takes the existing sent path (RecordSent + intent
// cleared), exercising the Deliverer field directly (not via the
// signalerDeliverer/fakeSignaler adapter used by the pre-existing tests
// above).
func TestDispatcherDeliverer_NilErrRecordsSent(t *testing.T) {
	store := NewPendingStore()
	now := time.Now()
	store.Add(NudgeIntent{Key: IntentKey{"sid-1", SourceManual}, Text: "x", EmittedAt: now})
	tree := treeWith(time.Time{}, newSV("sid-1", 1234, session.Idle))
	del := &fakeDeliverer{}
	rec := &fakeRecorder{}
	d := &Dispatcher{Deliverer: del, Recorder: rec}
	d.Dispatch(context.Background(), TickContext{Now: now, Tree: tree, Watermarks: wmStub{}}, store)

	if len(rec.sent) != 1 || rec.sent[0] != "sid-1" {
		t.Errorf("recorder.sent = %v, want [sid-1]", rec.sent)
	}
	if store.HasAny("sid-1") {
		t.Error("store not cleared after successful delivery")
	}
	if del.pid != 1234 || del.text != "x" {
		t.Errorf("deliverer got pid=%d text=%q, want pid=1234 text=x", del.pid, del.text)
	}
}

// TestDispatcherDeliverer_NoBridgeFreshIntentRetained verifies that
// ErrNoBridge with an EmittedAt inside noBridgeDropWindow leaves the intent
// queued for a retry next tick: no RecordSent, no RecordDroppedNoBridge, no
// RemoveKeys.
func TestDispatcherDeliverer_NoBridgeFreshIntentRetained(t *testing.T) {
	store := NewPendingStore()
	now := time.Now()
	store.Add(NudgeIntent{Key: IntentKey{"sid-1", SourceManual}, Text: "x", EmittedAt: now})
	tree := treeWith(time.Time{}, newSV("sid-1", 1234, session.Idle))
	del := &fakeDeliverer{err: ErrNoBridge}
	rec := &fakeRecorder{}
	d := &Dispatcher{Deliverer: del, Recorder: rec}
	d.Dispatch(context.Background(), TickContext{Now: now, Tree: tree, Watermarks: wmStub{}}, store)

	if !store.HasAny("sid-1") {
		t.Error("intent removed despite fresh no-bridge error; should retry next tick")
	}
	if len(rec.sent) != 0 {
		t.Errorf("RecordSent called %d times, want 0", len(rec.sent))
	}
	if len(rec.droppedNoBridge) != 0 {
		t.Errorf("RecordDroppedNoBridge called %d times, want 0 (within noBridgeDropWindow)", len(rec.droppedNoBridge))
	}
}

// TestDispatcherDeliverer_NoBridgeStaleIntentDropped verifies that
// ErrNoBridge with an EmittedAt older than noBridgeDropWindow drops the
// intent: keys removed, RecordDroppedNoBridge called, and a "dropped"
// nudge_history row persisted with ErrorText="no_bridge".
func TestDispatcherDeliverer_NoBridgeStaleIntentDropped(t *testing.T) {
	store := NewPendingStore()
	now := time.Now()
	old := now.Add(-noBridgeDropWindow - time.Second)
	store.Add(NudgeIntent{Key: IntentKey{"sid-1", SourceManual}, Text: "x", EmittedAt: old})
	tree := treeWith(time.Time{}, newSV("sid-1", 1234, session.Idle))
	del := &fakeDeliverer{err: ErrNoBridge}
	rec := &fakeRecorder{}
	nudgeRec := &fakeNudgeRecorder{}
	d := &Dispatcher{Deliverer: del, Recorder: rec, NudgeRecorder: nudgeRec}
	d.Dispatch(context.Background(), TickContext{Now: now, Tree: tree, Watermarks: wmStub{}}, store)

	if store.HasAny("sid-1") {
		t.Error("stale no-bridge intent not dropped")
	}
	if len(rec.droppedNoBridge) != 1 || rec.droppedNoBridge[0].sid != "sid-1" {
		t.Fatalf("recorder.droppedNoBridge = %+v, want one entry for sid-1", rec.droppedNoBridge)
	}

	nudgeRec.mu.Lock()
	events := nudgeRec.events
	nudgeRec.mu.Unlock()
	if len(events) != 1 {
		t.Fatalf("NudgeRecorder.Record called %d times, want 1 (dropped persisted)", len(events))
	}
	if events[0].Result != "dropped" {
		t.Errorf("event.Result = %q, want dropped", events[0].Result)
	}
	if events[0].ErrorText != "no_bridge" {
		t.Errorf("event.ErrorText = %q, want no_bridge", events[0].ErrorText)
	}
}

// TestDispatcherDeliverer_GenericErrorRecordsFailure verifies that a non-nil,
// non-ErrNoBridge error from Deliverer.Deliver still takes the existing
// failed path: RecordSendFailed + a "failed" nudge_history row + the intent
// left queued to retry.
func TestDispatcherDeliverer_GenericErrorRecordsFailure(t *testing.T) {
	store := NewPendingStore()
	now := time.Now()
	store.Add(NudgeIntent{Key: IntentKey{"sid-1", SourceManual}, Text: "x", EmittedAt: now})
	tree := treeWith(time.Time{}, newSV("sid-1", 1234, session.Idle))
	del := &fakeDeliverer{err: errors.New("boom")}
	rec := &fakeRecorder{}
	nudgeRec := &fakeNudgeRecorder{}
	d := &Dispatcher{Deliverer: del, Recorder: rec, NudgeRecorder: nudgeRec}
	d.Dispatch(context.Background(), TickContext{Now: now, Tree: tree, Watermarks: wmStub{}}, store)

	if !store.HasAny("sid-1") {
		t.Error("intent removed after generic delivery failure; should retry next tick")
	}
	if len(rec.sendFailed) != 1 {
		t.Fatalf("RecordSendFailed called %d times, want 1", len(rec.sendFailed))
	}
	if len(rec.droppedNoBridge) != 0 {
		t.Errorf("RecordDroppedNoBridge called %d times, want 0 (not a no-bridge error)", len(rec.droppedNoBridge))
	}

	nudgeRec.mu.Lock()
	events := nudgeRec.events
	nudgeRec.mu.Unlock()
	if len(events) != 1 || events[0].Result != "failed" {
		t.Fatalf("events = %+v, want one failed event", events)
	}
}

// TestDispatcherLimitPauseLatchAdvancesOnDeliverFailure is the churn-regression
// test (bead pg2-2z7k). A lone qualifying limit_pause session whose Deliver
// returns a generic (non-ErrNoBridge) error MUST still advance the limit-pause
// latch to FiveHourResetsAt — the attempt counts even on failure. Otherwise a
// persistently-failing delivery would re-nudge every tick forever (unlike
// window-reset, FiveHourResetsAt never stale-zeroes to release the latch). The
// follow-up producer reconcile confirms no unbounded re-queue: once the latch
// equals reset, the producer cancels the stale intent.
func TestDispatcherLimitPauseLatchAdvancesOnDeliverFailure(t *testing.T) {
	reset := time.Date(2026, 7, 8, 17, 0, 0, 0, time.UTC)
	now := reset.Add(-time.Hour)
	store := NewPendingStore()
	store.Add(NudgeIntent{Key: IntentKey{"sid-lp", SourceLimitPause}, Text: "continue", EmittedAt: now})
	tree := treeWithFiveHour(reset, newSV("sid-lp", 1234, session.Idle))
	del := &fakeDeliverer{err: errors.New("boom")} // non-ErrNoBridge failure
	rec := &fakeRecorder{}
	d := &Dispatcher{Deliverer: del, Recorder: rec}
	d.Dispatch(context.Background(), TickContext{Now: now, Tree: tree, Watermarks: wmStub{}}, store)

	rec.mu.Lock()
	ops := rec.limitLatchOps
	rec.mu.Unlock()
	if len(ops) != 1 || !ops[0].Equal(reset) {
		t.Fatalf("limitLatchOps = %v, want [%v] (advance on ATTEMPT even on failure)", ops, reset)
	}
	// A failed delivery leaves the intent queued for this tick.
	if !store.HasAny("sid-lp") {
		t.Error("failed delivery should leave the intent queued")
	}

	// Next reconcile: latch now equals reset, so the producer must cancelAll —
	// the queued intent is removed, proving no unbounded re-queue.
	p := &LimitPauseProducer{}
	p.Reconcile(TickContext{
		Now: now, AutoResumeEnabled: true, AutoResumeMessage: "continue",
		Tree:       treeWithFiveHour(reset, qualifyingLimitPauseSV("sid-lp", 1234)),
		Watermarks: wmStub{lp: reset},
	}, store)
	if store.HasAny("sid-lp") {
		t.Error("producer should cancel the stale limit_pause intent once latch == reset (churn regression)")
	}
}

// TestDispatcherLimitPauseLatchAdvancesOnSuccess verifies a successful
// limit_pause delivery also advances the latch and clears the intent.
func TestDispatcherLimitPauseLatchAdvancesOnSuccess(t *testing.T) {
	reset := time.Date(2026, 7, 8, 17, 0, 0, 0, time.UTC)
	now := reset.Add(-time.Hour)
	store := NewPendingStore()
	store.Add(NudgeIntent{Key: IntentKey{"sid-lp", SourceLimitPause}, Text: "continue", EmittedAt: now})
	tree := treeWithFiveHour(reset, newSV("sid-lp", 1234, session.Idle))
	sig := &fakeSignaler{}
	rec := &fakeRecorder{}
	d := &Dispatcher{Deliverer: signalerDeliverer{sig}, Recorder: rec}
	d.Dispatch(context.Background(), TickContext{Now: now, Tree: tree, Watermarks: wmStub{}}, store)

	if len(sig.sent) != 1 {
		t.Fatalf("len(sent) = %d, want 1", len(sig.sent))
	}
	rec.mu.Lock()
	ops := rec.limitLatchOps
	rec.mu.Unlock()
	if len(ops) != 1 || !ops[0].Equal(reset) {
		t.Fatalf("limitLatchOps = %v, want [%v]", ops, reset)
	}
	if store.HasAny("sid-lp") {
		t.Error("store not cleared after successful limit_pause delivery")
	}
}

// TestDispatcherLimitPauseLatchAdvancesOnNoBridge documents the deliberate
// choice (bead pg2-2z7k) that ErrNoBridge also counts as an attempt: even with
// the intent still inside the 60s no-bridge retry window, the limit-pause latch
// advances, so the session consumes its 5h window on the first attempt rather
// than leaning on the reconnect grace. A transient bridge blip therefore costs
// at most one window's probe (immaterial for a monthly cap) and avoids re-arm
// churn. Contrast disrupt/window-reset, which lean on the retry window.
func TestDispatcherLimitPauseLatchAdvancesOnNoBridge(t *testing.T) {
	reset := time.Date(2026, 7, 8, 17, 0, 0, 0, time.UTC)
	now := reset.Add(-time.Hour)
	store := NewPendingStore()
	// EmittedAt = now: well inside noBridgeDropWindow, so the intent is retained
	// (not dropped) by the no-bridge path — yet the latch must still advance.
	store.Add(NudgeIntent{Key: IntentKey{"sid-lp", SourceLimitPause}, Text: "continue", EmittedAt: now})
	tree := treeWithFiveHour(reset, newSV("sid-lp", 1234, session.Idle))
	del := &fakeDeliverer{err: ErrNoBridge}
	rec := &fakeRecorder{}
	d := &Dispatcher{Deliverer: del, Recorder: rec}
	d.Dispatch(context.Background(), TickContext{Now: now, Tree: tree, Watermarks: wmStub{}}, store)

	rec.mu.Lock()
	ops := rec.limitLatchOps
	rec.mu.Unlock()
	if len(ops) != 1 || !ops[0].Equal(reset) {
		t.Fatalf("limitLatchOps = %v, want [%v] (ErrNoBridge still advances the latch)", ops, reset)
	}
}

// TestDispatcherLimitPauseLatchNotAdvancedWithoutLimitPause is the negative
// case: a tick that dispatches only non-limit_pause intents must not advance the
// limit-pause latch, even when FiveHourResetsAt is set.
func TestDispatcherLimitPauseLatchNotAdvancedWithoutLimitPause(t *testing.T) {
	reset := time.Date(2026, 7, 8, 17, 0, 0, 0, time.UTC)
	now := reset.Add(-time.Hour)
	store := NewPendingStore()
	store.Add(NudgeIntent{Key: IntentKey{"sid-1", SourceManual}, Text: "continue", EmittedAt: now})
	tree := treeWithFiveHour(reset, newSV("sid-1", 1234, session.Idle))
	sig := &fakeSignaler{}
	rec := &fakeRecorder{}
	d := &Dispatcher{Deliverer: signalerDeliverer{sig}, Recorder: rec}
	d.Dispatch(context.Background(), TickContext{Now: now, Tree: tree, Watermarks: wmStub{}}, store)

	rec.mu.Lock()
	ops := rec.limitLatchOps
	rec.mu.Unlock()
	if len(ops) != 0 {
		t.Fatalf("limitLatchOps = %v, want [] (no SourceLimitPause intent this tick)", ops)
	}
}

// TestDispatcherMixedWindowResetAndLimitPauseCoalesce verifies that when a
// single session carries BOTH a SourceWindowReset and a SourceLimitPause intent
// they coalesce into one Send, and both latches advance independently to their
// own reset times.
func TestDispatcherMixedWindowResetAndLimitPauseCoalesce(t *testing.T) {
	windowReset := time.Date(2026, 7, 8, 16, 0, 0, 0, time.UTC)
	fiveHour := time.Date(2026, 7, 8, 17, 0, 0, 0, time.UTC)
	now := windowReset.Add(-time.Minute)
	store := NewPendingStore()
	store.Add(NudgeIntent{Key: IntentKey{"sid-1", SourceWindowReset}, Text: "continue", EmittedAt: now})
	store.Add(NudgeIntent{Key: IntentKey{"sid-1", SourceLimitPause}, Text: "continue", EmittedAt: now})
	tree := &aggregate.Tree{WindowResetsAt: windowReset, FiveHourResetsAt: fiveHour}
	tree.Dirs = []*aggregate.Directory{{Sessions: []*aggregate.SessionView{newSV("sid-1", 1234, session.Idle)}}}
	sig := &fakeSignaler{}
	rec := &fakeRecorder{}
	d := &Dispatcher{Deliverer: signalerDeliverer{sig}, Recorder: rec}
	d.Dispatch(context.Background(), TickContext{Now: now, Tree: tree, Watermarks: wmStub{}}, store)

	if len(sig.sent) != 1 {
		t.Fatalf("len(sent) = %d, want 1 (coalesced single Send)", len(sig.sent))
	}
	rec.mu.Lock()
	wrOps := rec.windowLatchOps
	lpOps := rec.limitLatchOps
	rec.mu.Unlock()
	if len(wrOps) != 1 || !wrOps[0].Equal(windowReset) {
		t.Errorf("windowLatchOps = %v, want [%v]", wrOps, windowReset)
	}
	if len(lpOps) != 1 || !lpOps[0].Equal(fiveHour) {
		t.Errorf("limitLatchOps = %v, want [%v]", lpOps, fiveHour)
	}
	if store.HasAny("sid-1") {
		t.Error("store not cleared after coalesced dispatch")
	}
}
