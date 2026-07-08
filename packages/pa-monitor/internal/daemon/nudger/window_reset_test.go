package nudger

import (
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/aggregate"
	"github.com/phillipgreenii/pa-monitor/internal/core/session"
)

type wmStub struct {
	wr        time.Time
	lp        time.Time // LimitPauseFiredFor latch
	per       map[string]SessionWatermark
	escalated map[string]bool // tracks SetDisruptEscalated calls (for assertions)
}

func (w wmStub) WindowResetFiredFor() time.Time { return w.wr }
func (w wmStub) LimitPauseFiredFor() time.Time  { return w.lp }
func (w wmStub) SessionWatermark(sid string) SessionWatermark {
	return w.per[sid]
}

func (w wmStub) SetDisruptEscalated(sid string, escalated bool) {
	if w.escalated != nil {
		w.escalated[sid] = escalated
	}
}

func treeWith(windowResetsAt time.Time, sessions ...*aggregate.SessionView) *aggregate.Tree {
	t := &aggregate.Tree{WindowResetsAt: windowResetsAt}
	t.Dirs = []*aggregate.Directory{{Sessions: sessions}}
	return t
}

func newSV(sid string, pid int, st session.Status) *aggregate.SessionView {
	return &aggregate.SessionView{
		Session: &session.Session{SessionID: sid, PID: pid, Status: st},
	}
}

func TestWindowResetProducerNoOpWhenZero(t *testing.T) {
	p := &WindowResetProducer{}
	store := NewPendingStore()
	p.Reconcile(TickContext{
		Now: time.Now(), AutoResumeEnabled: true,
		Tree:       &aggregate.Tree{},
		Watermarks: wmStub{},
	}, store)
	if got := len(store.List()); got != 0 {
		t.Errorf("len(intents) = %d, want 0 (no window)", got)
	}
}

func TestWindowResetProducerNoOpWhenDisabled(t *testing.T) {
	now := time.Date(2026, 5, 28, 15, 0, 0, 0, time.UTC)
	resetsAt := now.Add(-1 * time.Minute)
	p := &WindowResetProducer{}
	store := NewPendingStore()
	tree := treeWith(resetsAt, newSV("sid-1", 1, session.Idle))
	p.Reconcile(TickContext{
		Now: now, AutoResumeEnabled: false, AutoResumeDelay: 30 * time.Second,
		Tree: tree, Watermarks: wmStub{},
	}, store)
	if got := len(store.List()); got != 0 {
		t.Errorf("len(intents) = %d, want 0 (disabled)", got)
	}
}

func TestWindowResetProducerFiresAfterDelay(t *testing.T) {
	now := time.Date(2026, 5, 28, 15, 0, 30, 0, time.UTC)
	resetsAt := now.Add(-31 * time.Second) // delay elapsed (30s)
	p := &WindowResetProducer{}
	store := NewPendingStore()
	tree := treeWith(
		resetsAt,
		newSV("idle-1", 1, session.Idle),
		newSV("work-1", 2, session.Working),
		newSV("dorm-1", 3, session.Dormant),
	)
	p.Reconcile(TickContext{
		Now: now, AutoResumeEnabled: true, AutoResumeDelay: 30 * time.Second,
		AutoResumeMessage: "continue",
		Tree:              tree, Watermarks: wmStub{},
	}, store)
	got := store.List()
	if len(got) != 2 {
		t.Fatalf("len(intents) = %d, want 2 (idle + dormant; working skipped)", len(got))
	}
	for _, in := range got {
		if in.Key.Source != SourceWindowReset {
			t.Errorf("intent source = %q, want window_reset", in.Key.Source)
		}
		if in.Text != "continue" {
			t.Errorf("intent text = %q, want continue", in.Text)
		}
		if in.Key.SessionID == "work-1" {
			t.Errorf("Working session should be skipped at queue time")
		}
	}
}

func TestWindowResetProducerSkipsIfAlreadyFired(t *testing.T) {
	now := time.Date(2026, 5, 28, 15, 0, 30, 0, time.UTC)
	resetsAt := now.Add(-31 * time.Second)
	p := &WindowResetProducer{}
	store := NewPendingStore()
	tree := treeWith(resetsAt, newSV("idle-1", 1, session.Idle))
	p.Reconcile(TickContext{
		Now: now, AutoResumeEnabled: true, AutoResumeDelay: 30 * time.Second,
		AutoResumeMessage: "continue",
		Tree:              tree,
		Watermarks:        wmStub{wr: resetsAt}, // already fired for this window
	}, store)
	if got := len(store.List()); got != 0 {
		t.Errorf("len(intents) = %d, want 0 (already fired)", got)
	}
}

func TestWindowResetProducerCancelsWhenWindowClears(t *testing.T) {
	p := &WindowResetProducer{}
	store := NewPendingStore()
	store.Add(NudgeIntent{Key: IntentKey{"idle-1", SourceWindowReset}, EmittedAt: time.Now()})
	tree := &aggregate.Tree{} // WindowResetsAt zero
	tree.Dirs = []*aggregate.Directory{{Sessions: []*aggregate.SessionView{
		newSV("idle-1", 1, session.Idle),
	}}}
	p.Reconcile(TickContext{
		Now: time.Now(), AutoResumeEnabled: true, AutoResumeDelay: 30 * time.Second,
		Tree: tree, Watermarks: wmStub{},
	}, store)
	if store.HasAny("idle-1") {
		t.Error("intent not cancelled when window cleared")
	}
}
