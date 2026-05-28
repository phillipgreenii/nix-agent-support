package nudger

import (
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
	n := New(sig, rec)

	// Tick 1: first sighting at now-31s; grace not yet elapsed against firstSeen=now.
	n.Tick(TickContext{
		Now: now.Add(-31 * time.Second), AutoResumeEnabled: true,
		AutoResumeMessage: "continue", DisruptGrace: 30 * time.Second,
		EscalationAfter: 60 * time.Second, Tree: tree, Watermarks: wmStub{},
	})
	if len(sig.sent) != 0 {
		t.Errorf("tick 1: sent = %d, want 0 (grace not elapsed)", len(sig.sent))
	}

	// Tick 2: 31s later — grace elapsed; dispatcher fires.
	n.Tick(TickContext{
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
	n := New(sig, rec)
	n.QueueManual([]string{"sid-1"}, "manual!", now)
	n.Tick(TickContext{
		Now: now, AutoResumeEnabled: false, // disabled disables auto producers
		Tree: tree, Watermarks: wmStub{},
	})
	if len(sig.sent) != 1 || sig.sent[0].Text != "manual!" {
		t.Errorf("sent = %+v, want manual fire even with auto disabled", sig.sent)
	}
}

func TestNudgerCancelManual(t *testing.T) {
	now := time.Now()
	n := New(&fakeSignaler{}, &fakeRecorder{})
	n.QueueManual([]string{"sid-1"}, "x", now)
	if !n.PendingFor("sid-1") {
		t.Error("expected pending after QueueManual")
	}
	n.CancelManual([]string{"sid-1"})
	if n.PendingFor("sid-1") {
		t.Error("expected no pending after CancelManual")
	}
}
