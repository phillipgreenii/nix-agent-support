package caffeinate

import "github.com/phillipgreenii/pa-monitor/internal/core/power"

// BatteryGate is the verdict of the low-battery rule for the keep-awake
// assertion. The rule itself (hold and release thresholds, hysteresis) now lives
// in pg-keep-awake; pa-monitor only reads the verdict back from
// `pg-keep-awake status --json` (battery.gate) and keeps the names and meaning
// they had when pa-monitor evaluated the rule itself (ADR 0095).
type BatteryGate int

const (
	// GateDisabled: the rule is off. Also the zero value.
	GateDisabled BatteryGate = iota
	// GateOK: the rule does not restrict the assertion (on AC, or charged above
	// the hold threshold plus hysteresis).
	GateOK
	// GateNoSpawn: on battery at or below the hold threshold. A new assertion is
	// not taken; a held one continues until the release threshold.
	GateNoSpawn
	// GateReleased: on battery at or below the release threshold. A held
	// assertion is dropped and none is taken.
	GateReleased
	// GateUnknown: the battery read failed, or the keeper's status is not known.
	// The rule fails OPEN, so an unreadable battery never releases the hold.
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

// ParseBatteryGate maps a keeper gate token (ok, no_spawn, released, unknown,
// disabled) to a BatteryGate. An unrecognised token is GateUnknown, so a newer
// keeper's new gate degrades to "unknown" instead of being misread.
func ParseBatteryGate(s string) BatteryGate {
	switch s {
	case "ok":
		return GateOK
	case "no_spawn":
		return GateNoSpawn
	case "released":
		return GateReleased
	case "disabled":
		return GateDisabled
	default:
		return GateUnknown
	}
}

// Restricts reports whether the gate withholds the assertion (no new hold).
func (g BatteryGate) Restricts() bool { return g == GateNoSpawn || g == GateReleased }

// BatteryStatus is the observable state of the rule, for status surfaces.
type BatteryStatus struct {
	Gate      BatteryGate
	OnBattery bool
	// Percent is the last charge read, or power.NoPercent when unknown.
	Percent int
}

// BatteryStatus returns the keeper's battery verdict from the most recent
// Tick. Before the first Tick, or when the keeper's status could not be read,
// the gate is GateUnknown and the percent is power.NoPercent.
func (m *Manager) BatteryStatus() BatteryStatus {
	if !m.obs.StatusKnown {
		return BatteryStatus{Gate: GateUnknown, Percent: power.NoPercent}
	}
	pct := m.obs.Percent
	if !m.obs.BatteryReadOK {
		pct = power.NoPercent
	}
	return BatteryStatus{Gate: m.obs.Gate, OnBattery: m.obs.OnBattery, Percent: pct}
}
