package nudger

import (
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/aggregate"
	"github.com/phillipgreenii/pa-monitor/internal/core/session"
	"github.com/phillipgreenii/pa-monitor/internal/core/transcript"
)

func sessionWithError(sid string, kind transcript.ErrorKind, at time.Time, terminal bool) *aggregate.SessionView {
	return &aggregate.SessionView{
		Session: &session.Session{SessionID: sid, PID: 1, Status: session.Idle},
		SessionEnrichment: aggregate.SessionEnrichment{
			LastError: &transcript.ErrorRecord{
				Kind: kind, Text: "API Error: ...",
				At: at, IsTerminal: terminal, IsRetryable: kind.IsRetryable(),
			},
		},
	}
}

func TestDisruptProducerSkipsWhenNotTerminal(t *testing.T) {
	now := time.Date(2026, 5, 28, 15, 0, 0, 0, time.UTC)
	p := NewDisruptProducer()
	store := NewPendingStore()
	tree := treeWith(time.Time{},
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
	tree := treeWith(time.Time{},
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
