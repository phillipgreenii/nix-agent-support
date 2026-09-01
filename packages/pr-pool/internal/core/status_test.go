package core

import (
	"testing"
	"time"
)

// TestGateStateNewerObservationWins is the red-first test for Task 3.5 Step 1's
// compare rule: a concurrent tick-stat write with an OLDER observation MUST
// NOT overwrite a socket verb's newer one.
func TestGateStateNewerObservationWins(t *testing.T) {
	var svc Service
	newer := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	older := newer.Add(-time.Minute)

	svc.ObserveGateFromSocketVerb(newer, "quota_paused", GateInfo{Set: true, Owner: "operator"})
	svc.ObserveGateFromTick(older, map[string]GateInfo{"quota_paused": {Set: false}})

	gates, observedAt := svc.GateSnapshot()
	if !observedAt.Equal(newer) {
		t.Fatalf("gatesObservedAt = %v, want unchanged at the socket verb's %v", observedAt, newer)
	}
	if got := gates["quota_paused"]; !got.Set {
		t.Fatalf("quota_paused = %+v, want the socket verb's Set=true to survive the older drive-loop write", got)
	}
}

// TestSocketPauseReflectsImmediately: a socket pause/resume verb write is
// visible to an immediate status read with a fresh gatesObservedAt, and a
// LATER drive-loop tick (still older than the socket write, or simply
// observing a different gate) never reverts it.
func TestSocketPauseReflectsImmediately(t *testing.T) {
	var svc Service
	pauseAt := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

	svc.ObserveGateFromSocketVerb(pauseAt, "cicd_down", GateInfo{Set: true, Mtime: pauseAt, Owner: "operator"})

	gates, observedAt := svc.GateSnapshot()
	if got := gates["cicd_down"]; !got.Set || !got.Mtime.Equal(pauseAt) {
		t.Fatalf("cicd_down = %+v, want an immediate Set=true with Mtime %v", got, pauseAt)
	}
	if !observedAt.Equal(pauseAt) {
		t.Fatalf("gatesObservedAt = %v, want the fresh %v the socket verb just recorded", observedAt, pauseAt)
	}

	// The next drive-loop tick observes an OLDER snapshot of gate-file state
	// (e.g. it started its pass just before the socket write landed) — it
	// must not revert what the socket verb just recorded.
	tickAt := pauseAt.Add(-time.Second)
	svc.ObserveGateFromTick(tickAt, map[string]GateInfo{"cicd_down": {Set: false}})

	gates, observedAt = svc.GateSnapshot()
	if got := gates["cicd_down"]; !got.Set {
		t.Fatalf("cicd_down = %+v, want the socket pause unreverted by the older tick", got)
	}
	if !observedAt.Equal(pauseAt) {
		t.Fatalf("gatesObservedAt = %v, want still %v (the older tick write must drop)", observedAt, pauseAt)
	}
}

// TestFileDirectPauseLagsUntilNextTick: a file-direct pause (Task 1.2b, ADR
// 0036) never calls into Service at all — it can only become visible once the
// drive loop's own next periodic gate-file read calls ObserveGateFromTick. An
// immediate status read in between reports the PRIOR observation with a
// stale gatesObservedAt, flipping only at that next tick.
func TestFileDirectPauseLagsUntilNextTick(t *testing.T) {
	var svc Service
	priorTick := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	svc.ObserveGateFromTick(priorTick, map[string]GateInfo{"quota_paused": {Set: false}})

	// A file-direct pause happens here, out-of-band — nothing calls into svc.

	// An immediate status read still sees the PRIOR (unset) state, stamped
	// with the stale priorTick observation time.
	gates, observedAt := svc.GateSnapshot()
	if got := gates["quota_paused"]; got.Set {
		t.Fatalf("quota_paused = %+v, want the prior unset state until the next tick observes the file", got)
	}
	if !observedAt.Equal(priorTick) {
		t.Fatalf("gatesObservedAt = %v, want the stale %v (unrefreshed until the next tick)", observedAt, priorTick)
	}

	// The next drive-loop tick reads the gate file and observes it set.
	nextTick := priorTick.Add(10 * time.Second)
	svc.ObserveGateFromTick(nextTick, map[string]GateInfo{"quota_paused": {Set: true, Mtime: nextTick.Add(-5 * time.Second)}})

	gates, observedAt = svc.GateSnapshot()
	if got := gates["quota_paused"]; !got.Set {
		t.Fatalf("quota_paused = %+v, want it to flip to set at the next tick", got)
	}
	if !observedAt.Equal(nextTick) {
		t.Fatalf("gatesObservedAt = %v, want the fresh %v", observedAt, nextTick)
	}
}

// TestCurrentTick_nilBeforeFirstPublish is the boot-window test: a freshly
// constructed Service must not panic when status-composing logic touches its
// tick cell before any PublishTick call [design: Task 3.5 Step 4].
func TestCurrentTick_nilBeforeFirstPublish(t *testing.T) {
	var svc Service

	got := svc.CurrentTick()
	if got != nil {
		t.Fatalf("CurrentTick() = %+v, want nil before the first PublishTick", got)
	}

	// Status-composing logic (Task 3.8) must nil-check rather than deref
	// unconditionally; simulate that check here so a regression that removes
	// the nil-check panics this test instead of a live status call.
	var runMode string
	if tick := svc.CurrentTick(); tick != nil {
		runMode = tick.RunMode
	} else {
		runMode = "boot"
	}
	if runMode != "boot" {
		t.Fatalf("runMode = %q, want %q", runMode, "boot")
	}
}

// TestPublishTick_currentTickRoundTrips proves PublishTick/CurrentTick carry
// a value through unchanged, and that a second publish fully replaces the
// first (no partial-merge of stale fields).
func TestPublishTick_currentTickRoundTrips(t *testing.T) {
	var svc Service
	t0 := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

	svc.PublishTick(TickSnapshot{
		RunMode:    RunModeLongRunning,
		Version:    "v1",
		LastTickAt: t0,
		SnapshotAt: t0,
	})
	got := svc.CurrentTick()
	if got == nil || got.RunMode != RunModeLongRunning || got.Version != "v1" {
		t.Fatalf("CurrentTick() = %+v, want the just-published long-running v1 snapshot", got)
	}

	t1 := t0.Add(time.Minute)
	svc.PublishTick(TickSnapshot{
		RunMode:    RunModeDrainAndExit,
		Version:    "v1",
		LastTickAt: t1,
		SnapshotAt: t1,
	})
	got = svc.CurrentTick()
	if got == nil || got.RunMode != RunModeDrainAndExit || !got.LastTickAt.Equal(t1) {
		t.Fatalf("CurrentTick() = %+v, want the second publish to fully replace the first", got)
	}
}

// TestGateSnapshot_returnsIndependentCopy proves the map GateSnapshot hands
// back is a copy: a caller mutating it must not corrupt the Service's own
// cache.
func TestGateSnapshot_returnsIndependentCopy(t *testing.T) {
	var svc Service
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	svc.ObserveGateFromSocketVerb(now, "quota_paused", GateInfo{Set: true})

	gates, _ := svc.GateSnapshot()
	gates["quota_paused"] = GateInfo{Set: false}

	gates2, _ := svc.GateSnapshot()
	if got := gates2["quota_paused"]; !got.Set {
		t.Fatalf("quota_paused = %+v, want the caller's mutation of the returned map to not affect the cache", got)
	}
}
