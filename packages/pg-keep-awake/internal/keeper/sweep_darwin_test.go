//go:build darwin

package keeper

import (
	"bufio"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"syscall"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-keep-awake/internal/lease"
)

// child is a process the test itself spawned. It is the only kind of pid the
// tests signal, and every signal path refuses pid 1 or lower.
type child struct {
	cmd *exec.Cmd
	ref lease.ProcRef
}

func (c *child) pid() int { return c.cmd.Process.Pid }

func (c *child) alive() bool { return (lease.SysProber{}).Probe(c.pid()).Exists }

func (c *child) kill() {
	if pid := c.pid(); refusePID(pid) == nil && c.cmd.ProcessState == nil {
		_ = syscall.Kill(pid, syscall.SIGKILL)
		_ = c.cmd.Wait()
	}
}

func startChild(t *testing.T, cmd *exec.Cmd) *child {
	t.Helper()
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	c := &child{cmd: cmd}
	t.Cleanup(c.kill)
	var ref lease.ProcRef
	for i := 0; i < 500; i++ {
		var err error
		if ref, err = lease.Ref(c.pid()); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	c.ref = ref
	return c
}

func spawnSleep(t *testing.T) *child { return startChild(t, exec.Command("sleep", "120")) }

// spawnNamed starts a copy of the test binary under the given executable name
// (the helper mode just sleeps), so ProcessInfo reports that name.
func spawnNamed(t *testing.T, name string) *child {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	src, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, src, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(path)
	cmd.Env = append(os.Environ(), "KEEPER_TEST_HELPER=sleep")
	c := startChild(t, cmd)
	for i := 0; i < 500; i++ { // wait for exec to replace the forked image
		if info, err := lease.ProcessInfo(c.pid()); err == nil && info.Name == name {
			return c
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("child never reported executable name %q", name)
	return nil
}

func waitGone(t *testing.T, c *child) bool {
	t.Helper()
	done := make(chan struct{})
	go func() { _ = c.cmd.Wait(); close(done) }()
	select {
	case <-done:
		return true
	case <-time.After(5 * time.Second):
		return false
	}
}

func realSweeper(d *lease.Dir) *Sweeper { return NewSweeper(d, Options{}) }

func TestSIGKILLedShimLeaseIsReaped(t *testing.T) {
	d := newDir(t)
	shim := spawnNamed(t, "pg-caffeinate-shim")
	other := spawnSleep(t)
	put(t, d, &lease.Lease{Owner: lease.ShimOwner, Key: "9-9", Kind: lease.KindShim, Client: &shim.ref, Flags: []string{"i", "s"}})
	put(t, d, &lease.Lease{Owner: "o", Key: "ok", Kind: lease.KindRun, Client: &other.ref})

	res, err := realSweeper(d).Sweep()
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Live) != 2 || res.Reaped[ReapClientDead] != 0 {
		t.Fatalf("a running shim must hold its lease: live=%v reaped=%v", liveIDs(res), res.Reaped)
	}
	signalChild(t, shim.pid(), syscall.SIGKILL)
	_ = shim.cmd.Wait() // reaped: the pid is gone
	res, err = realSweeper(d).Sweep()
	if err != nil {
		t.Fatal(err)
	}
	if res.Reaped[ReapClientDead] != 1 || exists(t, leaseFile(d, "shim.9-9")) {
		t.Errorf("a SIGKILLed shim's lease must be reaped: reaped=%v", res.Reaped)
	}
	if got := liveIDs(res); !slices.Equal(got, []string{"o.ok"}) {
		t.Errorf("live = %v", got)
	}
	if len(res.Signalled) != 0 {
		t.Errorf("signalled %v for a dead client", res.Signalled)
	}
}

func TestZombieClientDoesNotHold(t *testing.T) {
	d := newDir(t)
	c := spawnSleep(t)
	put(t, d, &lease.Lease{Owner: "o", Key: "z", Kind: lease.KindRun, Client: &c.ref})
	signalChild(t, c.pid(), syscall.SIGKILL) // deliberately not Wait()ed: a zombie
	var st lease.ProcState
	for i := 0; i < 500; i++ {
		if st = (lease.SysProber{}).Probe(c.pid()); st.Zombie {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !st.Exists || !st.Zombie {
		t.Fatalf("state = %+v, want a zombie", st)
	}
	res, err := realSweeper(d).Sweep()
	if err != nil {
		t.Fatal(err)
	}
	if res.Reaped[ReapClientDead] != 1 || len(res.Live) != 0 {
		t.Errorf("zombie client held its lease: reaped=%v live=%v", res.Reaped, liveIDs(res))
	}
	_ = c.cmd.Wait()
}

func TestWatchedProcessDeathReapsRealLease(t *testing.T) {
	d := newDir(t)
	w := spawnSleep(t)
	future := time.Now().Add(time.Hour)
	put(t, d, &lease.Lease{Owner: "o", Key: "w", Kind: lease.KindAcquire, ExpiresAt: &future, Watch: &w.ref})
	res, err := realSweeper(d).Sweep()
	if err != nil || len(res.Live) != 1 {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	signalChild(t, w.pid(), syscall.SIGKILL)
	_ = w.cmd.Wait()
	res, err = realSweeper(d).Sweep()
	if err != nil || res.Reaped[ReapWatchedDead] != 1 || len(res.Live) != 0 {
		t.Errorf("res=%+v err=%v", res, err)
	}
	if len(res.Signalled) != 0 {
		t.Errorf("signalled %v: a dead watch is not a backstop case", res.Signalled)
	}
}

// A real child holds flock on leases/.lock and is then stopped: the sweep must
// give up within the 2 second bound and say so.
func TestStoppedClientHoldingLockMakesSweepGiveUp(t *testing.T) {
	d := newDir(t)
	past := time.Now().Add(-time.Minute)
	put(t, d, &lease.Lease{Owner: "o", Key: "dead", Kind: lease.KindAcquire, ExpiresAt: &past})

	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe)
	cmd.Env = append(os.Environ(), "KEEPER_TEST_HELPER=flock:"+filepath.Join(d.LeasesPath(), ".lock"))
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	c := &child{cmd: cmd}
	t.Cleanup(func() {
		if pid := c.pid(); refusePID(pid) == nil && cmd.ProcessState == nil {
			_ = syscall.Kill(pid, syscall.SIGCONT)
			_ = syscall.Kill(pid, syscall.SIGKILL)
			_ = cmd.Wait()
		}
	})
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil || line != "locked\n" {
		t.Fatalf("helper said %q, %v", line, err)
	}
	signalChild(t, c.pid(), syscall.SIGSTOP)

	start := time.Now()
	res, err := realSweeper(d).Sweep()
	took := time.Since(start)
	if err != nil {
		t.Fatalf("a skipped sweep is not an error: %v", err)
	}
	if !res.LockSkipped {
		t.Error("LockSkipped = false")
	}
	if took < time.Second || took > 4*time.Second {
		t.Errorf("sweep took %v, want about 2s", took)
	}
	if !exists(t, leaseFile(d, "o.dead")) {
		t.Error("nothing may be reaped without the lock")
	}
}

func TestBackstopSignalsRealStuckShimAndLeavesOthersAlone(t *testing.T) {
	d := newDir(t)
	past := time.Now().Add(-time.Minute)
	shim := spawnNamed(t, "pg-caffeinate-shim")
	wrongName := spawnSleep(t) // kind run, but not a pg-keep-awake executable
	acquirePID := spawnNamed(t, "pg-keep-awake")
	put(t, d, &lease.Lease{Owner: "shim", Key: "1-1", Kind: lease.KindShim, Client: &shim.ref, ExpiresAt: &past})
	put(t, d, &lease.Lease{Owner: "o", Key: "run", Kind: lease.KindRun, Client: &wrongName.ref, ExpiresAt: &past})
	// An acquire lease naming a real, matching executable: never signalled.
	put(t, d, &lease.Lease{Owner: "o", Key: "acq", Kind: lease.KindAcquire, Client: &acquirePID.ref, ExpiresAt: &past})

	res, err := realSweeper(d).Sweep()
	if err != nil {
		t.Fatal(err)
	}
	if res.Reaped[ReapExpired] != 3 {
		t.Errorf("Reaped = %v", res.Reaped)
	}
	if !slices.Equal(res.Signalled, []int{shim.pid()}) {
		t.Errorf("Signalled = %v, want only the shim %d", res.Signalled, shim.pid())
	}
	if !waitGone(t, shim) {
		t.Error("the stuck shim did not receive SIGTERM")
	}
	time.Sleep(200 * time.Millisecond)
	if !wrongName.alive() || !acquirePID.alive() {
		t.Errorf("a client that must not be signalled died: wrongName=%v acquire=%v", wrongName.alive(), acquirePID.alive())
	}
}

func TestBackstopRefusesRecycledPIDEvenWithMatchingName(t *testing.T) {
	d := newDir(t)
	shim := spawnNamed(t, "pg-caffeinate-shim")
	past := time.Now().Add(-time.Minute)
	stale := lease.ProcRef{PID: shim.pid(), StartUS: shim.ref.StartUS - 1} // same pid, different start time
	put(t, d, &lease.Lease{Owner: "shim", Key: "2-2", Kind: lease.KindShim, Client: &stale, ExpiresAt: &past})
	res, err := realSweeper(d).Sweep()
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Signalled) != 0 || !shim.alive() {
		t.Errorf("a recycled pid was signalled: %v alive=%v", res.Signalled, shim.alive())
	}
}

// Both sweeps keep the anchor and caffeinate the keeper started, valid leases
// and assertion.json; only the shutdown sweep removes keeper.heartbeat.
func TestSweepsKeepAssertionProcessesAndFiles(t *testing.T) {
	d := newDir(t)
	anchor, caff := spawnNamed(t, "pg-keep-awake"), spawnSleep(t)
	holder := spawnSleep(t)
	put(t, d, &lease.Lease{Owner: "o", Key: "ok", Kind: lease.KindRun, Client: &holder.ref})
	past := time.Now().Add(-time.Minute)
	put(t, d, &lease.Lease{Owner: "o", Key: "old", Kind: lease.KindAcquire, ExpiresAt: &past})
	if err := d.WriteAssertion(&lease.AssertionFile{
		Anchor:     lease.ProcDesc{PID: anchor.pid(), StartUS: anchor.ref.StartUS, Argv: []string{"pg-keep-awake", "anchor"}},
		Caffeinate: lease.ProcDesc{PID: caff.pid(), StartUS: caff.ref.StartUS, Argv: []string{"caffeinate", "-i", "-w", "1"}},
	}); err != nil {
		t.Fatal(err)
	}
	hb := filepath.Join(d.Path(), FileHeartbeat)
	if err := os.WriteFile(hb, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	s := realSweeper(d)

	// Ordered, not a map: Shutdown removes keeper.heartbeat, so startup MUST run
	// first for the heartbeat assertions below to hold (map order is random).
	for _, step := range []struct {
		name string
		run  func() (*Result, error)
	}{{"startup", s.Startup}, {"shutdown", s.Shutdown}} {
		name := step.name
		res, err := step.run()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(res.Signalled) != 0 {
			t.Errorf("%s signalled %v", name, res.Signalled)
		}
		if !anchor.alive() || !caff.alive() || !holder.alive() {
			t.Errorf("%s: the anchor, caffeinate or a lease holder was disturbed", name)
		}
		if !exists(t, leaseFile(d, "o.ok")) || !exists(t, filepath.Join(d.Path(), lease.FileAssertion)) {
			t.Errorf("%s removed a valid lease or assertion.json", name)
		}
		if exists(t, leaseFile(d, "o.old")) {
			t.Errorf("%s left an expired lease", name)
		}
		if name == "startup" && !exists(t, hb) {
			t.Error("startup removed keeper.heartbeat")
		}
		if name == "shutdown" && exists(t, hb) {
			t.Error("shutdown kept keeper.heartbeat")
		}
	}
}

// signalChild signals only a pid the test spawned. Darwin-only: the sweep
// tests that need a real signal are the only callers.
func signalChild(t *testing.T, pid int, sig syscall.Signal) {
	t.Helper()
	if err := refusePID(pid); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(pid, sig); err != nil {
		t.Fatalf("kill(%d, %v): %v", pid, sig, err)
	}
}
