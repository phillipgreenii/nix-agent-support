package caffeinate

import "github.com/phillipgreenii/pa-monitor/internal/core/power"

// BatteryPolicy is the low-battery rule for the keep-awake assertion (bead
// pg2-s86z9). While the Mac draws from its battery:
//
//   - at or below HoldPct no NEW caffeinate subprocess is started;
//   - at or below ReleasePct a RUNNING subprocess is stopped at the next Tick;
//   - the normal rule resumes once AC returns, or the charge rises above
//     HoldPct+HysteresisPct (so a charge hovering at the threshold does not
//     flap the assertion).
//
// A HoldPct of 0 (or less) disables the rule entirely; a ReleasePct of 0 (or
// less) never stops a running subprocess. ReleasePct above HoldPct behaves as
// HoldPct: the daemon's config loader clamps it, and Tick tolerates it.
type BatteryPolicy struct {
	HoldPct       int
	ReleasePct    int
	HysteresisPct int
}

// BatteryGate is the verdict of the low-battery rule for one Tick.
type BatteryGate int

const (
	// GateDisabled: no reader, or the policy is off. Today's behavior.
	GateDisabled BatteryGate = iota
	// GateOK: the rule does not restrict the assertion (on AC, or charged above
	// the hold threshold plus hysteresis).
	GateOK
	// GateNoSpawn: on battery at or below the hold threshold. A new subprocess
	// is not started; a running one continues until the release threshold.
	GateNoSpawn
	// GateReleased: on battery at or below the release threshold. A running
	// subprocess is stopped and none is started.
	GateReleased
	// GateUnknown: the battery read failed. The rule fails OPEN to today's
	// behavior, so an unreadable battery never releases the hold.
	GateUnknown
)

// String is the stable machine token used on the status wire and in logs.
func (g BatteryGate) String() string {
	switch g {
	case GateOK:
		return "ok"
	case GateNoSpawn:
		return "no_spawn"
	case GateReleased:
		return "released"
	case GateUnknown:
		return "unknown"
	default:
		return "disabled"
	}
}

// Restricts reports whether the gate withholds the assertion (no new spawn).
func (g BatteryGate) Restricts() bool { return g == GateNoSpawn || g == GateReleased }

// Reasons carried on the battery-release counter and its log event.
const (
	// ReasonReleaseThreshold: a running subprocess was stopped because the
	// charge fell to the release threshold.
	ReasonReleaseThreshold = "release_threshold"
	// ReasonSpawnBlocked: the Mac was due to be held awake but the low charge
	// withheld the subprocess. Counted once per episode, not once per Tick.
	ReasonSpawnBlocked = "spawn_blocked"
)

// BatteryStatus is the observable state of the rule, for status surfaces.
type BatteryStatus struct {
	Gate      BatteryGate
	OnBattery bool
	// Percent is the last charge read, or power.NoPercent when unknown.
	Percent int
}

// BatteryStatus returns the verdict of the most recent Tick.
func (m *Manager) BatteryStatus() BatteryStatus {
	return BatteryStatus{Gate: m.gate, OnBattery: m.lastReading.OnBattery, Percent: m.lastReading.Percent}
}

// TakeBatteryReleases returns, and clears, the reasons recorded since the last
// call: one entry per battery release. The daemon turns each into a counter
// increment, keeping the emitter out of the state machine.
func (m *Manager) TakeBatteryReleases() []string {
	r := m.releases
	m.releases = nil
	return r
}

// evalBattery reads the battery and updates m.gate / m.lastReading / the
// hold latch. It runs every Tick, before the toggle check, so the status is
// meaningful even while the user toggle is off.
func (m *Manager) evalBattery() BatteryGate {
	p := m.BatteryPolicy
	if m.Battery == nil || p.HoldPct <= 0 {
		m.latched = false
		m.lastReading = power.Reading{Percent: power.NoPercent}
		m.gate = GateDisabled
		return m.gate
	}
	r, err := m.Battery.Read()
	if err != nil || (r.OnBattery && r.Percent == power.NoPercent) {
		// Fail open: forget the latch so an unreadable battery cannot keep the
		// hold withheld, and never release on a guess.
		m.latched = false
		m.lastReading = power.Reading{Percent: power.NoPercent}
		m.gate = GateUnknown
		return m.gate
	}
	m.lastReading = r
	switch {
	case !r.OnBattery:
		m.latched = false
	case r.Percent <= p.HoldPct:
		m.latched = true
	case r.Percent > p.HoldPct+p.HysteresisPct:
		m.latched = false
	}
	release := min(p.ReleasePct, p.HoldPct)
	switch {
	case !m.latched:
		m.gate = GateOK
	case release > 0 && r.Percent <= release:
		m.gate = GateReleased
	default:
		m.gate = GateNoSpawn
	}
	return m.gate
}
