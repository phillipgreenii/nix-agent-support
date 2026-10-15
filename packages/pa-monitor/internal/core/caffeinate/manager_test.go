package caffeinate

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/caffeinate/keepertest"
)

// rig is a Manager driving the REAL pg-keep-awake CLI against an isolated state
// directory, with an injected clock. The keeper process is not started: tests
// stand in for it with keepertest.SimulateKeeper, and everything pa-monitor
// reads is the real CLI's output over those files.
type rig struct {
	t        *testing.T
	m        *Manager
	rec      *recordingKeeper
	stateDir string
	now      time.Time
}

// recordingKeeper delegates to the real CLI and counts the calls it makes.
type recordingKeeper struct {
	inner    Keeper
	acquires []int // the --pid of each acquire
	releases int
}

func (r *recordingKeeper) Acquire(pid int) error {
	r.acquires = append(r.acquires, pid)
	return r.inner.Acquire(pid)
}

func (r *recordingKeeper) Release() error {
	r.releases++
	return r.inner.Release()
}

func (r *recordingKeeper) Observe() Observation { return r.inner.Observe() }

func newRig(t *testing.T) *rig {
	t.Helper()
	bin := keepertest.Bin(t)
	r := &rig{t: t, stateDir: keepertest.UseStateDir(t), now: time.Unix(1_000_000, 0)}
	r.rec = &recordingKeeper{inner: &CLI{Configured: bin}}
	r.m = &Manager{
		Grace:  5 * time.Second,
		Keeper: r.rec,
		Now:    func() time.Time { return r.now },
		PID:    os.Getpid(),
	}
	return r
}

func (r *rig) advance(d time.Duration) { r.now = r.now.Add(d) }

func (r *rig) leaseFile() string { return filepath.Join(r.stateDir, "leases", Owner+".default.json") }

// lease reads pa-monitor's lease file, or nil when it is absent.
func (r *rig) lease() map[string]any {
	r.t.Helper()
	data, err := os.ReadFile(r.leaseFile())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		r.t.Fatal(err)
	}
	var l map[string]any
	if err := json.Unmarshal(data, &l); err != nil {
		r.t.Fatalf("lease file: %v", err)
	}
	return l
}

func TestToggleOnWithBusyWorkAcquiresForTheDaemonPid(t *testing.T) {
	r := newRig(t)
	r.m.SetToggle(true)
	r.m.Tick(true)
	if r.m.State() != StateArmedRunning {
		t.Fatalf("State = %v, want ArmedRunning", r.m.State())
	}
	if len(r.rec.acquires) != 1 || r.rec.acquires[0] != os.Getpid() {
		t.Fatalf("acquire calls = %v, want exactly one with the daemon pid %d", r.rec.acquires, os.Getpid())
	}
	l := r.lease()
	if l == nil {
		t.Fatal("no lease file after acquire")
	}
	if l["owner"] != Owner {
		t.Errorf("lease owner = %v, want %q", l["owner"], Owner)
	}
	client, _ := l["client"].(map[string]any)
	if pid, _ := client["pid"].(float64); int(pid) != os.Getpid() {
		t.Errorf("lease client pid = %v, want the daemon pid %d (--pid)", client["pid"], os.Getpid())
	}
}

func TestNoAcquireWhenIdleOnToggleOn(t *testing.T) {
	r := newRig(t)
	r.m.SetToggle(true)
	r.m.Tick(false)
	if len(r.rec.acquires) != 0 || r.m.State() != StateOff || r.lease() != nil {
		t.Errorf("acquires=%v state=%v lease=%v, want none / Off / no lease", r.rec.acquires, r.m.State(), r.lease())
	}
}

func TestIdleThenGraceExpiryReleases(t *testing.T) {
	r := newRig(t)
	r.m.SetToggle(true)
	r.m.Tick(true)
	r.advance(time.Second)
	r.m.Tick(false)
	if r.m.State() != StateArmedCountdown {
		t.Fatalf("State = %v, want ArmedCountdown", r.m.State())
	}
	if r.lease() == nil {
		t.Fatal("lease dropped during the grace period; it must be held until the grace expires")
	}
	if got := r.m.GraceRemaining(); got != 5*time.Second {
		t.Errorf("GraceRemaining = %v, want 5s", got)
	}
	r.advance(10 * time.Second)
	r.m.Tick(false)
	if r.m.State() != StateOff {
		t.Errorf("State = %v, want Off after the grace expired", r.m.State())
	}
	if r.rec.releases != 1 || r.lease() != nil {
		t.Errorf("releases=%d lease=%v, want one release and no lease file", r.rec.releases, r.lease())
	}
}

func TestReactivateDuringGraceDoesNotReacquire(t *testing.T) {
	r := newRig(t)
	r.m.SetToggle(true)
	r.m.Tick(true)
	r.advance(time.Second)
	r.m.Tick(false)
	r.advance(time.Second)
	r.m.Tick(true)
	if r.m.State() != StateArmedRunning {
		t.Errorf("State = %v, want ArmedRunning", r.m.State())
	}
	if len(r.rec.acquires) != 1 {
		t.Errorf("acquire calls = %d, want 1 (the lease is still held)", len(r.rec.acquires))
	}
}

func TestGoingOffReleasesOwnLease(t *testing.T) {
	r := newRig(t)
	r.m.SetToggle(true)
	r.m.Tick(true)
	if r.lease() == nil {
		t.Fatal("setup: no lease")
	}
	r.m.SetToggle(false)
	if r.m.State() != StateOff {
		t.Errorf("State = %v, want Off", r.m.State())
	}
	if r.rec.releases != 1 || r.lease() != nil {
		t.Errorf("releases=%d lease=%v, want one release and no lease file", r.rec.releases, r.lease())
	}
	// Turning off again, with nothing held, must not call release again.
	r.m.SetToggle(false)
	if r.rec.releases != 1 {
		t.Errorf("release calls = %d after a second off, want 1", r.rec.releases)
	}
}

func TestShutdownReleases(t *testing.T) {
	r := newRig(t)
	r.m.SetToggle(true)
	r.m.Tick(true)
	r.m.Shutdown()
	if r.lease() != nil || r.m.State() != StateOff {
		t.Errorf("lease=%v state=%v, want released and Off", r.lease(), r.m.State())
	}
}

// TestAcquireRepeatsAboutOncePerMinute drives the injected clock: no repeat
// inside the minute, one at each minute mark, and a wiped state directory heals
// at the repeat.
func TestAcquireRepeatsAboutOncePerMinute(t *testing.T) {
	r := newRig(t)
	r.m.SetToggle(true)
	r.m.Tick(true) // t=0
	for _, step := range []struct {
		at       time.Duration
		acquires int
	}{
		{30 * time.Second, 1},
		{59 * time.Second, 1},
		{60 * time.Second, 2},
		{90 * time.Second, 2},
		{121 * time.Second, 3},
	} {
		r.now = time.Unix(1_000_000, 0).Add(step.at)
		r.m.Tick(true)
		if len(r.rec.acquires) != step.acquires {
			t.Fatalf("at +%v: acquire calls = %d, want %d", step.at, len(r.rec.acquires), step.acquires)
		}
	}

	// Wipe the state directory's lease; the next repeat recreates it.
	if err := os.Remove(r.leaseFile()); err != nil {
		t.Fatal(err)
	}
	r.now = time.Unix(1_000_000, 0).Add(182 * time.Second)
	r.m.Tick(true)
	if r.lease() == nil {
		t.Error("lease not recreated by the repeated acquire")
	}
}

// TestAcquireRepeatsDuringGrace: the lease is still wanted while the grace
// counts down, so the repeat continues.
func TestAcquireRepeatsDuringGrace(t *testing.T) {
	r := newRig(t)
	r.m.Grace = 10 * time.Minute
	r.m.SetToggle(true)
	r.m.Tick(true)
	r.advance(time.Second)
	r.m.Tick(false) // grace starts
	r.advance(61 * time.Second)
	r.m.Tick(false)
	if r.m.State() != StateArmedCountdown {
		t.Fatalf("State = %v, want ArmedCountdown", r.m.State())
	}
	if len(r.rec.acquires) != 2 {
		t.Errorf("acquire calls = %d, want 2 (one repeat during grace)", len(r.rec.acquires))
	}
}

// TestHeldReflectsTheKeepersStatus: pa-monitor's held signal is the keeper's
// assertion.held from status --json, not its own idea of what it asked for.
func TestHeldReflectsTheKeepersStatus(t *testing.T) {
	r := newRig(t)
	r.m.SetToggle(true)
	r.m.Tick(true)
	if r.m.Held() {
		t.Error("Held = true with no keeper running; want false")
	}
	keepertest.SimulateKeeper(t, r.stateDir, keepertest.KeeperState{Held: true})
	r.m.Tick(true)
	if !r.m.Held() {
		t.Error("Held = false while the keeper reports the assertion held; want true")
	}
	keepertest.SimulateKeeper(t, r.stateDir, keepertest.KeeperState{Held: false})
	r.m.Tick(true)
	if r.m.Held() {
		t.Error("Held = true after the keeper dropped the assertion; want false")
	}
}

// TestLowBatteryGateReadsBackAndShowsProcessOff: the keeper withholds the
// assertion on a low battery; pa-monitor reads the gate back unchanged and
// presents process=off, as it did when it ran the rule itself.
func TestLowBatteryGateReadsBackAndShowsProcessOff(t *testing.T) {
	r := newRig(t)
	r.m.SetToggle(true)
	r.m.Tick(true)
	keepertest.SimulateKeeper(t, r.stateDir, keepertest.KeeperState{Held: false, Gate: "released", OnBattery: true, Percent: 15})
	r.m.Tick(true)
	if b := r.m.BatteryStatus(); b.Gate != GateReleased || !b.OnBattery || b.Percent != 15 {
		t.Errorf("BatteryStatus = %+v, want released/on-battery/15", b)
	}
	if r.m.State() != StateArmedRunning {
		t.Errorf("State = %v, want ArmedRunning (the lease is still wanted)", r.m.State())
	}
	if r.m.ProcessState() != StateOff {
		t.Errorf("ProcessState = %v, want Off while the keeper withholds the assertion", r.m.ProcessState())
	}
	if r.m.Held() {
		t.Error("Held = true while the gate is released")
	}
	// AC returns and the keeper holds again.
	keepertest.SimulateKeeper(t, r.stateDir, keepertest.KeeperState{Held: true, Gate: "ok"})
	r.m.Tick(true)
	if r.m.ProcessState() != StateArmedRunning || !r.m.Held() {
		t.Errorf("ProcessState=%v Held=%v, want ArmedRunning / true once held", r.m.ProcessState(), r.m.Held())
	}
}

// TestAcquireFailureIsStateError: an acquire call that fails (here the state
// directory path is a file, so the real CLI exits nonzero) is StateError,
// observable as process=error, and a later tick retries.
func TestAcquireFailureIsStateError(t *testing.T) {
	r := newRig(t)
	if err := os.MkdirAll(filepath.Dir(r.stateDir), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(r.stateDir, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	r.m.SetToggle(true)
	r.m.Tick(true)
	if r.m.State() != StateError || r.m.SpawnErr() == nil {
		t.Fatalf("State=%v SpawnErr=%v, want StateError with an error", r.m.State(), r.m.SpawnErr())
	}
	// The directory heals; the next tick retries and recovers.
	if err := os.Remove(r.stateDir); err != nil {
		t.Fatal(err)
	}
	r.advance(time.Second)
	r.m.Tick(true)
	if r.m.State() != StateArmedRunning || r.m.SpawnErr() != nil {
		t.Errorf("State=%v SpawnErr=%v, want ArmedRunning and no error after recovery", r.m.State(), r.m.SpawnErr())
	}
}

// TestMissingExecutableIsStateError: an executable that cannot be found must be
// visible as process=error, never a silent no-op.
func TestMissingExecutableIsStateError(t *testing.T) {
	m := &Manager{
		Grace:  time.Second,
		Keeper: &CLI{Configured: filepath.Join(t.TempDir(), "no-such-pg-keep-awake")},
		Now:    time.Now,
		PID:    os.Getpid(),
	}
	m.SetToggle(true)
	m.Tick(true)
	if m.State() != StateError || !errors.Is(m.SpawnErr(), ErrKeeperNotFound) {
		t.Errorf("State=%v SpawnErr=%v, want StateError wrapping ErrKeeperNotFound", m.State(), m.SpawnErr())
	}
	if m.KeeperUp() {
		t.Error("KeeperUp = true although the executable cannot run")
	}
	// Nothing to hold any more: drop back to Off rather than stay in error.
	m.Tick(false)
	if m.State() != StateOff || m.SpawnErr() != nil {
		t.Errorf("State=%v SpawnErr=%v, want Off and no error once nothing is wanted", m.State(), m.SpawnErr())
	}
}

// TestDegradedKeeperIsStateError: status --check exiting 6 (a live lease, the
// keeper running, nothing held) maps to StateError (process=error), and
// recovers when the keeper holds again.
func TestDegradedKeeperIsStateError(t *testing.T) {
	r := newRig(t)
	r.m.SetToggle(true)
	r.m.Tick(true)
	keepertest.SimulateKeeper(t, r.stateDir, keepertest.KeeperState{Held: false, UnexplainedTicks: 2})
	r.advance(time.Second)
	r.m.Tick(true)
	if r.m.State() != StateError {
		t.Fatalf("State = %v, want StateError for a degraded keeper", r.m.State())
	}
	if !errors.Is(r.m.SpawnErr(), ErrKeeperDegraded) {
		t.Errorf("SpawnErr = %v, want ErrKeeperDegraded", r.m.SpawnErr())
	}
	if !r.m.KeeperUp() {
		t.Error("KeeperUp = false for a degraded keeper; degraded is still up (exit 6)")
	}
	keepertest.SimulateKeeper(t, r.stateDir, keepertest.KeeperState{Held: true})
	r.advance(time.Second)
	r.m.Tick(true)
	if r.m.State() != StateArmedRunning || r.m.SpawnErr() != nil {
		t.Errorf("State=%v SpawnErr=%v, want ArmedRunning and no error once the keeper holds", r.m.State(), r.m.SpawnErr())
	}
}

// TestKeeperUpFollowsStatusCheckExitCode: 0 and 6 are up, 5 is down.
func TestKeeperUpFollowsStatusCheckExitCode(t *testing.T) {
	r := newRig(t)
	r.m.Tick(false) // toggle off: the keeper is still observed
	if r.m.KeeperUp() {
		t.Error("KeeperUp = true with no keeper ever started (status --check exit 5)")
	}
	keepertest.SimulateKeeper(t, r.stateDir, keepertest.KeeperState{})
	r.m.Tick(false)
	if !r.m.KeeperUp() || r.m.obs.Health != HealthOK {
		t.Errorf("KeeperUp=%v health=%v, want up / OK (exit 0)", r.m.KeeperUp(), r.m.obs.Health)
	}
	keepertest.StopKeeper(t, r.stateDir)
	r.m.Tick(false)
	if r.m.KeeperUp() || r.m.obs.Health != HealthDown {
		t.Errorf("KeeperUp=%v health=%v, want down (exit 5)", r.m.KeeperUp(), r.m.obs.Health)
	}
}
