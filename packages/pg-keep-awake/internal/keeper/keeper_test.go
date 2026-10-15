package keeper

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-keep-awake/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-keep-awake/internal/lease"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-keep-awake/internal/policy"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-keep-awake/internal/telemetry"
)

// recEmitter records the keeper's emissions. The embedded Noop supplies every
// method the tests do not look at.
type recEmitter struct {
	telemetry.Noop
	mu          sync.Mutex
	up          []bool
	anchorExits []string
	battRel     []string
	reaped      []string
	released    []string
	configErrs  []string
	adopted     []int
	spawned     []int
	transitions []string
	created     []string
	ticks       int
	closed      bool
}

func (r *recEmitter) RecordKeeperUp(up bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.up = append(r.up, up)
}

func (r *recEmitter) RecordTick(time.Duration, time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ticks++
}

func (r *recEmitter) IncAnchorExit(reason string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.anchorExits = append(r.anchorExits, reason)
}

func (r *recEmitter) IncBatteryRelease(reason string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.battRel = append(r.battRel, reason)
}

func (r *recEmitter) IncLeasesReaped(reason string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reaped = append(r.reaped, reason)
}

func (r *recEmitter) LogAssertionReleased(reason string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.released = append(r.released, reason)
}

func (r *recEmitter) LogConfigError(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.configErrs = append(r.configErrs, err.Error())
}

func (r *recEmitter) LogAssertionAdopted(pid int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.adopted = append(r.adopted, pid)
}

func (r *recEmitter) LogAssertionSpawned(pid int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.spawned = append(r.spawned, pid)
}

func (r *recEmitter) LogGateTransition(from, to string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.transitions = append(r.transitions, from+">"+to)
}

func (r *recEmitter) LogLeaseCreated(id, _ string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.created = append(r.created, id)
}

func (r *recEmitter) Close(context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	return nil
}

// krig is a Keeper over a fake process table, a fake battery and a fake clock,
// with a real temp state directory.
type krig struct {
	t      *testing.T
	dir    *lease.Dir
	fp     *fakeProcs
	reader *policy.FakeReader
	em     *recEmitter
	k      *Keeper
	now    time.Time
	steps  []string
}

func newKRig(t *testing.T, mut func(*KeeperOptions)) *krig {
	t.Helper()
	d := newDir(t)
	g := &krig{
		t: t, dir: d, fp: newFakeProcs(t), em: &recEmitter{}, now: t0,
		reader: policy.NewFakeReader(policy.Reading{Percent: 64}),
	}
	opts := KeeperOptions{
		Dir: d, Config: config.Defaults(), Version: "test-1",
		Emitter: g.em, Reader: g.reader, Procs: g.fp, Exe: testExe,
		External: func(int) []lease.StatusExternal { return nil },
		Now:      func() time.Time { return g.now },
	}
	if mut != nil {
		mut(&opts)
	}
	g.k = NewKeeper(opts)
	g.k.step = func(s string) { g.steps = append(g.steps, s) }
	return g
}

func (g *krig) tick() {
	g.t.Helper()
	if err := g.k.Tick(); err != nil {
		g.t.Fatalf("Tick: %v", err)
	}
}

func (g *krig) advance(d time.Duration) { g.now = g.now.Add(d) }

func (g *krig) status() *lease.StatusFile {
	g.t.Helper()
	s, err := g.dir.ReadStatus()
	if err != nil {
		g.t.Fatalf("ReadStatus: %v", err)
	}
	return s
}

func (g *krig) hbPath() string { return filepath.Join(g.dir.Path(), FileHeartbeat) }

func (g *krig) hbMTime() time.Time {
	g.t.Helper()
	fi, err := os.Stat(g.hbPath())
	if err != nil {
		g.t.Fatalf("heartbeat: %v", err)
	}
	return fi.ModTime()
}

// putAcquire writes a live acquire lease expiring an hour after the rig's now.
func (g *krig) putAcquire(key string, flags ...string) {
	g.t.Helper()
	exp := g.now.Add(time.Hour)
	put(g.t, g.dir, &lease.Lease{Owner: "t", Key: key, Kind: lease.KindAcquire, ExpiresAt: &exp, Flags: flags, CreatedAt: g.now})
}

func TestTickRunsTheSixStepsInOrder(t *testing.T) {
	g := newKRig(t, nil)
	g.putAcquire("a")
	g.tick()
	want := []string{stepSweep, stepGate, stepDesired, stepObserve, stepConverge, stepStatus, stepHeartbeat}
	if !slices.Equal(g.steps, want) {
		t.Fatalf("steps = %v, want %v", g.steps, want)
	}
	s := g.status()
	if !s.Assertion.Held || len(s.Leases) != 1 {
		t.Fatalf("status after a tick with a live lease = %+v", s)
	}
	if !g.hbMTime().Equal(g.now) {
		t.Fatalf("heartbeat mtime %v, want the tick time %v", g.hbMTime(), g.now)
	}
	if g.em.ticks != 1 {
		t.Fatalf("RecordTick calls = %d, want 1", g.em.ticks)
	}
}

func TestHeartbeatTouchedOnlyAfterStatusWritten(t *testing.T) {
	g := newKRig(t, nil)
	if err := TouchHeartbeat(g.dir, t0.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	old := g.hbMTime()
	// status.json cannot be replaced while a directory sits there: the pass
	// cannot complete.
	statusDir := filepath.Join(g.dir.Path(), lease.FileStatus)
	if err := os.MkdirAll(filepath.Join(statusDir, "x"), 0o700); err != nil {
		t.Fatal(err)
	}
	g.advance(time.Minute)
	if err := g.k.Tick(); err == nil {
		t.Fatal("Tick succeeded although status.json could not be written")
	}
	if !g.hbMTime().Equal(old) {
		t.Fatalf("heartbeat touched by an incomplete pass: %v, was %v", g.hbMTime(), old)
	}
	if slices.Contains(g.steps, stepHeartbeat) {
		t.Fatalf("steps %v include the heartbeat step", g.steps)
	}

	// The pass completes once status.json can be written, and the heartbeat
	// follows the write.
	if err := os.RemoveAll(statusDir); err != nil {
		t.Fatal(err)
	}
	g.steps = nil
	g.tick()
	if !g.hbMTime().After(old) {
		t.Fatalf("heartbeat not touched by a completed pass: %v", g.hbMTime())
	}
	if i, j := slices.Index(g.steps, stepStatus), slices.Index(g.steps, stepHeartbeat); i < 0 || j < i {
		t.Fatalf("heartbeat must follow the status write; steps = %v", g.steps)
	}
}

func TestLockSkippedSweepStillTouchesHeartbeat(t *testing.T) {
	g := newKRig(t, nil)
	if err := TouchHeartbeat(g.dir, t0.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	old := g.hbMTime()
	lk, err := g.dir.TakeLock() // hold leases/.lock so the sweep cannot
	if err != nil {
		t.Fatal(err)
	}
	defer lk.Unlock()
	g.tick() // blocks for the 2 second bound, then carries on
	if !g.hbMTime().After(old) {
		t.Fatal("a pass with a lock-skipped sweep must still touch the heartbeat")
	}
	if g.status().Keeper.PID != os.Getpid() {
		t.Fatal("status.json not written on a lock-skipped pass")
	}
}

func TestSweepListFailureIsAnIncompletePass(t *testing.T) {
	g := newKRig(t, nil)
	if err := TouchHeartbeat(g.dir, t0.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	old := g.hbMTime()
	// leases/ replaced by a regular file: the keeper cannot judge the leases,
	// so it must neither drop the assertion nor claim a completed pass.
	if err := os.RemoveAll(g.dir.LeasesPath()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(g.dir.LeasesPath(), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := g.k.Tick(); err == nil {
		t.Fatal("Tick succeeded although the leases directory is unusable")
	}
	if !g.hbMTime().Equal(old) {
		t.Fatal("heartbeat touched although the leases could not be listed")
	}
}

func TestStartCreatesHeartbeatFirstAndKeepsItThroughTheStartupSweep(t *testing.T) {
	g := newKRig(t, nil)
	if err := g.k.Start(); err != nil {
		t.Fatal(err)
	}
	if len(g.steps) == 0 || g.steps[0] != stepStartHB {
		t.Fatalf("steps = %v, want the heartbeat created first", g.steps)
	}
	if _, err := os.Stat(g.hbPath()); err != nil {
		t.Fatalf("heartbeat missing after Start (the startup sweep must not remove it): %v", err)
	}
	if len(g.em.up) == 0 || !g.em.up[0] {
		t.Fatalf("keeper.up not set on start: %v", g.em.up)
	}
}

func TestConfigErrorReportedInStatusAndLogged(t *testing.T) {
	g := newKRig(t, func(o *KeeperOptions) { o.ConfigError = "parse config: boom" })
	if err := g.k.Start(); err != nil {
		t.Fatal(err)
	}
	g.tick()
	s := g.status()
	if s.Keeper.ConfigError == nil || *s.Keeper.ConfigError != "parse config: boom" {
		t.Fatalf("keeper.config_error = %v", s.Keeper.ConfigError)
	}
	if !slices.Equal(g.em.configErrs, []string{"parse config: boom"}) {
		t.Fatalf("LogConfigError calls = %v", g.em.configErrs)
	}
}

func TestNoConfigErrorByDefault(t *testing.T) {
	g := newKRig(t, nil)
	g.tick()
	if s := g.status(); s.Keeper.ConfigError != nil {
		t.Fatalf("keeper.config_error = %q, want null", *s.Keeper.ConfigError)
	}
}

func TestAnchorExitConsumedOnNextTick(t *testing.T) {
	g := newKRig(t, nil)
	g.putAcquire("a")
	g.tick()
	if !g.status().Assertion.Held {
		t.Fatal("assertion not held after the first tick")
	}
	anchor := g.k.rc.cur.anchor
	// The anchor exits by itself and leaves its record.
	g.fp.kill(anchor.PID)
	if err := g.dir.WriteAnchorExit(&lease.AnchorExitFile{At: g.now, Reason: lease.ReasonReleaseThreshold, Percent: 18, PID: anchor.PID}); err != nil {
		t.Fatal(err)
	}
	g.advance(time.Minute)
	g.tick()

	if _, err := os.Stat(filepath.Join(g.dir.Path(), lease.FileAnchorExit)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("anchor-exit.json not removed: %v", err)
	}
	if !slices.Equal(g.em.anchorExits, []string{"release_threshold"}) {
		t.Fatalf("anchor.exits_total emissions = %v", g.em.anchorExits)
	}
	if !slices.Contains(g.em.battRel, "release_threshold") {
		t.Fatalf("battery.releases_total emissions = %v", g.em.battRel)
	}
	if !slices.Contains(g.em.released, "release_threshold") {
		t.Fatalf("assertion.released logs = %v", g.em.released)
	}
	lr := g.status().LastRelease
	if lr == nil || lr.Reason != lease.ReasonReleaseThreshold || !lr.At.Equal(g.now) {
		t.Fatalf("last_release = %+v", lr)
	}

	// The previous last_release is carried forward into later snapshots.
	g.advance(time.Minute)
	g.tick()
	lr2 := g.status().LastRelease
	if lr2 == nil || lr2.Reason != lease.ReasonReleaseThreshold || !lr2.At.Equal(lr.At) {
		t.Fatalf("last_release not carried forward: %+v, was %+v", lr2, lr)
	}
	if len(g.em.anchorExits) != 1 {
		t.Fatalf("a consumed record was emitted again: %v", g.em.anchorExits)
	}
}

func TestAnchorGoneWithoutRecordIsUnknown(t *testing.T) {
	g := newKRig(t, nil)
	g.putAcquire("a")
	g.tick()
	anchor := g.k.rc.cur.anchor
	g.fp.kill(anchor.PID) // SIGKILL: no anchor-exit.json
	g.advance(2 * time.Minute)
	g.tick() // the same tick restarts the pair; the loss is still recorded

	if !slices.Equal(g.em.anchorExits, []string{"unknown"}) {
		t.Fatalf("anchor.exits_total emissions = %v, want [unknown]", g.em.anchorExits)
	}
	lr := g.status().LastRelease
	if lr == nil || lr.Reason != lease.ReasonUnknown {
		t.Fatalf("last_release = %+v, want unknown", lr)
	}
	// The loss is reported once.
	g.advance(time.Minute)
	g.tick()
	if len(g.em.anchorExits) != 1 {
		t.Fatalf("anchor loss emitted again: %v", g.em.anchorExits)
	}
}

func TestExitRecordConsumedWhileAnchorAliveIsNotAlsoUnknown(t *testing.T) {
	g := newKRig(t, nil)
	g.putAcquire("a")
	g.tick()
	anchor := g.k.rc.cur.anchor
	// The record is visible a moment before the anchor is gone.
	if err := g.dir.WriteAnchorExit(&lease.AnchorExitFile{At: g.now, Reason: lease.ReasonWatchdog, PID: anchor.PID}); err != nil {
		t.Fatal(err)
	}
	g.advance(time.Minute)
	g.tick()
	g.fp.kill(anchor.PID)
	g.advance(time.Minute)
	g.tick()
	if !slices.Equal(g.em.anchorExits, []string{"watchdog"}) {
		t.Fatalf("anchor.exits_total emissions = %v, want just [watchdog]", g.em.anchorExits)
	}
}

func TestPauseWithPastUntilIsRemoved(t *testing.T) {
	g := newKRig(t, nil)
	g.putAcquire("a")
	past := g.now.Add(-time.Minute)
	if err := g.dir.WritePause(&lease.PauseFile{Until: &past, CreatedAt: past.Add(-time.Minute), ByPID: 1}); err != nil {
		t.Fatal(err)
	}
	g.tick()
	if _, err := os.Stat(filepath.Join(g.dir.Path(), lease.FilePause)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expired pause.json not removed: %v", err)
	}
	s := g.status()
	if s.Paused != nil || !s.Assertion.Held {
		t.Fatalf("an expired pause must count as no pause: paused=%v held=%v", s.Paused, s.Assertion.Held)
	}
}

func TestActivePauseIsKeptAndStopsTheAssertion(t *testing.T) {
	g := newKRig(t, nil)
	g.putAcquire("a")
	g.tick()
	future := g.now.Add(time.Hour)
	if err := g.dir.WritePause(&lease.PauseFile{Until: &future, CreatedAt: g.now, ByPID: 1}); err != nil {
		t.Fatal(err)
	}
	g.advance(time.Second)
	g.tick()
	if _, err := os.Stat(filepath.Join(g.dir.Path(), lease.FilePause)); err != nil {
		t.Fatalf("active pause.json removed: %v", err)
	}
	s := g.status()
	if s.Paused == nil || s.Assertion.Held {
		t.Fatalf("paused=%v held=%v, want paused and not held", s.Paused, s.Assertion.Held)
	}
	if lr := s.LastRelease; lr == nil || lr.Reason != lease.ReasonPaused {
		t.Fatalf("last_release = %+v, want paused", lr)
	}
}

func TestDegradedTickCounter(t *testing.T) {
	g := newKRig(t, nil)
	g.putAcquire("a")
	g.fp.startFn = func([]string) error { return errTest } // nothing can be started
	for want := 1; want <= 3; want++ {
		g.advance(time.Second)
		g.tick()
		if got := g.status().UnexplainedTicks; got != want {
			t.Fatalf("tick %d: unexplained_ticks = %d, want %d", want, got, want)
		}
	}

	// A pause explains the missing assertion: the counter resets.
	future := g.now.Add(time.Hour)
	if err := g.dir.WritePause(&lease.PauseFile{Until: &future, CreatedAt: g.now, ByPID: 1}); err != nil {
		t.Fatal(err)
	}
	g.advance(time.Second)
	g.tick()
	if got := g.status().UnexplainedTicks; got != 0 {
		t.Fatalf("unexplained_ticks under a pause = %d, want 0", got)
	}
	if err := g.dir.RemovePause(); err != nil {
		t.Fatal(err)
	}

	// Degraded again, then healed.
	g.advance(time.Second)
	g.tick()
	if got := g.status().UnexplainedTicks; got != 1 {
		t.Fatalf("unexplained_ticks after the pause = %d, want 1", got)
	}
	g.fp.startFn = nil
	g.advance(time.Second)
	g.tick()
	s := g.status()
	if s.UnexplainedTicks != 0 || !s.Assertion.Held {
		t.Fatalf("healed: unexplained_ticks=%d held=%v", s.UnexplainedTicks, s.Assertion.Held)
	}
}

func TestLingerAndDesiredHoldFactsReachTheSnapshot(t *testing.T) {
	g := newKRig(t, nil)
	g.putAcquire("a")
	g.tick()
	if err := g.dir.RemoveLease("t.a"); err != nil {
		t.Fatal(err)
	}
	g.advance(time.Second)
	g.tick()
	s := g.status()
	if !s.Assertion.Held || s.LingerUntil == nil || !s.LingerUntil.After(g.now) {
		t.Fatalf("linger: held=%v linger_until=%v", s.Assertion.Held, s.LingerUntil)
	}
	if s.UnexplainedTicks != 0 {
		t.Fatalf("linger counted as degraded: %d", s.UnexplainedTicks)
	}
}

func TestBatteryReleaseIsCountedAndTransitionsLogged(t *testing.T) {
	g := newKRig(t, nil)
	g.putAcquire("a")
	g.tick()
	g.reader.Set(policy.Reading{OnBattery: true, Percent: 18})
	g.advance(time.Second)
	g.tick()
	s := g.status()
	if s.Assertion.Held || s.Battery.Gate != "released" {
		t.Fatalf("held=%v gate=%q, want not held and released", s.Assertion.Held, s.Battery.Gate)
	}
	if !slices.Contains(g.em.transitions, "ok>released") {
		t.Fatalf("gate transitions = %v", g.em.transitions)
	}
	if !slices.Contains(g.em.battRel, "release_threshold") {
		t.Fatalf("battery releases = %v", g.em.battRel)
	}
}

func TestSpawnBlockedCountedOncePerBlock(t *testing.T) {
	g := newKRig(t, nil)
	g.reader.Set(policy.Reading{OnBattery: true, Percent: 28}) // no_spawn: at or below hold
	g.putAcquire("a")
	for range 3 {
		g.advance(time.Second)
		g.tick()
	}
	if !slices.Equal(g.em.battRel, []string{"spawn_blocked"}) {
		t.Fatalf("battery releases = %v, want one spawn_blocked", g.em.battRel)
	}
}

func TestLeaseAndReapEmissions(t *testing.T) {
	g := newKRig(t, nil)
	g.putAcquire("a")
	past := g.now.Add(-time.Minute)
	put(t, g.dir, &lease.Lease{Owner: "t", Key: "old", Kind: lease.KindAcquire, ExpiresAt: &past, CreatedAt: g.now})
	g.tick()
	if !slices.Equal(g.em.created, []string{"t.a"}) {
		t.Fatalf("lease creations = %v", g.em.created)
	}
	if !slices.Equal(g.em.reaped, []string{"expired"}) {
		t.Fatalf("reaped = %v", g.em.reaped)
	}
	g.tick()
	if len(g.em.created) != 1 {
		t.Fatalf("a known lease was logged as created again: %v", g.em.created)
	}
}

func TestShutdownRemovesHeartbeatKeepsLeasesAndPair(t *testing.T) {
	g := newKRig(t, nil)
	if err := g.k.Start(); err != nil {
		t.Fatal(err)
	}
	g.putAcquire("a")
	past := g.now.Add(-time.Minute)
	put(t, g.dir, &lease.Lease{Owner: "t", Key: "stale", Kind: lease.KindAcquire, ExpiresAt: &past, CreatedAt: g.now})
	g.tick()
	pair := g.k.rc.cur
	g.fp.reset()

	g.k.Shutdown()

	if _, err := os.Stat(g.hbPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("heartbeat survived the shutdown sweep: %v", err)
	}
	if !exists(t, leaseFile(g.dir, "t.a")) {
		t.Fatal("a valid lease was removed at shutdown")
	}
	if exists(t, leaseFile(g.dir, "t.stale")) {
		t.Fatal("an expired lease survived the shutdown sweep")
	}
	if !exists(t, filepath.Join(g.dir.Path(), lease.FileAssertion)) {
		t.Fatal("assertion.json removed at shutdown")
	}
	if len(g.fp.log()) != 0 {
		t.Fatalf("shutdown touched processes: %v", g.fp.log())
	}
	if !g.fp.procs[pair.anchor.PID].up || !g.fp.procs[pair.caff.PID].up {
		t.Fatal("the anchor or caffeinate was stopped at shutdown")
	}
	if !g.em.closed || g.em.up[len(g.em.up)-1] {
		t.Fatalf("emitter: closed=%v up=%v", g.em.closed, g.em.up)
	}
}

func TestRunTicksUntilCancelledThenShutsDown(t *testing.T) {
	g := newKRig(t, func(o *KeeperOptions) {
		o.Now = time.Now
		o.Config.HeartbeatInterval = 20 * time.Millisecond
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- g.k.Run(ctx) }()

	deadline := time.Now().Add(5 * time.Second)
	for g.tickCount() < 3 {
		if time.Now().After(deadline) {
			cancel()
			t.Fatalf("Run did not tick: %d ticks", g.tickCount())
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
	if _, err := os.Stat(g.hbPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("heartbeat survived Run's shutdown: %v", err)
	}
}

func (g *krig) tickCount() int {
	g.em.mu.Lock()
	defer g.em.mu.Unlock()
	return g.em.ticks
}

func TestStartAdoptsARunningPair(t *testing.T) {
	g := newKRig(t, nil)
	g.putAcquire("a")
	g.tick()
	pair := g.k.rc.cur

	// A restarted keeper over the same state directory and process table.
	em2 := &recEmitter{}
	g2 := *g
	g2.em = em2
	g2.k = NewKeeper(KeeperOptions{
		Dir: g.dir, Config: config.Defaults(), Version: "test-1", Emitter: em2, Reader: g.reader,
		Procs: g.fp, Exe: testExe, External: func(int) []lease.StatusExternal { return nil },
		Now: func() time.Time { return g.now },
	})
	g.fp.reset()
	if err := g2.k.Start(); err != nil {
		t.Fatal(err)
	}
	g.advance(time.Second)
	if err := g2.k.Tick(); err != nil {
		t.Fatal(err)
	}
	if hasPrefixCall(g.fp.log(), "start") {
		t.Fatalf("the restarted keeper started a new process instead of adopting: %v", g.fp.log())
	}
	if !slices.Equal(em2.adopted, []int{pair.caff.PID}) {
		t.Fatalf("adopted = %v, want [%d]", em2.adopted, pair.caff.PID)
	}
	if s := g2.status(); !s.Assertion.Held || s.Assertion.PID != pair.caff.PID {
		t.Fatalf("status after adoption = %+v", s.Assertion)
	}
}
