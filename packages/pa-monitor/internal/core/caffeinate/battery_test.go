package caffeinate

import (
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/power"
)

func TestParseBatteryGate(t *testing.T) {
	cases := []struct {
		in   string
		want BatteryGate
	}{
		{"ok", GateOK},
		{"no_spawn", GateNoSpawn},
		{"released", GateReleased},
		{"disabled", GateDisabled},
		{"unknown", GateUnknown},
		{"", GateUnknown},
		{"some_future_gate", GateUnknown},
	}
	for _, tc := range cases {
		if got := ParseBatteryGate(tc.in); got != tc.want {
			t.Errorf("ParseBatteryGate(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// TestBatteryGateTokensRoundTrip pins the wire tokens: every gate prints the
// token the keeper uses, and parsing it returns the same gate.
func TestBatteryGateTokensRoundTrip(t *testing.T) {
	for _, g := range []BatteryGate{GateDisabled, GateOK, GateNoSpawn, GateReleased, GateUnknown} {
		if got := ParseBatteryGate(g.String()); got != g {
			t.Errorf("ParseBatteryGate(%q) = %v, want %v", g.String(), got, g)
		}
	}
}

func TestBatteryGateRestricts(t *testing.T) {
	for g, want := range map[BatteryGate]bool{
		GateDisabled: false, GateOK: false, GateNoSpawn: true, GateReleased: true, GateUnknown: false,
	} {
		if got := g.Restricts(); got != want {
			t.Errorf("%v.Restricts() = %v, want %v", g, got, want)
		}
	}
}

// fakeKeeper returns canned observations; it is for the pure-logic tests of
// BatteryStatus, where no CLI is involved.
type fakeKeeper struct{ obs Observation }

func (f *fakeKeeper) Acquire(int) error    { return nil }
func (f *fakeKeeper) Release() error       { return nil }
func (f *fakeKeeper) Observe() Observation { return f.obs }

func TestBatteryStatusFromObservation(t *testing.T) {
	fk := &fakeKeeper{}
	m := &Manager{Keeper: fk, Grace: 5 * time.Second, Now: time.Now}

	// Before any Tick, and when the status cannot be read: unknown, no percent.
	if got := m.BatteryStatus(); got.Gate != GateUnknown || got.Percent != power.NoPercent {
		t.Errorf("before Tick: %+v, want unknown / NoPercent", got)
	}
	fk.obs = Observation{Health: HealthOK, StatusKnown: true, Gate: GateReleased, OnBattery: true, Percent: 15, BatteryReadOK: true}
	m.Tick(false)
	if got := m.BatteryStatus(); got.Gate != GateReleased || !got.OnBattery || got.Percent != 15 {
		t.Errorf("after Tick: %+v, want released/on-battery/15", got)
	}
	// A battery the keeper could not read has no percent.
	fk.obs = Observation{Health: HealthOK, StatusKnown: true, Gate: GateUnknown, Percent: 0, BatteryReadOK: false}
	m.Tick(false)
	if got := m.BatteryStatus(); got.Gate != GateUnknown || got.Percent != power.NoPercent {
		t.Errorf("unreadable battery: %+v, want unknown / NoPercent", got)
	}
}
