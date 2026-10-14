package caffeinate

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/power"
)

// batteryRig is a Manager wired to a FakeReader and counting Spawn/Kill, with
// the initial defaults (hold 30, release 20) and a 5-point hysteresis.
type batteryRig struct {
	m               *Manager
	bat             *power.FakeReader
	spawned, killed int
	alive           bool
}

func newBatteryRig(initial power.Reading) *batteryRig {
	r := &batteryRig{bat: power.NewFakeReader(initial)}
	r.m = &Manager{
		Grace:         5 * time.Second,
		Spawn:         func(int) error { r.spawned++; r.alive = true; return nil },
		Kill:          func() error { r.killed++; r.alive = false; return nil },
		IsAlive:       func() bool { return r.alive },
		Now:           func() time.Time { return time.Unix(0, 0) },
		Battery:       r.bat,
		BatteryPolicy: BatteryPolicy{HoldPct: 30, ReleasePct: 20, HysteresisPct: 5},
	}
	r.m.SetToggle(true)
	return r
}

func onBattery(pct int) power.Reading { return power.Reading{OnBattery: true, Percent: pct} }
func onAC(pct int) power.Reading      { return power.Reading{OnBattery: false, Percent: pct} }

func TestBattery_LowBatteryWithWorkingSessionDoesNotSpawn(t *testing.T) {
	r := newBatteryRig(onBattery(15))
	r.m.Tick(true) // a session is working
	if r.spawned != 0 {
		t.Errorf("Spawn called %d times on battery at 15%%, want 0", r.spawned)
	}
	if r.m.State() != StateOff {
		t.Errorf("State = %v, want Off", r.m.State())
	}
	if got := r.m.BatteryStatus(); got.Gate != GateReleased || !got.OnBattery || got.Percent != 15 {
		t.Errorf("BatteryStatus = %+v, want released/on-battery/15", got)
	}
	if got := r.m.TakeBatteryReleases(); !reflect.DeepEqual(got, []string{ReasonSpawnBlocked}) {
		t.Errorf("releases = %v, want [spawn_blocked]", got)
	}
}

func TestBattery_RunningSubprocessStoppedAtReleaseThreshold(t *testing.T) {
	r := newBatteryRig(onAC(80))
	r.m.Tick(true)
	if r.m.State() != StateArmedRunning || r.spawned != 1 {
		t.Fatalf("setup: state=%v spawned=%d, want ArmedRunning/1", r.m.State(), r.spawned)
	}
	r.bat.Set(onBattery(15))
	r.m.Tick(true) // work is still going on
	if r.killed != 1 {
		t.Errorf("Kill called %d times, want 1 (release at the next Tick)", r.killed)
	}
	if r.m.State() != StateOff {
		t.Errorf("State = %v, want Off after release", r.m.State())
	}
	if got := r.m.TakeBatteryReleases(); !reflect.DeepEqual(got, []string{ReasonReleaseThreshold, ReasonSpawnBlocked}) {
		// The release itself, then the same Tick's withheld respawn.
		t.Errorf("releases = %v, want [release_threshold spawn_blocked]", got)
	}
	r.m.Tick(true)
	if r.spawned != 1 {
		t.Errorf("respawned on battery at 15%%: spawned=%d, want still 1", r.spawned)
	}
}

func TestBattery_ReleaseInGraceCountdownToo(t *testing.T) {
	r := newBatteryRig(onAC(80))
	r.m.Tick(true)
	r.m.Tick(false) // grace begins
	if r.m.State() != StateArmedCountdown {
		t.Fatalf("setup: state=%v, want ArmedCountdown", r.m.State())
	}
	r.bat.Set(onBattery(10))
	r.m.Tick(false)
	if r.m.State() != StateOff || r.killed != 1 {
		t.Errorf("state=%v killed=%d, want Off/1", r.m.State(), r.killed)
	}
}

func TestBattery_OnACAtLowChargeKeepsHold(t *testing.T) {
	r := newBatteryRig(onAC(15))
	r.m.Tick(true)
	if r.m.State() != StateArmedRunning || r.spawned != 1 {
		t.Errorf("on AC at 15%%: state=%v spawned=%d, want ArmedRunning/1", r.m.State(), r.spawned)
	}
	r.m.Tick(true)
	if r.killed != 0 {
		t.Errorf("a running hold was released on AC at 15%%: killed=%d", r.killed)
	}
	if got := r.m.BatteryStatus().Gate; got != GateOK {
		t.Errorf("Gate = %v, want ok on AC", got)
	}
	if got := r.m.TakeBatteryReleases(); len(got) != 0 {
		t.Errorf("releases = %v, want none on AC", got)
	}
}

func TestBattery_HoldResumesAtNextTickAfterACReturns(t *testing.T) {
	r := newBatteryRig(onBattery(15))
	r.m.Tick(true)
	if r.spawned != 0 {
		t.Fatalf("setup: spawned=%d, want 0 while on battery", r.spawned)
	}
	r.bat.Set(onAC(15)) // charger attached; the charge is still low
	r.m.Tick(true)
	if r.m.State() != StateArmedRunning || r.spawned != 1 {
		t.Errorf("after AC returned: state=%v spawned=%d, want ArmedRunning/1", r.m.State(), r.spawned)
	}
}

func TestBattery_ReadErrorKeepsTodaysBehavior(t *testing.T) {
	r := newBatteryRig(onAC(80))
	r.bat.SetErr(errors.New("pmset exploded"))
	r.m.Tick(true)
	if r.m.State() != StateArmedRunning || r.spawned != 1 {
		t.Errorf("read error: state=%v spawned=%d, want today's ArmedRunning/1", r.m.State(), r.spawned)
	}
	if got := r.m.BatteryStatus().Gate; got != GateUnknown {
		t.Errorf("Gate = %v, want unknown", got)
	}
	r.m.Tick(true)
	if r.killed != 0 {
		t.Errorf("a read error released the hold: killed=%d", r.killed)
	}
	if got := r.m.TakeBatteryReleases(); len(got) != 0 {
		t.Errorf("releases = %v, want none on a read error", got)
	}
}

func TestBattery_ReadErrorAfterLowChargeFailsOpen(t *testing.T) {
	r := newBatteryRig(onBattery(15))
	r.m.Tick(true)
	if r.spawned != 0 {
		t.Fatalf("setup: spawned=%d, want 0", r.spawned)
	}
	r.bat.SetErr(errors.New("pmset exploded"))
	r.m.Tick(true)
	if r.spawned != 1 {
		t.Errorf("after a read error spawned=%d, want 1 (fail open to today's rule)", r.spawned)
	}
}

func TestBattery_BatteryWithNoPercentFailsOpen(t *testing.T) {
	r := newBatteryRig(power.Reading{OnBattery: true, Percent: power.NoPercent})
	r.m.Tick(true)
	if r.spawned != 1 || r.m.BatteryStatus().Gate != GateUnknown {
		t.Errorf("spawned=%d gate=%v, want 1/unknown", r.spawned, r.m.BatteryStatus().Gate)
	}
}

func TestBattery_BetweenReleaseAndHoldKeepsRunningButSpawnsNothing(t *testing.T) {
	r := newBatteryRig(onAC(80))
	r.m.Tick(true) // holding
	r.bat.Set(onBattery(25))
	r.m.Tick(true)
	if r.m.State() != StateArmedRunning || r.killed != 0 {
		t.Errorf("at 25%%: state=%v killed=%d, want the running hold kept", r.m.State(), r.killed)
	}
	if got := r.m.BatteryStatus().Gate; got != GateNoSpawn {
		t.Errorf("Gate = %v, want no_spawn", got)
	}
	// The process dies on its own; it must not come back while the gate holds.
	r.alive = false
	r.m.Tick(true)
	if r.spawned != 1 {
		t.Errorf("respawned at 25%% on battery: spawned=%d, want 1", r.spawned)
	}
}

func TestBattery_HysteresisBeforeResuming(t *testing.T) {
	r := newBatteryRig(onBattery(30))
	r.m.Tick(true) // at the hold threshold exactly
	if r.spawned != 0 {
		t.Fatalf("spawned=%d at 30%% (<= hold), want 0", r.spawned)
	}
	// Charge creeps up while still on battery (e.g. a brief idle): still
	// inside hold+hysteresis, so the gate stays closed.
	r.bat.Set(onBattery(35))
	r.m.Tick(true)
	if r.spawned != 0 {
		t.Errorf("spawned=%d at 35%% (== hold+hysteresis), want 0", r.spawned)
	}
	r.bat.Set(onBattery(36))
	r.m.Tick(true)
	if r.spawned != 1 {
		t.Errorf("spawned=%d at 36%% (> hold+hysteresis), want 1", r.spawned)
	}
}

func TestBattery_HoldThresholdIsInclusive(t *testing.T) {
	r := newBatteryRig(onBattery(31))
	r.m.Tick(true)
	if r.spawned != 1 {
		t.Errorf("at 31%% (> hold) spawned=%d, want 1", r.spawned)
	}
}

func TestBattery_DisabledPolicyOrNilReaderIsTodaysBehavior(t *testing.T) {
	t.Run("zero hold disables", func(t *testing.T) {
		r := newBatteryRig(onBattery(2))
		r.m.BatteryPolicy = BatteryPolicy{}
		r.m.Tick(true)
		if r.spawned != 1 || r.m.BatteryStatus().Gate != GateDisabled {
			t.Errorf("spawned=%d gate=%v, want 1/disabled", r.spawned, r.m.BatteryStatus().Gate)
		}
		if r.bat.Reads() != 0 {
			t.Errorf("a disabled policy still read the battery %d times", r.bat.Reads())
		}
	})
	t.Run("nil reader", func(t *testing.T) {
		r := newBatteryRig(onBattery(2))
		r.m.Battery = nil
		r.m.Tick(true)
		if r.spawned != 1 || r.m.BatteryStatus().Gate != GateDisabled {
			t.Errorf("spawned=%d gate=%v, want 1/disabled", r.spawned, r.m.BatteryStatus().Gate)
		}
	})
	t.Run("noop reader", func(t *testing.T) {
		r := newBatteryRig(onBattery(2))
		r.m.Battery = power.NoopReader{}
		r.m.Tick(true)
		if r.spawned != 1 || r.m.BatteryStatus().Gate != GateOK {
			t.Errorf("spawned=%d gate=%v, want 1/ok", r.spawned, r.m.BatteryStatus().Gate)
		}
	})
}

func TestBattery_ReleaseAboveHoldBehavesAsHold(t *testing.T) {
	r := newBatteryRig(onAC(80))
	r.m.BatteryPolicy = BatteryPolicy{HoldPct: 30, ReleasePct: 50, HysteresisPct: 5}
	r.m.Tick(true)
	r.bat.Set(onBattery(40)) // above hold: no gate at all
	r.m.Tick(true)
	if r.killed != 0 {
		t.Errorf("released at 40%% with hold 30: killed=%d", r.killed)
	}
	r.bat.Set(onBattery(30))
	r.m.Tick(true)
	if r.killed != 1 {
		t.Errorf("not released at 30%% (release clamped to hold): killed=%d", r.killed)
	}
}

func TestBattery_SpawnBlockedCountedOncePerEpisode(t *testing.T) {
	r := newBatteryRig(onBattery(15))
	for i := 0; i < 5; i++ {
		r.m.Tick(true)
	}
	if got := r.m.TakeBatteryReleases(); !reflect.DeepEqual(got, []string{ReasonSpawnBlocked}) {
		t.Errorf("five blocked Ticks recorded %v, want exactly one spawn_blocked", got)
	}
	r.m.Tick(false) // work ends: the episode ends
	r.m.Tick(true)  // new work, still on a low battery: a new episode
	if got := r.m.TakeBatteryReleases(); !reflect.DeepEqual(got, []string{ReasonSpawnBlocked}) {
		t.Errorf("a second episode recorded %v, want one spawn_blocked", got)
	}
	if got := r.m.TakeBatteryReleases(); len(got) != 0 {
		t.Errorf("TakeBatteryReleases must clear: %v", got)
	}
}

func TestBattery_NoReleaseRecordedWhenNothingIsWanted(t *testing.T) {
	r := newBatteryRig(onBattery(15))
	r.m.Tick(false) // toggle on, nothing working
	if got := r.m.TakeBatteryReleases(); len(got) != 0 {
		t.Errorf("recorded %v with nothing to hold", got)
	}
}

func TestBattery_ErrorStateDropsToOffWithoutReleaseCount(t *testing.T) {
	r := newBatteryRig(onAC(80))
	r.m.Spawn = func(int) error { return errors.New("boom") }
	r.m.Tick(true)
	if r.m.State() != StateError {
		t.Fatalf("setup: state=%v, want Error", r.m.State())
	}
	r.bat.Set(onBattery(10))
	r.m.Tick(true)
	if r.m.State() != StateOff || r.m.SpawnErr() != nil {
		t.Errorf("state=%v err=%v, want Off/nil", r.m.State(), r.m.SpawnErr())
	}
	if got := r.m.TakeBatteryReleases(); !reflect.DeepEqual(got, []string{ReasonSpawnBlocked}) {
		t.Errorf("releases = %v, want only spawn_blocked (no process was released)", got)
	}
}

func TestBattery_StatusTrackedWhileToggleOff(t *testing.T) {
	r := newBatteryRig(onBattery(12))
	r.m.SetToggle(false)
	r.m.Tick(true)
	if got := r.m.BatteryStatus(); got.Gate != GateReleased || got.Percent != 12 {
		t.Errorf("BatteryStatus = %+v, want released/12 even with the toggle off", got)
	}
	if r.spawned != 0 {
		t.Errorf("spawned=%d with toggle off", r.spawned)
	}
}

func TestBatteryGate_String(t *testing.T) {
	cases := map[BatteryGate]string{
		GateDisabled: "disabled", GateOK: "ok", GateNoSpawn: "no_spawn",
		GateReleased: "released", GateUnknown: "unknown",
	}
	for g, want := range cases {
		if got := g.String(); got != want {
			t.Errorf("%d.String() = %q, want %q", g, got, want)
		}
	}
}
