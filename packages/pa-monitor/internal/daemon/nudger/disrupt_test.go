package nudger

import (
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/aggregate"
	"github.com/phillipgreenii/pa-monitor/internal/core/session"
	"github.com/phillipgreenii/pa-monitor/internal/core/transcript"
)

func sessionWithError(sid string, kind transcript.ErrorKind, at time.Time, terminal bool) *aggregate.SessionView {
	// Use a connection-drop text so an `unknown` kind classifies as a transient
	// network drop (ClassTransientNetwork) — the disrupt producer's auto-resume
	// case. A server_error is transient regardless of text.
	rec := &transcript.ErrorRecord{
		Kind: kind, Text: "API Error: The socket connection was closed unexpectedly",
		At: at, IsTerminal: terminal,
	}
	return &aggregate.SessionView{
		Session: &session.Session{SessionID: sid, PID: 1, Status: session.Idle},
		SessionEnrichment: aggregate.SessionEnrichment{
			LastError:          rec,
			LastErrorRetryable: transcript.Retryable(rec),
		},
	}
}

func TestDisruptProducerSkipsWhenNotTerminal(t *testing.T) {
	now := time.Date(2026, 5, 28, 15, 0, 0, 0, time.UTC)
	p := NewDisruptProducer()
	store := NewPendingStore()
	tree := treeWith(
		time.Time{},
		sessionWithError("sid-1", transcript.ErrUnknown, now.Add(-1*time.Minute), false /*not terminal*/),
	)
	p.Reconcile(TickContext{
		Now: now, AutoResumeEnabled: true, DisruptGrace: 30 * time.Second,
		Tree: tree, Watermarks: wmStub{},
	}, store)
	if store.HasAny("sid-1") {
		t.Error("intent queued for non-terminal error")
	}
}

func TestDisruptProducerGraceWindow(t *testing.T) {
	now := time.Date(2026, 5, 28, 15, 0, 0, 0, time.UTC)
	at := now.Add(-10 * time.Second) // 10s ago, grace is 30s
	p := NewDisruptProducer()
	store := NewPendingStore()
	tree := treeWith(time.Time{}, sessionWithError("sid-1", transcript.ErrUnknown, at, true))
	p.Reconcile(TickContext{
		Now: now, AutoResumeEnabled: true, DisruptGrace: 30 * time.Second,
		AutoResumeMessage: "continue", Tree: tree, Watermarks: wmStub{},
	}, store)
	if store.HasAny("sid-1") {
		t.Error("intent queued before grace elapsed")
	}
	// Advance now past grace; same LastError.At unchanged.
	p.Reconcile(TickContext{
		Now: now.Add(35 * time.Second), AutoResumeEnabled: true,
		DisruptGrace:      30 * time.Second,
		AutoResumeMessage: "continue", Tree: tree, Watermarks: wmStub{},
	}, store)
	if !store.HasAny("sid-1") {
		t.Error("intent not queued after grace elapsed")
	}
}

func TestDisruptProducerCancelsOnResume(t *testing.T) {
	now := time.Date(2026, 5, 28, 15, 0, 0, 0, time.UTC)
	p := NewDisruptProducer()
	store := NewPendingStore()
	store.Add(NudgeIntent{Key: IntentKey{"sid-1", SourceDisrupted}, EmittedAt: now})
	sv := &aggregate.SessionView{
		Session: &session.Session{SessionID: "sid-1", PID: 1, Status: session.Working},
	}
	tree := treeWith(time.Time{}, sv)
	p.Reconcile(TickContext{
		Now: now, AutoResumeEnabled: true, DisruptGrace: 30 * time.Second,
		Tree: tree, Watermarks: wmStub{},
	}, store)
	if store.HasAny("sid-1") {
		t.Error("intent not cancelled after session resumed (LastError nil)")
	}
}

func TestDisruptProducerCancelsOnNonRetryable(t *testing.T) {
	now := time.Date(2026, 5, 28, 15, 0, 0, 0, time.UTC)
	p := NewDisruptProducer()
	store := NewPendingStore()
	store.Add(NudgeIntent{Key: IntentKey{"sid-1", SourceDisrupted}, EmittedAt: now})
	tree := treeWith(
		time.Time{},
		sessionWithError("sid-1", transcript.ErrAuthFailed, now.Add(-1*time.Minute), true),
	)
	p.Reconcile(TickContext{
		Now: now, AutoResumeEnabled: true, DisruptGrace: 30 * time.Second,
		Tree: tree, Watermarks: wmStub{},
	}, store)
	if store.HasAny("sid-1") {
		t.Error("intent not cancelled for non-retryable error")
	}
}

func TestDisruptProducerEscalatesAfterNudgedAndStillStuck(t *testing.T) {
	now := time.Date(2026, 5, 28, 15, 0, 0, 0, time.UTC)
	errAt := now.Add(-2 * time.Minute)
	nudgedAt := now.Add(-65 * time.Second) // > escalation_after_s (60s)
	p := NewDisruptProducer()
	store := NewPendingStore()
	tree := treeWith(time.Time{}, sessionWithError("sid-1", transcript.ErrUnknown, errAt, true))
	watermarks := wmStub{per: map[string]SessionWatermark{
		"sid-1": {
			LastDisruptNudgeAt:  nudgedAt,
			LastDisruptNudgeFor: errAt, // same error
		},
	}}
	p.Reconcile(TickContext{
		Now: now, AutoResumeEnabled: true,
		DisruptGrace: 30 * time.Second, EscalationAfter: 60 * time.Second,
		AutoResumeMessage: "continue", Tree: tree, Watermarks: watermarks,
	}, store)
	if store.HasAny("sid-1") {
		t.Error("intent queued for escalated session (should be cancelled, no re-arm)")
	}
}

// wmTrackingStub is a wmStub variant that records SetDisruptEscalated calls
// so tests can assert that the flag was set.
type wmTrackingStub struct {
	wmStub
	escalateCalls []struct {
		sid       string
		escalated bool
	}
}

func newTrackingStub(per map[string]SessionWatermark) *wmTrackingStub {
	return &wmTrackingStub{wmStub: wmStub{per: per}}
}

func (w *wmTrackingStub) SetDisruptEscalated(sid string, escalated bool) {
	w.escalateCalls = append(w.escalateCalls, struct {
		sid       string
		escalated bool
	}{sid, escalated})
}

func TestDisruptProducerSetsEscalatedFlagWhenStuck(t *testing.T) {
	now := time.Date(2026, 5, 28, 15, 0, 0, 0, time.UTC)
	errAt := now.Add(-2 * time.Minute)
	nudgedAt := now.Add(-65 * time.Second) // > escalation_after_s (60s)
	p := NewDisruptProducer()
	store := NewPendingStore()
	tree := treeWith(time.Time{}, sessionWithError("sid-1", transcript.ErrUnknown, errAt, true))
	wm := newTrackingStub(map[string]SessionWatermark{
		"sid-1": {
			LastDisruptNudgeAt:  nudgedAt,
			LastDisruptNudgeFor: errAt, // same error, already nudged
		},
	})
	p.Reconcile(TickContext{
		Now: now, AutoResumeEnabled: true,
		DisruptGrace: 30 * time.Second, EscalationAfter: 60 * time.Second,
		AutoResumeMessage: "continue", Tree: tree, Watermarks: wm,
	}, store)

	if store.HasAny("sid-1") {
		t.Error("intent queued for escalated session (should be cancelled)")
	}
	if len(wm.escalateCalls) == 0 {
		t.Fatal("SetDisruptEscalated was never called — escalation flag not persisted")
	}
	call := wm.escalateCalls[len(wm.escalateCalls)-1]
	if call.sid != "sid-1" || !call.escalated {
		t.Errorf("SetDisruptEscalated(%q, %v), want (sid-1, true)", call.sid, call.escalated)
	}
}

func TestDisruptProducerClearsEscalatedFlagOnNewError(t *testing.T) {
	now := time.Date(2026, 5, 28, 15, 0, 0, 0, time.UTC)
	oldErrAt := now.Add(-3 * time.Minute)
	newErrAt := now.Add(-10 * time.Second)
	nudgedAt := now.Add(-2 * time.Minute)
	p := NewDisruptProducer()
	store := NewPendingStore()
	tree := treeWith(time.Time{}, sessionWithError("sid-1", transcript.ErrUnknown, newErrAt, true))
	wm := newTrackingStub(map[string]SessionWatermark{
		"sid-1": {
			LastDisruptNudgeAt:  nudgedAt,
			LastDisruptNudgeFor: oldErrAt,
			DisruptEscalated:    true, // was escalated on old error
		},
	})
	p.Reconcile(TickContext{
		Now: now, AutoResumeEnabled: true,
		DisruptGrace: 30 * time.Second, EscalationAfter: 60 * time.Second,
		AutoResumeMessage: "continue", Tree: tree, Watermarks: wm,
	}, store)

	if len(wm.escalateCalls) == 0 {
		t.Fatal("SetDisruptEscalated was never called — re-arm on new error broken")
	}
	call := wm.escalateCalls[len(wm.escalateCalls)-1]
	if call.sid != "sid-1" || call.escalated {
		t.Errorf("SetDisruptEscalated(%q, %v), want (sid-1, false) to re-arm", call.sid, call.escalated)
	}
}

func TestDisruptProducerCancelsOnSubagentError(t *testing.T) {
	// A terminal, retryable error whose firstSeen is already past grace and
	// whose watermark marks it not-new and recently-nudged (no escalation)
	// reaches the nudge Add path — so absent the FromSubagent guard a nudge
	// would be queued here. With FromSubagent=true the subagent guard must
	// suppress it (visibility only, no auto-nudge). This setup makes the test
	// fail if the guard is removed.
	now := time.Date(2026, 5, 28, 15, 0, 0, 0, time.UTC)
	errAt := now.Add(-2 * time.Minute)
	p := NewDisruptProducer()
	// Prime firstSeen so the grace window has already elapsed.
	p.NoteFirstSeen("sid-1", now.Add(-1*time.Minute))
	store := NewPendingStore()
	sv := sessionWithError("sid-1", transcript.ErrUnknown, errAt, true)
	sv.LastError.FromSubagent = true
	tree := treeWith(time.Time{}, sv)
	// Not a new error (LastDisruptNudgeFor == errAt) and nudged recently
	// (within EscalationAfter) so neither the new-error reset nor escalation
	// fires; the grace check passes and the producer would Add a nudge.
	watermarks := wmStub{per: map[string]SessionWatermark{
		"sid-1": {LastDisruptNudgeAt: now.Add(-5 * time.Second), LastDisruptNudgeFor: errAt},
	}}
	p.Reconcile(TickContext{
		Now: now, AutoResumeEnabled: true,
		DisruptGrace: 30 * time.Second, EscalationAfter: 60 * time.Second,
		AutoResumeMessage: "continue", Tree: tree, Watermarks: watermarks,
	}, store)
	if store.HasAny("sid-1") {
		t.Error("nudge queued for subagent error (FromSubagent=true must suppress the nudge)")
	}
}

func TestDisruptProducerNewErrorReArms(t *testing.T) {
	now := time.Date(2026, 5, 28, 15, 0, 0, 0, time.UTC)
	oldErrAt := now.Add(-3 * time.Minute)
	newErrAt := now.Add(-31 * time.Second) // 31s ago, past grace
	nudgedAt := now.Add(-2 * time.Minute)
	p := NewDisruptProducer()
	// Mark this session as previously seen with the old error (firstSeen).
	p.NoteFirstSeen("sid-1", oldErrAt)
	store := NewPendingStore()
	tree := treeWith(time.Time{}, sessionWithError("sid-1", transcript.ErrUnknown, newErrAt, true))
	watermarks := wmStub{per: map[string]SessionWatermark{
		"sid-1": {LastDisruptNudgeAt: nudgedAt, LastDisruptNudgeFor: oldErrAt},
	}}
	// First tick: producer sees new errAt > LastDisruptNudgeFor; resets firstSeen=now.
	p.Reconcile(TickContext{
		Now: now.Add(-31 * time.Second), AutoResumeEnabled: true,
		DisruptGrace: 30 * time.Second, EscalationAfter: 60 * time.Second,
		AutoResumeMessage: "continue", Tree: tree, Watermarks: watermarks,
	}, store)
	if store.HasAny("sid-1") {
		t.Error("intent queued on the same tick as first sighting (grace not elapsed)")
	}
	// Second tick, 31s later: grace elapsed against the firstSeen.
	p.Reconcile(TickContext{
		Now: now, AutoResumeEnabled: true,
		DisruptGrace: 30 * time.Second, EscalationAfter: 60 * time.Second,
		AutoResumeMessage: "continue", Tree: tree, Watermarks: watermarks,
	}, store)
	if !store.HasAny("sid-1") {
		t.Error("intent not queued after grace elapsed on fresh error")
	}
}
