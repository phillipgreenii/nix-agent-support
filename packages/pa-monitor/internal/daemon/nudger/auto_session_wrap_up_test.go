package nudger

import (
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/aggregate"
	"github.com/phillipgreenii/pa-monitor/internal/core/session"
)

// newIdleSV builds an Idle SessionView with the given TranscriptMTime (the
// idle-episode-start proxy — see AutoSessionWrapUpProducer.Reconcile's doc).
func newIdleSV(sid string, pid int, transcriptMTime time.Time) *aggregate.SessionView {
	return &aggregate.SessionView{
		Session: &session.Session{
			SessionID: sid, PID: pid, Status: session.Idle, TranscriptMTime: transcriptMTime,
		},
	}
}

const autoWrapThreshold = 45 * time.Minute

func autoWrapCtx(now time.Time, tree *aggregate.Tree, wm WatermarkView) TickContext {
	return TickContext{
		Now:                                now,
		Tree:                               tree,
		AutoSessionWrapUpEnabled:           true,
		AutoSessionWrapUpIdleThreshold:     autoWrapThreshold,
		AutoSessionWrapUpDirectiveTemplate: "idle %[1]dm",
		Watermarks:                         wm,
	}
}

func TestAutoSessionWrapUpProducerDisabledNoOp(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	p := &AutoSessionWrapUpProducer{}
	store := NewPendingStore()
	sv := newIdleSV("sid-1", 1001, now.Add(-2*time.Hour))
	tree := &aggregate.Tree{Dirs: []*aggregate.Directory{{Sessions: []*aggregate.SessionView{sv}}}}

	ctx := autoWrapCtx(now, tree, wmStub{})
	ctx.AutoSessionWrapUpEnabled = false
	p.Reconcile(ctx, store)

	if store.HasAny("sid-1") {
		t.Error("intent queued while AutoSessionWrapUpEnabled=false")
	}
}

// TestAutoSessionWrapUpProducerIdleThresholdBoundary covers verification item
// 1's first bullet: idle for exactly IdleThresholdMinutes fires; idle for
// threshold-minus-one-tick does not.
func TestAutoSessionWrapUpProducerIdleThresholdBoundary(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	p := &AutoSessionWrapUpProducer{}

	atThreshold := newIdleSV("at-threshold", 1001, now.Add(-autoWrapThreshold))
	belowThreshold := newIdleSV("below-threshold", 1002, now.Add(-autoWrapThreshold+time.Minute))
	tree := &aggregate.Tree{Dirs: []*aggregate.Directory{{Sessions: []*aggregate.SessionView{atThreshold, belowThreshold}}}}

	store := NewPendingStore()
	p.Reconcile(autoWrapCtx(now, tree, wmStub{autoWrapNudgedFor: map[string]time.Time{}}), store)

	if !store.HasAny("at-threshold") {
		t.Error("session idle for exactly the threshold was not enqueued")
	}
	if store.HasAny("below-threshold") {
		t.Error("session idle for threshold-minus-one-tick was enqueued; should not fire yet")
	}
}

// TestAutoSessionWrapUpProducerBlockedNeverFires covers verification item 1's
// second bullet: a Blocked session — including a permission-prompt-parked one
// — never fires, even past threshold; only Idle does.
func TestAutoSessionWrapUpProducerBlockedNeverFires(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	p := &AutoSessionWrapUpProducer{}

	blocked := &aggregate.SessionView{
		Session: &session.Session{
			SessionID: "blocked", PID: 1001, Status: session.Blocked, Blocker: session.HumanInput,
			TranscriptMTime: now.Add(-2 * time.Hour),
		},
	}
	tree := &aggregate.Tree{Dirs: []*aggregate.Directory{{Sessions: []*aggregate.SessionView{blocked}}}}

	store := NewPendingStore()
	p.Reconcile(autoWrapCtx(now, tree, wmStub{autoWrapNudgedFor: map[string]time.Time{}}), store)

	if store.HasAny("blocked") {
		t.Error("Blocked session (permission-prompt-parked) was enqueued; only Idle may fire")
	}
}

// TestAutoSessionWrapUpProducerUnsubmittedInputGuard covers verification item
// 1's unsubmitted-input-guard bullet: a non-empty captured pane input line
// suppresses send for that tick WITHOUT consuming the episode — a later
// empty-input tick still fires.
func TestAutoSessionWrapUpProducerUnsubmittedInputGuard(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	episodeStart := now.Add(-autoWrapThreshold)
	p := &AutoSessionWrapUpProducer{}
	wm := wmStub{autoWrapNudgedFor: map[string]time.Time{}}

	sv := newIdleSV("sid-1", 1001, episodeStart)
	tree := &aggregate.Tree{Dirs: []*aggregate.Directory{{Sessions: []*aggregate.SessionView{sv}}}}

	// Tick 1: composing (non-empty pane input) -> must NOT enqueue, must NOT
	// consume the episode watermark.
	store := NewPendingStore()
	ctx := autoWrapCtx(now, tree, wm)
	ctx.HasUnsubmittedInput = func(pid int) bool { return true }
	p.Reconcile(ctx, store)
	if store.HasAny("sid-1") {
		t.Fatal("intent enqueued despite unsubmitted (composing) input")
	}
	if _, wrote := wm.autoWrapNudgedFor["sid-1"]; wrote {
		t.Fatal("episode watermark consumed on a tick that was suppressed by the unsubmitted-input guard")
	}

	// Tick 2 (a moment later, input now clear): must fire.
	now2 := now.Add(5 * time.Second)
	ctx2 := autoWrapCtx(now2, tree, wm)
	ctx2.HasUnsubmittedInput = func(pid int) bool { return false }
	p.Reconcile(ctx2, store)
	if !store.HasAny("sid-1") {
		t.Error("intent not enqueued once unsubmitted input cleared")
	}
}

// TestAutoSessionWrapUpProducerDedupWithinEpisode covers verification item
// 1's dedup bullet: once a nudge has fired for an episode (simulated by
// removing the dispatched intent, as the real dispatcher would), a second
// Reconcile against the SAME episode must not re-add it.
func TestAutoSessionWrapUpProducerDedupWithinEpisode(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	episodeStart := now.Add(-autoWrapThreshold)
	p := &AutoSessionWrapUpProducer{}
	wm := wmStub{autoWrapNudgedFor: map[string]time.Time{}}

	sv := newIdleSV("sid-1", 1001, episodeStart)
	tree := &aggregate.Tree{Dirs: []*aggregate.Directory{{Sessions: []*aggregate.SessionView{sv}}}}
	store := NewPendingStore()

	p.Reconcile(autoWrapCtx(now, tree, wm), store)
	if !store.HasAny("sid-1") {
		t.Fatal("first Reconcile did not enqueue the nudge")
	}
	// Simulate the dispatcher having fired and cleared the intent.
	store.RemoveKeys([]IntentKey{{SessionID: "sid-1", Source: SourceAutoSessionWrapUp}})

	// Second Reconcile, same idle episode (episodeStart unchanged): must NOT
	// re-add, because the persisted watermark now covers this episode.
	p.Reconcile(autoWrapCtx(now.Add(time.Second), tree, wm), store)
	if store.HasAny("sid-1") {
		t.Error("nudge re-fired within the same idle episode; once-per-episode latch failed")
	}
}

// TestAutoSessionWrapUpProducerEpisodeReset covers verification item 1's
// episode-reset bullet: after the session leaves Idle and returns with a
// NEWER TranscriptMTime (a fresh idle episode), a nudge fires again even
// though a previous episode already consumed the latch.
func TestAutoSessionWrapUpProducerEpisodeReset(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	firstEpisode := now.Add(-2 * time.Hour)
	p := &AutoSessionWrapUpProducer{}
	wm := wmStub{autoWrapNudgedFor: map[string]time.Time{"sid-1": firstEpisode}}

	secondEpisode := now.Add(-autoWrapThreshold) // newer episode, past threshold
	sv := newIdleSV("sid-1", 1001, secondEpisode)
	tree := &aggregate.Tree{Dirs: []*aggregate.Directory{{Sessions: []*aggregate.SessionView{sv}}}}
	store := NewPendingStore()

	p.Reconcile(autoWrapCtx(now, tree, wm), store)
	if !store.HasAny("sid-1") {
		t.Error("nudge did not fire for a fresh idle episode newer than the previous one's watermark")
	}
}
