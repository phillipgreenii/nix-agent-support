package anchor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-keep-awake/internal/lease"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-keep-awake/internal/policy"
)

// fakeClock advances only when Sleep is called, so a test controls awake time
// exactly. onSleep runs after each sleep with the number of sleeps so far.
type fakeClock struct {
	awake   time.Duration
	wall    time.Time
	sleeps  int
	onSleep func(n int)
	// maxSleeps stops a runaway loop: Sleep returns an error past it.
	maxSleeps int
}

func newClock() *fakeClock {
	return &fakeClock{wall: time.Date(2026, 10, 14, 20, 20, 0, 0, time.UTC), maxSleeps: 10000}
}

func (c *fakeClock) Awake() time.Duration { return c.awake }
func (c *fakeClock) Now() time.Time       { return c.wall }
func (c *fakeClock) Sleep(_ context.Context, d time.Duration) error {
	c.sleeps++
	if c.sleeps > c.maxSleeps {
		return errors.New("fake clock: runaway loop")
	}
	c.awake += d
	c.wall = c.wall.Add(d)
	if c.onSleep != nil {
		c.onSleep(c.sleeps)
	}
	return nil
}

type rig struct {
	t       *testing.T
	dir     *lease.Dir
	battery string
	hb      string
	clock   *fakeClock
	p       Params
}

// newRig builds a rig with a file battery source (on AC at 80%) and a
// heartbeat file the test controls through setHeartbeat.
func newRig(t *testing.T) *rig {
	t.Helper()
	d, err := lease.EnsureDir(filepath.Join(t.TempDir(), "state", lease.DirName))
	if err != nil {
		t.Fatal(err)
	}
	r := &rig{
		t:       t,
		dir:     d,
		battery: filepath.Join(t.TempDir(), "battery"),
		hb:      filepath.Join(t.TempDir(), "hb"),
		clock:   newClock(),
		p: Params{
			ReleasePct: 20, BlindPct: 30, Watchdog: time.Hour, Interval: time.Second,
		},
	}
	r.p.Heartbeat = r.hb
	r.setBattery("80 ac")
	return r
}

func (r *rig) setBattery(line string) {
	r.t.Helper()
	if err := os.WriteFile(r.battery, []byte(line+"\n"), 0o600); err != nil {
		r.t.Fatal(err)
	}
}

func (r *rig) breakBattery() {
	r.t.Helper()
	if err := os.Remove(r.battery); err != nil && !errors.Is(err, os.ErrNotExist) {
		r.t.Fatal(err)
	}
}

func (r *rig) setHeartbeat(mtime time.Time) {
	r.t.Helper()
	if err := os.WriteFile(r.hb, nil, 0o600); err != nil {
		r.t.Fatal(err)
	}
	if err := os.Chtimes(r.hb, mtime, mtime); err != nil {
		r.t.Fatal(err)
	}
}

// run drives Run to completion on the fake clock and the file battery source.
func (r *rig) run() (*lease.AnchorExitFile, error) {
	r.t.Helper()
	return Run(context.Background(), r.p, Env{
		Dir:    r.dir,
		Reader: policy.FileReader{Path: r.battery},
		Clock:  r.clock,
		PID:    4412,
	})
}

func (r *rig) mustExit(reason string, percent int) *lease.AnchorExitFile {
	r.t.Helper()
	exit, err := r.run()
	if err != nil {
		r.t.Fatalf("Run: %v", err)
	}
	if exit.Reason != reason || exit.Percent != percent {
		r.t.Fatalf("exit = reason %q percent %d, want %q %d", exit.Reason, exit.Percent, reason, percent)
	}
	got, err := r.dir.ReadAnchorExit()
	if err != nil {
		r.t.Fatalf("anchor-exit.json not written: %v", err)
	}
	if got.SchemaVersion != lease.SchemaVersion || got.Reason != reason || got.Percent != percent || got.PID != 4412 {
		r.t.Fatalf("anchor-exit.json = %+v", got)
	}
	if !got.At.Equal(exit.At) || !got.At.Equal(r.clock.wall) {
		r.t.Fatalf("at = %v, want %v", got.At, r.clock.wall)
	}
	return got
}

// mustRunOn asserts the anchor is still running after ticks sleeps and writes
// nothing: the clock's maxSleeps ends the run.
func (r *rig) mustRunOn(ticks int) {
	r.t.Helper()
	r.clock.maxSleeps = ticks
	exit, err := r.run()
	if err == nil {
		r.t.Fatalf("anchor exited (%+v), want it to keep running", exit)
	}
	if _, rerr := r.dir.ReadAnchorExit(); rerr == nil {
		r.t.Fatal("anchor-exit.json exists although the anchor did not exit")
	}
}

func TestReleaseThresholdOnBatteryAt19(t *testing.T) {
	r := newRig(t)
	r.setBattery("19 battery")
	r.setHeartbeat(time.Unix(1000, 0))
	r.mustExit(ReasonRelease, 19)
}

func TestReleaseThresholdAtExactlyR(t *testing.T) {
	r := newRig(t)
	r.setBattery("20 battery")
	r.mustExit(ReasonRelease, 20)
}

func TestNoExitAbove(t *testing.T) {
	r := newRig(t)
	r.setBattery("21 battery")
	r.mustRunOn(20)
}

func TestNoExitAt19OnAC(t *testing.T) {
	r := newRig(t)
	r.setBattery("19 ac")
	r.mustRunOn(20)
}

func TestReleaseZeroNeverExitsForBattery(t *testing.T) {
	r := newRig(t)
	r.p.ReleasePct, r.p.BlindPct = 0, 0
	r.setBattery("1 battery")
	r.mustRunOn(20)
}

func TestReleaseTriggersWhenBatteryDropsLater(t *testing.T) {
	r := newRig(t)
	r.setBattery("50 battery")
	r.clock.onSleep = func(n int) {
		if n == 3 {
			r.setBattery("18 battery")
		}
	}
	r.mustExit(ReasonRelease, 18)
	if r.clock.sleeps != 3 {
		t.Errorf("exited after %d sleeps, want 3", r.clock.sleeps)
	}
}

func TestBlindThresholdAfterThreeFailedReads(t *testing.T) {
	r := newRig(t)
	r.setBattery("25 battery") // above release (20), at or below blind (30)
	r.clock.onSleep = func(n int) {
		if n == 1 {
			r.breakBattery()
		}
	}
	r.mustExit(ReasonBlind, 25)
	// 1 good read, then 3 failed reads (after sleeps 1,2,3): 3 sleeps total.
	if r.clock.sleeps != 3 {
		t.Errorf("exited after %d sleeps, want 3", r.clock.sleeps)
	}
}

func TestBlindNeedsThreeConsecutiveFailures(t *testing.T) {
	r := newRig(t)
	r.setBattery("25 battery")
	r.clock.onSleep = func(n int) {
		switch n {
		case 1, 3:
			r.breakBattery() // two failures, then a good read resets the count
		case 2, 5:
			r.setBattery("25 battery")
		}
	}
	// Reads: good, fail, good, fail, fail, good: never 3 failures in a row.
	r.clock.maxSleeps = 5
	if _, err := r.run(); err == nil {
		t.Fatal("anchor exited, want it to keep running")
	}
	if _, err := r.dir.ReadAnchorExit(); err == nil {
		t.Fatal("anchor-exit.json written")
	}
}

func TestBlindDoesNotFireAboveB(t *testing.T) {
	r := newRig(t)
	r.setBattery("31 battery")
	r.clock.onSleep = func(n int) {
		if n == 1 {
			r.breakBattery()
		}
	}
	r.mustRunOn(20)
}

func TestBlindDoesNotFireWhenLastGoodWasOnAC(t *testing.T) {
	r := newRig(t)
	r.setBattery("10 ac")
	r.clock.onSleep = func(n int) {
		if n == 1 {
			r.breakBattery()
		}
	}
	r.mustRunOn(20)
}

func TestBlindDoesNotFireWithoutAnyGoodRead(t *testing.T) {
	r := newRig(t)
	r.breakBattery()
	r.mustRunOn(20)
}

func TestBlindZeroDisablesBlindRule(t *testing.T) {
	r := newRig(t)
	r.p.ReleasePct, r.p.BlindPct = 0, 0
	r.setBattery("5 battery")
	r.clock.onSleep = func(n int) {
		if n == 1 {
			r.breakBattery()
		}
	}
	r.mustRunOn(20)
}

func TestWatchdogFiresWhenMtimeFrozen(t *testing.T) {
	r := newRig(t)
	r.p.Watchdog = 10 * time.Second
	r.setHeartbeat(time.Unix(1000, 0))
	r.mustExit(ReasonWatchdog, 80) // last good reading was 80 on AC
	if r.clock.awake != 10*time.Second {
		t.Errorf("exited at awake %s, want 10s", r.clock.awake)
	}
}

func TestWatchdogPercentUnknownWithoutGoodRead(t *testing.T) {
	r := newRig(t)
	r.p.Watchdog = 3 * time.Second
	r.breakBattery()
	r.setHeartbeat(time.Unix(1000, 0))
	r.mustExit(ReasonWatchdog, policy.NoPercent)
}

func TestWatchdogDoesNotFireWhileMtimeChanges(t *testing.T) {
	r := newRig(t)
	r.p.Watchdog = 10 * time.Second
	r.setHeartbeat(time.Unix(1000, 0))
	r.clock.onSleep = func(n int) { r.setHeartbeat(time.Unix(1000+int64(n), 0)) }
	r.mustRunOn(100)
}

func TestWatchdogCountsFromLastChange(t *testing.T) {
	r := newRig(t)
	r.p.Watchdog = 10 * time.Second
	r.setHeartbeat(time.Unix(1000, 0))
	r.clock.onSleep = func(n int) {
		if n <= 4 {
			r.setHeartbeat(time.Unix(1000+int64(n), 0)) // last change seen at awake 4s
		}
	}
	r.mustExit(ReasonWatchdog, 80)
	if r.clock.awake != 14*time.Second {
		t.Errorf("exited at awake %s, want 14s (10s after the change at 4s)", r.clock.awake)
	}
}

func TestMissingHeartbeatCountsAsUnchanged(t *testing.T) {
	r := newRig(t)
	r.p.Watchdog = 5 * time.Second
	// The heartbeat file never exists.
	r.mustExit(ReasonWatchdog, 80)
	if r.clock.awake != 5*time.Second {
		t.Errorf("exited at awake %s, want 5s", r.clock.awake)
	}
}

func TestHeartbeatDisappearingCountsAsUnchanged(t *testing.T) {
	r := newRig(t)
	r.p.Watchdog = 5 * time.Second
	r.setHeartbeat(time.Unix(1000, 0))
	r.clock.onSleep = func(n int) {
		if n == 2 {
			if err := os.Remove(r.hb); err != nil {
				t.Fatal(err)
			}
		}
	}
	r.mustExit(ReasonWatchdog, 80)
	if r.clock.awake != 5*time.Second {
		t.Errorf("exited at awake %s, want 5s", r.clock.awake)
	}
}

func TestHeartbeatAppearingResetsCountdown(t *testing.T) {
	r := newRig(t)
	r.p.Watchdog = 5 * time.Second
	r.clock.onSleep = func(n int) {
		if n == 4 {
			r.setHeartbeat(time.Unix(1000, 0))
		}
	}
	r.mustExit(ReasonWatchdog, 80)
	if r.clock.awake != 9*time.Second {
		t.Errorf("exited at awake %s, want 9s", r.clock.awake)
	}
}

func TestWallClockStepDoesNotResetCountdown(t *testing.T) {
	r := newRig(t)
	r.p.Watchdog = 10 * time.Second
	r.setHeartbeat(time.Unix(1000, 0))
	r.clock.onSleep = func(n int) {
		// Wall clock jumps around; the heartbeat mtime value stays the same.
		if n == 3 {
			r.clock.wall = r.clock.wall.Add(-6 * time.Hour)
		}
		if n == 6 {
			r.clock.wall = r.clock.wall.Add(48 * time.Hour)
		}
	}
	exit, err := r.run()
	if err != nil {
		t.Fatal(err)
	}
	if exit.Reason != ReasonWatchdog || r.clock.awake != 10*time.Second {
		t.Fatalf("exit %+v at awake %s, want watchdog at 10s", exit, r.clock.awake)
	}
}

func TestStatedMtimeAgeIsIgnored(t *testing.T) {
	r := newRig(t)
	r.p.Watchdog = 10 * time.Second
	// The injected Stat returns a frozen mtime in the far future and then the
	// far past: only changes of the value matter, never the age.
	mt := time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
	exit, err := Run(context.Background(), r.p, Env{
		Dir:    r.dir,
		Reader: policy.NewFakeReader(policy.Reading{Percent: 80}),
		Clock:  r.clock,
		Stat:   func(string) (time.Time, error) { return mt, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if exit.Reason != ReasonWatchdog || r.clock.awake != 10*time.Second {
		t.Fatalf("exit %+v at awake %s", exit, r.clock.awake)
	}
}

func TestPrecedenceReleaseOverWatchdog(t *testing.T) {
	r := newRig(t)
	r.p.Watchdog = time.Second
	// The watchdog (1s, no heartbeat file) and the release level both come due
	// at the read after sleep 1: release wins.
	r.setBattery("50 battery")
	r.clock.onSleep = func(n int) {
		if n == 1 {
			r.setBattery("10 battery")
		}
	}
	r.mustExit(ReasonRelease, 10)
}

func TestPrecedenceBlindOverWatchdog(t *testing.T) {
	r := newRig(t)
	r.p.Watchdog = 3 * time.Second
	r.setBattery("25 battery")
	r.clock.onSleep = func(n int) {
		if n == 1 {
			r.breakBattery()
		}
	}
	// Blind fires at the read after sleep 3 and the watchdog is due at awake 3
	// as well: blind wins.
	r.mustExit(ReasonBlind, 25)
}

func TestContextCancelWritesNothing(t *testing.T) {
	r := newRig(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	real := NewRealClock()
	_, err := Run(ctx, r.p, Env{Dir: r.dir, Reader: policy.FileReader{Path: r.battery}, Clock: real})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if _, rerr := r.dir.ReadAnchorExit(); rerr == nil {
		t.Fatal("anchor-exit.json written on cancel")
	}
}

func TestHonorsBatterySourceEnv(t *testing.T) {
	r := newRig(t)
	r.setBattery("12 battery")
	t.Setenv(policy.BatterySourceEnv, "file:"+r.battery)
	exit, err := Run(context.Background(), r.p, Env{Dir: r.dir, Clock: r.clock})
	if err != nil {
		t.Fatal(err)
	}
	if exit.Reason != ReasonRelease || exit.Percent != 12 {
		t.Fatalf("exit = %+v", exit)
	}
	if exit.PID != os.Getpid() {
		t.Errorf("pid = %d, want own pid %d", exit.PID, os.Getpid())
	}
}

func TestValidate(t *testing.T) {
	ok := Params{ReleasePct: 20, BlindPct: 30, Watchdog: time.Second, Interval: time.Second, Heartbeat: "x"}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mut := range map[string]func(*Params){
		"release low":   func(p *Params) { p.ReleasePct = -1 },
		"release high":  func(p *Params) { p.ReleasePct = 101 },
		"blind low":     func(p *Params) { p.BlindPct = -1 },
		"blind high":    func(p *Params) { p.BlindPct = 101 },
		"watchdog zero": func(p *Params) { p.Watchdog = 0 },
		"interval zero": func(p *Params) { p.Interval = 0 },
		"no heartbeat":  func(p *Params) { p.Heartbeat = "" },
	} {
		p := ok
		mut(&p)
		if p.Validate() == nil {
			t.Errorf("%s: Validate = nil, want an error", name)
		}
	}
}
