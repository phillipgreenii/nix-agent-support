package keeper

import (
	"slices"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-keep-awake/internal/lease"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-keep-awake/internal/policy"
)

func TestDesiredStateUnderEachGate(t *testing.T) {
	hold := []policy.Gate{policy.GateOK, policy.GateUnknown, policy.GateDisabled}
	for _, gate := range hold {
		t.Run(gate.String()+" holds", func(t *testing.T) {
			g := newRig(t)
			res := g.tickG(gateOf(gate), ll("a", "i"))
			noErrs(t, res)
			if !res.Held || !res.Started || !res.WantHold || res.SpawnBlocked {
				t.Fatalf("result = %+v", res)
			}
			equalCalls(t, g.fp.log(), "start anchor#101", "start caffeinate#102")
		})
	}
	t.Run("no_spawn neither starts nor holds", func(t *testing.T) {
		g := newRig(t)
		res := g.tickG(gateOf(policy.GateNoSpawn), ll("a", "i"))
		if res.Held || res.Started || !res.SpawnBlocked || res.BlockedBy != BlockNoSpawn {
			t.Fatalf("result = %+v", res)
		}
		equalCalls(t, g.fp.log())
	})
	t.Run("released drops and does not start", func(t *testing.T) {
		g := newRig(t)
		res := g.tickG(gateOf(policy.GateReleased), ll("a", "i"))
		if res.Held || res.Started || res.WantHold {
			t.Fatalf("result = %+v", res)
		}
		equalCalls(t, g.fp.log())
	})
}

func TestStartOrderArgvAndAssertionFile(t *testing.T) {
	g := newRig(t)
	res := g.tick(ll("a", "s"), ll("b", "d", "i"), ll("c"))
	noErrs(t, res)
	equalCalls(t, g.fp.log(), "start anchor#101", "start caffeinate#102")

	anchor := g.fp.procs[101].argv
	if want := g.cfg.AnchorArgv(testExe); !slices.Equal(anchor, want) {
		t.Fatalf("anchor argv = %q, want %q", anchor, want)
	}
	wantAnchor := []string{
		testExe, "anchor", "--release-pct", "20", "--blind-pct", "30", "--watchdog", "600s",
		"--heartbeat", g.cfg.StateDir + "/keeper.heartbeat", "--interval", "10s", "--state-dir", g.cfg.StateDir,
	}
	if !slices.Equal(anchor, wantAnchor) {
		t.Fatalf("anchor argv = %q, want %q", anchor, wantAnchor)
	}
	wantCaff := []string{"/usr/bin/caffeinate", "-d", "-i", "-s", "-w", "101"}
	if got := g.fp.procs[102].argv; !slices.Equal(got, wantCaff) {
		t.Fatalf("caffeinate argv = %q, want %q", got, wantCaff)
	}
	if !slices.Equal(res.Flags, []string{"d", "i", "s"}) || res.AnchorPID != 101 || res.CaffeinatePID != 102 {
		t.Fatalf("result = %+v", res)
	}

	a, err := g.dir.ReadAssertion()
	if err != nil {
		t.Fatalf("ReadAssertion: %v", err)
	}
	if a.SchemaVersion != 1 || a.Anchor.PID != 101 || a.Anchor.StartUS != g.fp.procs[101].startUS ||
		a.Caffeinate.PID != 102 || !slices.Equal(a.Flags, []string{"d", "i", "s"}) ||
		a.KeeperVersion != "test-1" || !a.StartedAt.Equal(t0) {
		t.Fatalf("assertion.json = %+v", a)
	}
	if want := (lease.AssertionParams{ReleasePct: 20, BlindPct: 30, WatchdogSeconds: 600, HeartbeatSeconds: 10, StateDir: g.cfg.StateDir}); a.Params != want {
		t.Fatalf("params = %+v, want %+v", a.Params, want)
	}
	if !slices.Equal(a.Caffeinate.Argv, wantCaff) || !slices.Equal(a.Anchor.Argv, wantAnchor) {
		t.Fatalf("recorded argv = %q / %q", a.Anchor.Argv, a.Caffeinate.Argv)
	}
}

func TestEmptyFlagUnionUsesI(t *testing.T) {
	g := newRig(t)
	g.tick(ll("a"))
	if got := g.fp.procs[102].argv; !slices.Equal(got, []string{"/usr/bin/caffeinate", "-i", "-w", "101"}) {
		t.Fatalf("caffeinate argv = %q", got)
	}
}

func TestLingerThenDrop(t *testing.T) {
	g := newRig(t)
	g.tick(ll("a", "i"))
	g.fp.reset()

	g.advance(10 * time.Second) // first tick without a live lease: the window starts here
	res := g.tick()
	if !res.Held || !res.LingerActive || !res.WantHold || res.Stopped {
		t.Fatalf("result = %+v", res)
	}
	wantUntil := t0.Add(40 * time.Second)
	if res.LingerUntil == nil || !res.LingerUntil.Equal(wantUntil) {
		t.Fatalf("LingerUntil = %v, want %v", res.LingerUntil, wantUntil)
	}

	g.advance(29 * time.Second) // 29 s into the window
	if res := g.tick(); !res.Held || !res.LingerActive {
		t.Fatalf("at 29s: %+v", res)
	}
	equalCalls(t, g.fp.log())

	g.advance(time.Second) // 30 s into the window
	res = g.tick()
	if res.Held || !res.Stopped || res.Release != lease.ReasonNoLeases || res.LingerActive || res.WantHold {
		t.Fatalf("at 30s: %+v", res)
	}
	calls := g.fp.log()
	if indexOf(t, calls, "TERM caffeinate#102") > indexOf(t, calls, "TERM anchor#101") {
		t.Fatalf("caffeinate must stop before the anchor: %q", calls)
	}
	if _, err := g.dir.ReadAssertion(); err == nil {
		t.Fatal("assertion.json must be removed once the assertion is dropped")
	}
}

func TestLeaseReturningDuringLingerKeepsPair(t *testing.T) {
	g := newRig(t)
	g.tick(ll("a", "i"))
	g.fp.reset()
	g.advance(10 * time.Second)
	g.tick()
	g.advance(10 * time.Second)
	res := g.tick(ll("b", "i"))
	if !res.Held || res.LingerActive || res.Started {
		t.Fatalf("result = %+v", res)
	}
	equalCalls(t, g.fp.log())
	// The window restarts at the next lease-free tick.
	g.advance(10 * time.Second)
	g.tick()
	g.advance(29 * time.Second)
	if res := g.tick(); !res.Held {
		t.Fatalf("window must restart, got %+v", res)
	}
}

func TestZeroLingerDropsAtOnce(t *testing.T) {
	g := newRig(t)
	g.cfg.Linger = 0
	g.tick(ll("a", "i"))
	g.advance(10 * time.Second)
	res := g.tick()
	if res.Held || !res.Stopped || res.Release != lease.ReasonNoLeases {
		t.Fatalf("result = %+v", res)
	}
}

func TestReleasedAndPauseOverrideLinger(t *testing.T) {
	t.Run("released", func(t *testing.T) {
		g := newRig(t)
		g.tick(ll("a", "i"))
		g.advance(10 * time.Second)
		if res := g.tick(); !res.LingerActive {
			t.Fatalf("setup: %+v", res)
		}
		g.advance(time.Second) // well inside the window
		res := g.tickG(gateOf(policy.GateReleased))
		if res.Held || !res.Stopped || res.Release != lease.ReasonReleaseThreshold || res.LingerActive {
			t.Fatalf("result = %+v", res)
		}
	})
	t.Run("blind", func(t *testing.T) {
		g := newRig(t)
		g.tick(ll("a", "i"))
		st := gateOf(policy.GateReleased)
		st.Blind = true
		res := g.tickG(st, ll("a", "i"))
		if res.Held || !res.Stopped || res.Release != lease.ReasonBlindThreshold {
			t.Fatalf("result = %+v", res)
		}
	})
	t.Run("pause", func(t *testing.T) {
		g := newRig(t)
		g.tick(ll("a", "i"))
		g.advance(10 * time.Second)
		g.tick()
		g.advance(time.Second)
		in := g.inputs(nil, okGate())
		in.Pause = &lease.PauseFile{CreatedAt: g.now}
		res := g.rc.Reconcile(in)
		if res.Held || !res.Stopped || res.Release != lease.ReasonPaused {
			t.Fatalf("result = %+v", res)
		}
	})
	t.Run("pause with live leases leaves leases and drops the assertion", func(t *testing.T) {
		g := newRig(t)
		g.tick(ll("a", "i"))
		until := g.now.Add(time.Hour)
		in := g.inputs([]LiveLease{ll("a", "i")}, okGate())
		in.Pause = &lease.PauseFile{Until: &until}
		res := g.rc.Reconcile(in)
		if res.Held || res.Release != lease.ReasonPaused || res.WantHold {
			t.Fatalf("result = %+v", res)
		}
		// A paused keeper starts nothing.
		g.fp.reset()
		if res := g.rc.Reconcile(in); res.Held || res.Started {
			t.Fatalf("paused tick started: %+v", res)
		}
		equalCalls(t, g.fp.log())
	})
	t.Run("a past until is no pause", func(t *testing.T) {
		g := newRig(t)
		past := g.now.Add(-time.Minute)
		in := g.inputs([]LiveLease{ll("a", "i")}, okGate())
		in.Pause = &lease.PauseFile{Until: &past}
		if res := g.rc.Reconcile(in); !res.Held {
			t.Fatalf("result = %+v", res)
		}
	})
}

func TestNoSpawnKeepsRunningPairAndNeverReplaces(t *testing.T) {
	g := newRig(t)
	g.tick(ll("a", "i"))
	g.fp.reset()

	res := g.tickG(gateOf(policy.GateNoSpawn), ll("a", "i"), ll("b", "s"))
	if !res.Held || res.Started || res.Replaced || !res.SpawnBlocked || res.BlockedBy != BlockNoSpawn {
		t.Fatalf("result = %+v", res)
	}
	if !slices.Equal(res.Flags, []string{"i"}) {
		t.Fatalf("flags = %v, the running pair must be kept", res.Flags)
	}
	equalCalls(t, g.fp.log())

	// Back to ok: the flag-set change now takes effect.
	res = g.tick(ll("a", "i"), ll("b", "s"))
	if !res.Replaced || !slices.Equal(res.Flags, []string{"i", "s"}) {
		t.Fatalf("result = %+v", res)
	}
}

func TestFlagSetChangeStartsNewPairBeforeStoppingOld(t *testing.T) {
	g := newRig(t)
	g.tick(ll("a", "i"))
	g.fp.reset()

	res := g.tick(ll("a", "i"), ll("b", "s"))
	noErrs(t, res)
	if !res.Replaced || !res.Started || !res.Held || res.AnchorPID != 103 || res.CaffeinatePID != 104 {
		t.Fatalf("result = %+v", res)
	}
	calls := g.fp.log()
	newCaff := indexOf(t, calls, "start caffeinate#104")
	if indexOf(t, calls, "start anchor#103") > newCaff {
		t.Fatalf("the new anchor starts first: %q", calls)
	}
	oldCaff, oldAnchor := indexOf(t, calls, "TERM caffeinate#102"), indexOf(t, calls, "TERM anchor#101")
	if oldCaff < newCaff || oldAnchor < oldCaff {
		t.Fatalf("new pair must be up before the old one stops, caffeinate before anchor: %q", calls)
	}
	if a, err := g.dir.ReadAssertion(); err != nil || a.Anchor.PID != 103 || !slices.Equal(a.Flags, []string{"i", "s"}) {
		t.Fatalf("assertion.json = %+v, %v", a, err)
	}
}

func TestParameterChangeReplacesMakeBeforeBreak(t *testing.T) {
	changes := map[string]func(*AssertionConfig){
		"release level":      func(c *AssertionConfig) { c.ReleasePct = 15 },
		"hold level":         func(c *AssertionConfig) { c.HoldPct = 40 },
		"watchdog":           func(c *AssertionConfig) { c.Watchdog = 5 * time.Minute },
		"heartbeat interval": func(c *AssertionConfig) { c.Heartbeat = 30 * time.Second },
	}
	for name, mod := range changes {
		t.Run(name, func(t *testing.T) {
			g := newRig(t)
			g.tick(ll("a", "i"))
			g.fp.reset()
			mod(&g.cfg)
			res := g.tick(ll("a", "i"))
			if !res.Replaced {
				t.Fatalf("result = %+v", res)
			}
			calls := g.fp.log()
			if indexOf(t, calls, "start caffeinate#104") > indexOf(t, calls, "TERM caffeinate#102") {
				t.Fatalf("make before break violated: %q", calls)
			}
			if got := g.fp.procs[103].argv; !slices.Equal(got, g.cfg.AnchorArgv(testExe)) {
				t.Fatalf("new anchor argv = %q", got)
			}
		})
	}
	t.Run("keeper version does not replace", func(t *testing.T) {
		g := newRig(t)
		g.tick(ll("a", "i"))
		g.fp.reset()
		g.cfg.KeeperVersion = "test-2"
		if res := g.tick(ll("a", "i")); res.Replaced || res.Started {
			t.Fatalf("result = %+v", res)
		}
	})
}

func TestFailedReplacementKeepsOldPair(t *testing.T) {
	g := newRig(t)
	g.tick(ll("a", "i"))
	g.fp.reset()
	starts := 0
	g.fp.startFn = func(argv []string) error {
		starts++
		if starts == 2 { // the new caffeinate
			return errTest
		}
		return nil
	}
	res := g.tick(ll("a", "i"), ll("b", "s"))
	if res.Replaced || len(res.Errs) == 0 || !res.Held || res.AnchorPID != 101 {
		t.Fatalf("result = %+v", res)
	}
	// The half-started new anchor was stopped again; the old pair never was.
	calls := g.fp.log()
	if hasPrefixCall(calls, "TERM caffeinate#102") || hasPrefixCall(calls, "TERM anchor#101") {
		t.Fatalf("old pair was stopped: %q", calls)
	}
	indexOf(t, calls, "TERM anchor#103")
}

func TestChurnGuardLowBattery(t *testing.T) {
	low := func(pct int) policy.Status {
		return policy.Status{Gate: policy.GateOK, Reading: policy.Reading{OnBattery: true, Percent: pct}}
	}
	t.Run("at or below release blocks", func(t *testing.T) {
		for _, pct := range []int{20, 19, 1} {
			g := newRig(t)
			res := g.tickG(low(pct), ll("a", "i"))
			if res.Held || res.Started || !res.SpawnBlocked || res.BlockedBy != BlockLowBattery {
				t.Fatalf("pct %d: %+v", pct, res)
			}
			equalCalls(t, g.fp.log())
		}
	})
	t.Run("above release starts", func(t *testing.T) {
		g := newRig(t)
		if res := g.tickG(low(21), ll("a", "i")); !res.Held {
			t.Fatalf("result = %+v", res)
		}
	})
	t.Run("release 0 never applies", func(t *testing.T) {
		g := newRig(t)
		g.cfg.ReleasePct = 0
		if res := g.tickG(low(5), ll("a", "i")); !res.Held {
			t.Fatalf("result = %+v", res)
		}
	})
	t.Run("rule off never applies", func(t *testing.T) {
		g := newRig(t)
		g.cfg.HoldPct = 0
		if res := g.tickG(low(5), ll("a", "i")); !res.Held {
			t.Fatalf("result = %+v", res)
		}
	})
	t.Run("release above hold is capped at hold", func(t *testing.T) {
		g := newRig(t)
		g.cfg.HoldPct, g.cfg.ReleasePct = 25, 40
		if res := g.tickG(low(26), ll("a", "i")); !res.Held {
			t.Fatalf("26%% is above the capped release level: %+v", res)
		}
		g2 := newRig(t)
		g2.cfg.HoldPct, g2.cfg.ReleasePct = 25, 40
		if res := g2.tickG(low(25), ll("a", "i")); res.Held {
			t.Fatalf("25%% is at the capped release level: %+v", res)
		}
	})
	t.Run("on AC never applies", func(t *testing.T) {
		g := newRig(t)
		st := policy.Status{Gate: policy.GateOK, Reading: policy.Reading{OnBattery: false, Percent: 5}}
		if res := g.tickG(st, ll("a", "i")); !res.Held {
			t.Fatalf("result = %+v", res)
		}
	})
}

func TestChurnGuardNoRespawnAfterQuickAnchorExit(t *testing.T) {
	g := newRig(t)
	g.tick(ll("a", "i"))
	g.advance(10 * time.Second) // the anchor dies 10 s after its spawn
	g.fp.kill(101)
	g.fp.kill(102)
	g.fp.reset()

	exitAt := g.now
	res := g.tick(ll("a", "i"))
	if !res.PairLost || res.Held || res.Started || !res.SpawnBlocked || res.BlockedBy != BlockChurn {
		t.Fatalf("result = %+v", res)
	}
	g.advance(59 * time.Second)
	if res := g.tick(ll("a", "i")); res.Started || res.BlockedBy != BlockChurn {
		t.Fatalf("at +59s: %+v", res)
	}
	equalCalls(t, g.fp.log())
	g.now = exitAt.Add(60 * time.Second)
	if res := g.tick(ll("a", "i")); !res.Started || !res.Held {
		t.Fatalf("at +60s: %+v", res)
	}
}

func TestNoChurnBlockAfterLongLivedAnchorExit(t *testing.T) {
	g := newRig(t)
	g.tick(ll("a", "i"))
	g.advance(31 * time.Second)
	g.fp.kill(101)
	g.fp.kill(102)
	res := g.tick(ll("a", "i"))
	if !res.PairLost || !res.Started || !res.Held || res.SpawnBlocked {
		t.Fatalf("result = %+v", res)
	}
}

func TestCaffeinateKilledIsRestartedUnderTheSameAnchor(t *testing.T) {
	g := newRig(t)
	g.tick(ll("a", "i"))
	g.advance(10 * time.Second)
	g.fp.kill(102)
	g.fp.reset()

	res := g.tick(ll("a", "i"))
	noErrs(t, res)
	if !res.Held || !res.Started || !res.PairLost || res.AnchorPID != 101 || res.CaffeinatePID != 103 {
		t.Fatalf("result = %+v", res)
	}
	equalCalls(t, g.fp.log(), "start caffeinate#103")
	if got := g.fp.procs[103].argv; !slices.Equal(got, []string{"/usr/bin/caffeinate", "-i", "-w", "101"}) {
		t.Fatalf("caffeinate argv = %q", got)
	}
	if a, err := g.dir.ReadAssertion(); err != nil || a.Caffeinate.PID != 103 || a.Anchor.PID != 101 {
		t.Fatalf("assertion.json = %+v, %v", a, err)
	}
}

func TestLoneAnchorIsStoppedWhenNothingMayStart(t *testing.T) {
	g := newRig(t)
	g.tick(ll("a", "i"))
	g.fp.kill(102)
	g.fp.reset()
	res := g.tickG(gateOf(policy.GateNoSpawn), ll("a", "i"))
	if res.Held || res.Started || res.BlockedBy != BlockNoSpawn {
		t.Fatalf("result = %+v", res)
	}
	equalCalls(t, g.fp.log(), "TERM anchor#101", "wait anchor#101")
}

func TestStopEscalatesToKillAfterTermIsIgnored(t *testing.T) {
	g := newRig(t)
	g.cfg.Linger = 0
	g.tick(ll("a", "i"))
	g.fp.ignoreTerm[102] = true
	g.fp.reset()
	g.advance(time.Second)
	res := g.tick()
	if !res.Stopped || res.Held {
		t.Fatalf("result = %+v", res)
	}
	equalCalls(t, g.fp.log(),
		"TERM caffeinate#102", "wait caffeinate#102", "KILL caffeinate#102", "wait caffeinate#102",
		"TERM anchor#101", "wait anchor#101")
}

func TestStartFailureIsReportedNotFatal(t *testing.T) {
	g := newRig(t)
	g.fp.startFn = func([]string) error { return errTest }
	res := g.tick(ll("a", "i"))
	if res.Held || len(res.Errs) == 0 {
		t.Fatalf("result = %+v", res)
	}
}

func TestAnchorStartedSecondStartFailureStopsAnchor(t *testing.T) {
	g := newRig(t)
	n := 0
	g.fp.startFn = func([]string) error {
		n++
		if n == 2 {
			return errTest
		}
		return nil
	}
	res := g.tick(ll("a", "i"))
	if res.Held || len(res.Errs) == 0 {
		t.Fatalf("result = %+v", res)
	}
	indexOf(t, g.fp.log(), "TERM anchor#101")
}

func TestParamsOfAnchorArgvAndAssertionFile(t *testing.T) {
	cases := []struct {
		name          string
		hold, release int
		wantR, wantB  string
	}{
		{"defaults", 30, 20, "20", "30"},
		{"release above hold is capped", 20, 30, "20", "20"},
		{"rule off", 0, 20, "0", "0"},
		{"release off", 30, 0, "0", "30"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			g := newRig(t)
			g.cfg.HoldPct, g.cfg.ReleasePct = c.hold, c.release
			g.tickG(gateOf(policy.GateOK), ll("a", "i"))
			argv := g.fp.procs[101].argv
			i := slices.Index(argv, "--release-pct")
			j := slices.Index(argv, "--blind-pct")
			if i < 0 || j < 0 || argv[i+1] != c.wantR || argv[j+1] != c.wantB {
				t.Fatalf("argv = %q, want --release-pct %s --blind-pct %s", argv, c.wantR, c.wantB)
			}
			a, err := g.dir.ReadAssertion()
			if err != nil {
				t.Fatal(err)
			}
			if got, want := a.Params.ReleasePct, mustAtoi(t, c.wantR); got != want {
				t.Fatalf("params.release_pct = %d, want %d", got, want)
			}
			if got, want := a.Params.BlindPct, mustAtoi(t, c.wantB); got != want {
				t.Fatalf("params.blind_pct = %d, want %d", got, want)
			}
		})
	}
}

func TestBatterySourceNeedsNoArgvElement(t *testing.T) {
	g := newRig(t)
	g.tick(ll("a", "i"))
	for _, a := range g.fp.procs[101].argv {
		if a == policy.BatterySourceEnv || a == "--battery-source" {
			t.Fatalf("the hidden battery source reaches the anchor by environment, found %q in argv", a)
		}
	}
}

// A sub-second heartbeat_interval and watchdog_timeout reach the anchor as
// configured: design section 9 makes the anchor interval I the keeper's
// heartbeat_interval, so nothing is rounded up to whole seconds.
func TestSubSecondIntervalsAreHonored(t *testing.T) {
	g := newRig(t)
	g.cfg.Heartbeat = 200 * time.Millisecond
	g.cfg.Watchdog = 1500 * time.Millisecond
	noErrs(t, g.tick(ll("a", "i")))

	argv := g.fp.procs[101].argv
	for flag, want := range map[string]string{"--interval": "200ms", "--watchdog": "1500ms"} {
		i := slices.Index(argv, flag)
		if i < 0 || argv[i+1] != want {
			t.Fatalf("argv = %q, want %s %s", argv, flag, want)
		}
	}
	a, err := g.dir.ReadAssertion()
	if err != nil {
		t.Fatal(err)
	}
	if a.Params.HeartbeatSeconds != 0.2 || a.Params.WatchdogSeconds != 1.5 {
		t.Fatalf("params = %+v, want heartbeat_seconds 0.2 and watchdog_seconds 1.5", a.Params)
	}
	// The recorded values are the ones the next tick compares: no replacement.
	res := g.tick(ll("a", "i"))
	if res.Started || res.Replaced || !res.Held {
		t.Fatalf("second tick = %+v, want the pair kept", res)
	}
}

func TestFormatDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{
		-time.Second:                            "0s",
		0:                                       "0s",
		10 * time.Second:                        "10s",
		10 * time.Minute:                        "600s",
		200 * time.Millisecond:                  "200ms",
		1500 * time.Millisecond:                 "1500ms",
		time.Millisecond + 500*time.Microsecond: "1.5ms",
	} {
		got := formatDuration(d)
		if got != want {
			t.Errorf("formatDuration(%v) = %q, want %q", d, got, want)
		}
		if d > 0 {
			if back, err := time.ParseDuration(got); err != nil || back != d {
				t.Errorf("formatDuration(%v) = %q does not round-trip: %v, %v", d, got, back, err)
			}
		}
	}
}
