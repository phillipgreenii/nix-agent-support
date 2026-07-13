// Package timing centralizes the connection-health durations shared by the
// daemon, the cmux-bridge, and the TUI.
//
// The daemon pushes a DaemonState snapshot to each bridge every
// SnapshotInterval; the bridge's receive-loop watchdog declares the connection
// lost if it hears nothing for PushBudget; the bridge refreshes its liveness
// every HeartbeatInterval; the daemon reaps a bridge whose heartbeat has not
// arrived within StaleAfter. These four values are coupled: if PushBudget is
// not comfortably larger than SnapshotInterval, a single dropped snapshot trips
// a spurious disconnect; if StaleAfter is not several HeartbeatIntervals, a
// healthy bridge is reaped.
//
// To make an inconsistent set unrepresentable, only the two base cadences
// (SnapshotInterval, HeartbeatInterval) are configurable; PushBudget and
// StaleAfter are DERIVED from them. The daemon and each bridge derive their own
// values from the same base config, so two processes reading the same config
// cannot drift into an inversion. Callers therefore never hand-pick a budget or
// stale window — they read it from Derive.
package timing

import "time"

// Defaults for the base cadences. A zero Config selects these.
const (
	DefaultSnapshotInterval  = 2 * time.Second
	DefaultHeartbeatInterval = 10 * time.Second
)

// Minimums the base cadences are clamped to. They bound how aggressive an
// operator (or a corrupt config) can make the cadences: below these the derived
// budgets shrink toward zero and the watchdog/reaper misbehave.
const (
	MinSnapshotInterval  = 250 * time.Millisecond
	MinHeartbeatInterval = 1 * time.Second
)

// Derivation factors, expressed as integer numerator/denominator pairs so the
// arithmetic is exact (no float rounding) for any base duration.
//
//	PushBudget = SnapshotInterval  * 5/2  (2.5x  -> strictly > 2x, so one dropped
//	                                        snapshot cannot trip the watchdog)
//	StaleAfter = HeartbeatInterval * 7/2  (3.5x  -> >= 3x, so at least three
//	                                        heartbeats fit before a bridge is stale)
const (
	pushBudgetNum, pushBudgetDen = 5, 2
	staleAfterNum, staleAfterDen = 7, 2
)

// Config holds the base, independently-configurable connection-timing knobs.
// Every other duration is derived from these. Zero values select the defaults.
type Config struct {
	SnapshotInterval  time.Duration
	HeartbeatInterval time.Duration
}

// Derived holds every connection-timing duration used by the daemon, the
// cmux-bridge, and the TUI, computed so the ordering invariants hold by
// construction. Consume these; do not re-derive or hand-pick them elsewhere.
type Derived struct {
	// SnapshotInterval is the daemon's per-bridge snapshot push cadence.
	SnapshotInterval time.Duration
	// HeartbeatInterval is how often a bridge refreshes its liveness.
	HeartbeatInterval time.Duration
	// PushBudget is the bridge receive-loop watchdog: no message within this
	// window means the connection is treated as lost.
	PushBudget time.Duration
	// StaleAfter is the daemon-side cutoff beyond which a bridge that has not
	// heartbeated is considered disconnected.
	StaleAfter time.Duration
}

// Derive resolves a base Config (applying defaults and clamping to the
// documented minimums) and computes the dependent durations. It is total: every
// input, including zero and negative durations, yields a Derived whose ordering
// invariants hold.
func Derive(c Config) Derived {
	snap := clamp(c.SnapshotInterval, DefaultSnapshotInterval, MinSnapshotInterval)
	hb := clamp(c.HeartbeatInterval, DefaultHeartbeatInterval, MinHeartbeatInterval)
	return Derived{
		SnapshotInterval:  snap,
		HeartbeatInterval: hb,
		PushBudget:        snap * pushBudgetNum / pushBudgetDen,
		StaleAfter:        hb * staleAfterNum / staleAfterDen,
	}
}

// clamp returns d when it is at least min; a non-positive d selects def; any
// other sub-minimum value is raised to min.
func clamp(d, def, min time.Duration) time.Duration {
	if d <= 0 {
		d = def
	}
	if d < min {
		d = min
	}
	return d
}
