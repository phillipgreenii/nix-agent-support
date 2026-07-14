package timing_test

import (
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/timing"
)

// TestDeriveDefaults pins the derived durations for the zero Config so a change
// to a factor or default is a deliberate, reviewed edit.
func TestDeriveDefaults(t *testing.T) {
	d := timing.Derive(timing.Config{})
	if d.SnapshotInterval != 2*time.Second {
		t.Errorf("SnapshotInterval = %s, want 2s", d.SnapshotInterval)
	}
	if d.HeartbeatInterval != 10*time.Second {
		t.Errorf("HeartbeatInterval = %s, want 10s", d.HeartbeatInterval)
	}
	if d.PushBudget != 5*time.Second {
		t.Errorf("PushBudget = %s, want 5s (2.5 x snapshot)", d.PushBudget)
	}
	if d.StaleAfter != 35*time.Second {
		t.Errorf("StaleAfter = %s, want 35s (3.5 x heartbeat)", d.StaleAfter)
	}
}

// TestDeriveInvariantsHoldForAnyBase is the whole point of the package: no base
// configuration — however weird — can produce an inconsistent set. The two
// ordering invariants MUST hold for every input:
//
//	PushBudget  > 2 x SnapshotInterval   (one dropped snapshot cannot trip the watchdog)
//	StaleAfter >= 3 x HeartbeatInterval  (at least three heartbeats fit before stale)
func TestDeriveInvariantsHoldForAnyBase(t *testing.T) {
	bases := []timing.Config{
		{},
		{SnapshotInterval: 1 * time.Second, HeartbeatInterval: 3 * time.Second},
		{SnapshotInterval: 500 * time.Millisecond, HeartbeatInterval: 5 * time.Second},
		{SnapshotInterval: 10 * time.Second, HeartbeatInterval: 30 * time.Second},
		// Degenerate / hostile inputs that must be clamped, not trusted.
		{SnapshotInterval: 0, HeartbeatInterval: 0},
		{SnapshotInterval: -5 * time.Second, HeartbeatInterval: -1 * time.Second},
		{SnapshotInterval: 1 * time.Nanosecond, HeartbeatInterval: 1 * time.Nanosecond},
	}
	for _, c := range bases {
		d := timing.Derive(c)
		if d.PushBudget <= 2*d.SnapshotInterval {
			t.Errorf("base %+v: PushBudget %s !> 2 x SnapshotInterval %s", c, d.PushBudget, 2*d.SnapshotInterval)
		}
		if d.StaleAfter < 3*d.HeartbeatInterval {
			t.Errorf("base %+v: StaleAfter %s !>= 3 x HeartbeatInterval %s", c, d.StaleAfter, 3*d.HeartbeatInterval)
		}
	}
}

// TestDeriveClampsHostileBase ensures zero/negative base knobs fall back to the
// documented minimums rather than producing zero/negative derived durations
// (which would make the watchdog fire instantly or the reaper never run).
func TestDeriveClampsHostileBase(t *testing.T) {
	d := timing.Derive(timing.Config{SnapshotInterval: -1, HeartbeatInterval: 0})
	if d.SnapshotInterval < timing.MinSnapshotInterval {
		t.Errorf("SnapshotInterval = %s, want >= MinSnapshotInterval %s", d.SnapshotInterval, timing.MinSnapshotInterval)
	}
	if d.HeartbeatInterval < timing.MinHeartbeatInterval {
		t.Errorf("HeartbeatInterval = %s, want >= MinHeartbeatInterval %s", d.HeartbeatInterval, timing.MinHeartbeatInterval)
	}
	if d.PushBudget <= 0 || d.StaleAfter <= 0 {
		t.Errorf("derived durations must be positive, got PushBudget=%s StaleAfter=%s", d.PushBudget, d.StaleAfter)
	}
}
