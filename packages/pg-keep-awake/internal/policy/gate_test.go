package policy

import (
	"errors"
	"testing"
)

// Origin: the battery cases below are ported from pa-monitor's
// internal/core/caffeinate/battery_test.go. That file drove a Manager (spawn
// and kill counters); here the same scenarios assert the gate directly, where
// "spawned" is gate == ok and "killed" is gate == released. The blind-rule and
// latch cases are new.

func onBattery(pct int) Reading { return Reading{OnBattery: true, Percent: pct} }
func onAC(pct int) Reading      { return Reading{OnBattery: false, Percent: pct} }

var errRead = errors.New("pmset exploded")

func defaults() *Evaluator { return NewEvaluator(DefaultPolicy()) }

func TestDefaultPolicy(t *testing.T) {
	if got, want := DefaultPolicy(), (Policy{HoldPct: 30, ReleasePct: 20, HysteresisPct: 5}); got != want {
		t.Errorf("DefaultPolicy = %+v, want %+v", got, want)
	}
}

func TestGate_DefaultLevels(t *testing.T) {
	cases := []struct {
		name string
		r    Reading
		want Gate
	}{
		{"ac high", onAC(80), GateOK},
		{"ac at low charge keeps hold", onAC(15), GateOK},
		{"battery above hold", onBattery(31), GateOK},
		{"battery at hold (inclusive)", onBattery(30), GateNoSpawn},
		{"battery between release and hold", onBattery(25), GateNoSpawn},
		{"battery just above release", onBattery(21), GateNoSpawn},
		{"battery at release (inclusive)", onBattery(20), GateReleased},
		{"battery below release", onBattery(15), GateReleased},
		{"battery at zero", onBattery(0), GateReleased},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := defaults().Observe(tc.r, nil)
			if st.Gate != tc.want {
				t.Errorf("gate = %v, want %v", st.Gate, tc.want)
			}
			if st.Reading != tc.r {
				t.Errorf("Reading = %+v, want %+v", st.Reading, tc.r)
			}
			if st.Blind {
				t.Error("a good reading is never blind")
			}
		})
	}
}

func TestGate_NamesAreExact(t *testing.T) {
	want := map[Gate]string{
		GateDisabled: "disabled", GateOK: "ok", GateNoSpawn: "no_spawn",
		GateReleased: "released", GateUnknown: "unknown",
	}
	for g, s := range want {
		if got := g.String(); got != s {
			t.Errorf("%d.String() = %q, want %q", g, got, s)
		}
		back, err := ParseGate(s)
		if err != nil || back != g {
			t.Errorf("ParseGate(%q) = %v, %v; want %v", s, back, err, g)
		}
	}
	if _, err := ParseGate("nope"); err == nil {
		t.Error("ParseGate accepted an unknown token")
	}
}

func TestGate_Restricts(t *testing.T) {
	for g, want := range map[Gate]bool{
		GateDisabled: false, GateOK: false, GateUnknown: false, GateNoSpawn: true, GateReleased: true,
	} {
		if got := g.Restricts(); got != want {
			t.Errorf("%v.Restricts() = %v, want %v", g, got, want)
		}
	}
}

// The latch holds no_spawn between hold and hold+hysteresis (pa-monitor's
// HysteresisBeforeResuming).
func TestGate_HysteresisLatch(t *testing.T) {
	e := defaults()
	steps := []struct {
		r    Reading
		want Gate
		note string
	}{
		{onBattery(30), GateNoSpawn, "at hold: latch set"},
		{onBattery(31), GateNoSpawn, "above hold, inside hysteresis"},
		{onBattery(35), GateNoSpawn, "== hold+hysteresis still latched"},
		{onBattery(36), GateOK, "> hold+hysteresis clears the latch"},
		{onBattery(33), GateOK, "latch is not re-set above hold"},
		{onBattery(30), GateNoSpawn, "back at hold: latch set again"},
		{onAC(32), GateOK, "AC clears the latch"},
		{onBattery(32), GateOK, "latch stayed cleared after AC"},
	}
	for _, s := range steps {
		if got := e.Observe(s.r, nil).Gate; got != s.want {
			t.Errorf("%s: gate = %v, want %v", s.note, got, s.want)
		}
	}
}

func TestGate_LatchStartsUnsetAtProcessStart(t *testing.T) {
	// A restart on battery at 33 (between hold and hold+hysteresis) loses the
	// latch: accepted, the gate reads ok.
	e := defaults()
	if e.Latched() {
		t.Fatal("a new Evaluator must start with the latch unset")
	}
	if got := e.Observe(onBattery(33), nil).Gate; got != GateOK {
		t.Errorf("gate = %v, want ok with an unset latch at 33%%", got)
	}
}

func TestGate_ReleaseAboveHoldIsCappedAtHold(t *testing.T) {
	e := NewEvaluator(Policy{HoldPct: 30, ReleasePct: 50, HysteresisPct: 5})
	if got := e.Policy.EffectiveRelease(); got != 30 {
		t.Fatalf("EffectiveRelease = %d, want 30", got)
	}
	if got := e.Observe(onBattery(40), nil).Gate; got != GateOK {
		t.Errorf("at 40%% with hold 30: %v, want ok (no gate at all)", got)
	}
	if got := e.Observe(onBattery(31), nil).Gate; got != GateOK {
		t.Errorf("at 31%%: %v, want ok", got)
	}
	if got := e.Observe(onBattery(30), nil).Gate; got != GateReleased {
		t.Errorf("at 30%% (release capped at hold): %v, want released", got)
	}
}

func TestGate_HoldZeroDisablesTheRule(t *testing.T) {
	for _, p := range []Policy{{}, {HoldPct: 0, ReleasePct: 20, HysteresisPct: 5}, {HoldPct: -1}} {
		e := NewEvaluator(p)
		if got := e.Observe(onBattery(2), nil).Gate; got != GateDisabled {
			t.Errorf("%+v at 2%% on battery: %v, want disabled", p, got)
		}
		if e.Latched() {
			t.Errorf("%+v: a disabled rule must not latch", p)
		}
		// Failed reads never blind a disabled rule.
		for range 5 {
			if got := e.Observe(Reading{Percent: NoPercent}, errRead).Gate; got != GateDisabled {
				t.Errorf("%+v: failed read with the rule off: %v, want disabled", p, got)
			}
		}
	}
}

func TestGate_EvaluateSkipsTheReadWhenDisabled(t *testing.T) {
	fr := NewFakeReader(onBattery(2))
	e := NewEvaluator(Policy{})
	if got := e.Evaluate(fr).Gate; got != GateDisabled {
		t.Errorf("gate = %v, want disabled", got)
	}
	if fr.Reads() != 0 {
		t.Errorf("a disabled policy still read the battery %d times", fr.Reads())
	}
}

func TestGate_EvaluateReadsOncePerCall(t *testing.T) {
	fr := NewFakeReader(onBattery(25))
	e := defaults()
	if got := e.Evaluate(fr).Gate; got != GateNoSpawn {
		t.Errorf("gate = %v, want no_spawn", got)
	}
	if fr.Reads() != 1 {
		t.Errorf("reads = %d, want 1", fr.Reads())
	}
}

func TestGate_ReleaseZeroNeverReleases(t *testing.T) {
	for _, rel := range []int{0, -3} {
		e := NewEvaluator(Policy{HoldPct: 30, ReleasePct: rel, HysteresisPct: 5})
		for _, pct := range []int{30, 10, 1, 0} {
			if got := e.Observe(onBattery(pct), nil).Gate; got != GateNoSpawn {
				t.Errorf("release %d at %d%%: %v, want no_spawn (never released from a reading)", rel, pct, got)
			}
		}
	}
}

func TestGate_ReleaseZeroStillSubjectToTheBlindRule(t *testing.T) {
	e := NewEvaluator(Policy{HoldPct: 30, ReleasePct: 0, HysteresisPct: 5})
	e.Observe(onBattery(25), nil)
	for range 2 {
		e.Observe(Reading{Percent: NoPercent}, errRead)
	}
	st := e.Observe(Reading{Percent: NoPercent}, errRead)
	if st.Gate != GateReleased || !st.Blind {
		t.Errorf("status = %+v, want released by the blind rule even with release 0", st)
	}
}

func TestGate_OnACAtLowChargeKeepsHold(t *testing.T) {
	e := defaults()
	st := e.Observe(onAC(15), nil)
	if st.Gate != GateOK {
		t.Errorf("on AC at 15%%: %v, want ok", st.Gate)
	}
	if st.Gate.Restricts() {
		t.Error("AC must not restrict")
	}
}

func TestGate_HoldResumesAfterACReturns(t *testing.T) {
	e := defaults()
	if got := e.Observe(onBattery(15), nil).Gate; got != GateReleased {
		t.Fatalf("setup: %v, want released", got)
	}
	if got := e.Observe(onAC(15), nil).Gate; got != GateOK {
		t.Errorf("after AC returned: %v, want ok", got)
	}
}

func TestGate_SingleReadErrorFailsOpen(t *testing.T) {
	e := defaults()
	e.Observe(onAC(80), nil)
	st := e.Observe(Reading{Percent: NoPercent}, errRead)
	if st.Gate != GateUnknown || st.Blind || st.ReadFailures != 1 {
		t.Errorf("status = %+v, want unknown, not blind, 1 failure", st)
	}
	if st.Gate.Restricts() {
		t.Error("unknown must fail open")
	}
}

func TestGate_ReadErrorAfterLowChargeFailsOpen(t *testing.T) {
	// pa-monitor's ReadErrorAfterLowChargeFailsOpen: the latch is forgotten.
	e := defaults()
	if got := e.Observe(onBattery(25), nil).Gate; got != GateNoSpawn {
		t.Fatalf("setup: %v, want no_spawn", got)
	}
	if got := e.Observe(Reading{Percent: NoPercent}, errRead).Gate; got != GateUnknown {
		t.Errorf("after a read error: %v, want unknown", got)
	}
	if e.Latched() {
		t.Error("a failed read must forget the latch")
	}
}

func TestGate_BatteryWithNoPercentFailsOpen(t *testing.T) {
	e := defaults()
	st := e.Observe(Reading{OnBattery: true, Percent: NoPercent}, nil)
	if st.Gate != GateUnknown || st.ReadFailures != 1 {
		t.Errorf("status = %+v, want unknown with 1 failure", st)
	}
}

func TestGate_NoopStyleReadingIsOK(t *testing.T) {
	// pa-monitor's noop reader: on AC, charge unknown.
	if got := defaults().Observe(Reading{OnBattery: false, Percent: NoPercent}, nil).Gate; got != GateOK {
		t.Errorf("gate = %v, want ok", got)
	}
}

func TestGate_BlindRuleAfterThreeFailures(t *testing.T) {
	cases := []struct {
		name      string
		lastGood  *Reading
		wantAfter [3]Gate
	}{
		{"last good on battery below release", ptr(onBattery(15)), [3]Gate{GateUnknown, GateUnknown, GateReleased}},
		{"last good on battery between release and hold", ptr(onBattery(25)), [3]Gate{GateUnknown, GateUnknown, GateReleased}},
		{"last good on battery at hold", ptr(onBattery(30)), [3]Gate{GateUnknown, GateUnknown, GateReleased}},
		{"last good on battery above hold", ptr(onBattery(31)), [3]Gate{GateUnknown, GateUnknown, GateUnknown}},
		{"last good on AC at low charge", ptr(onAC(10)), [3]Gate{GateUnknown, GateUnknown, GateUnknown}},
		{"no good reading ever", nil, [3]Gate{GateUnknown, GateUnknown, GateUnknown}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := defaults()
			if tc.lastGood != nil {
				e.Observe(*tc.lastGood, nil)
			}
			for i, want := range tc.wantAfter {
				if got := e.Observe(Reading{Percent: NoPercent}, errRead); got.Gate != want {
					t.Errorf("failure %d: gate = %v, want %v", i+1, got.Gate, want)
				}
			}
		})
	}
}

func TestGate_BlindRuleKeepsFailingOpenAboveHoldForever(t *testing.T) {
	e := defaults()
	e.Observe(onBattery(60), nil)
	for i := range 20 {
		if got := e.Observe(Reading{Percent: NoPercent}, errRead).Gate; got != GateUnknown {
			t.Fatalf("failure %d: %v, want unknown", i+1, got)
		}
	}
}

func TestGate_BlindStaysReleasedWhileReadsKeepFailing(t *testing.T) {
	e := defaults()
	e.Observe(onBattery(25), nil)
	for i := 1; i <= 10; i++ {
		got := e.Observe(Reading{Percent: NoPercent}, errRead)
		if i >= 3 && (got.Gate != GateReleased || !got.Blind) {
			t.Fatalf("failure %d: %+v, want released/blind", i, got)
		}
	}
}

func TestGate_GoodReadResetsTheFailureCounter(t *testing.T) {
	e := defaults()
	e.Observe(onBattery(25), nil)
	e.Observe(Reading{Percent: NoPercent}, errRead)
	e.Observe(Reading{Percent: NoPercent}, errRead)
	if got := e.Observe(onBattery(26), nil); got.Gate != GateNoSpawn || got.ReadFailures != 0 {
		t.Fatalf("recovery: %+v, want no_spawn with 0 failures", got)
	}
	// Two more failures are fewer than 3 consecutive: not blind.
	e.Observe(Reading{Percent: NoPercent}, errRead)
	if got := e.Observe(Reading{Percent: NoPercent}, errRead); got.Gate != GateUnknown {
		t.Errorf("2 failures after recovery: %v, want unknown", got.Gate)
	}
}

func TestGate_BlindUsesTheLastGoodReadingNotTheLatch(t *testing.T) {
	// The latch is forgotten on a failure, but the last good reading is not.
	e := defaults()
	e.Observe(onBattery(25), nil)
	for range 3 {
		e.Observe(Reading{Percent: NoPercent}, errRead)
	}
	if e.Latched() {
		t.Error("latch must be forgotten on failure")
	}
	if got := e.Observe(onBattery(29), nil).Gate; got != GateNoSpawn {
		t.Errorf("recovery at 29%%: %v, want no_spawn", got)
	}
}

func TestBlind_Predicate(t *testing.T) {
	cases := []struct {
		name     string
		failures int
		good     Reading
		have     bool
		hold     int
		want     bool
	}{
		{"3 failures, battery at hold", 3, onBattery(30), true, 30, true},
		{"3 failures, battery below hold", 3, onBattery(5), true, 30, true},
		{"many failures, battery below hold", 40, onBattery(5), true, 30, true},
		{"2 failures is not enough", 2, onBattery(5), true, 30, false},
		{"0 failures", 0, onBattery(5), true, 30, false},
		{"battery above hold", 3, onBattery(31), true, 30, false},
		{"on AC", 3, onAC(5), true, 30, false},
		{"no good reading", 3, Reading{Percent: NoPercent}, false, 30, false},
		{"unknown percent on battery", 3, Reading{OnBattery: true, Percent: NoPercent}, true, 30, false},
		{"rule off (hold 0)", 3, onBattery(5), true, 0, false},
		{"rule off (hold negative)", 3, onBattery(5), true, -1, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Blind(tc.failures, tc.good, tc.have, tc.hold); got != tc.want {
				t.Errorf("Blind = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestBlind_IsKeyedOnHoldNotRelease(t *testing.T) {
	// 25% is above release (20) and at or below hold (30): the keyed-on-release
	// alternative would never fire here.
	if !Blind(BlindReads, onBattery(25), true, DefaultHoldPct) {
		t.Error("blind must fire for a last good reading between release and hold")
	}
}

func ptr[T any](v T) *T { return &v }
