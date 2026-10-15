//go:build darwin

package keeper

import (
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-keep-awake/internal/lease"
)

// guardedProcs is the production ProcControl, except that it fails the test
// when asked to signal pid 1 or lower or any pid this test did not spawn
// through it.
type guardedProcs struct {
	*OSProcs
	t       *testing.T
	mu      sync.Mutex
	spawned map[int]bool
}

func (g *guardedProcs) Start(argv []string) (int, error) {
	pid, err := g.OSProcs.Start(argv)
	if err == nil {
		g.mu.Lock()
		g.spawned[pid] = true
		g.mu.Unlock()
	}
	return pid, err
}

func (g *guardedProcs) Signal(pid int, sig syscall.Signal) error {
	g.mu.Lock()
	ok := g.spawned[pid]
	g.mu.Unlock()
	if pid <= 1 || !ok {
		g.t.Errorf("refusing to signal pid %d: not in this test's spawn set", pid)
		return fmt.Errorf("pid %d not spawned by this test", pid)
	}
	return g.OSProcs.Signal(pid, sig)
}

// requireCaffeinate skips unless this host can really take an assertion.
func requireCaffeinate(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("/usr/bin/caffeinate"); err != nil {
		t.Skip("/usr/bin/caffeinate is not available")
	}
	if err := exec.Command("/usr/bin/pmset", "-g", "assertions").Run(); err != nil {
		t.Skipf("pmset -g assertions is not usable here (sandbox?): %v", err)
	}
}

func gone(pid int) bool {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if st := (lease.SysProber{}).Probe(pid); !st.Exists || st.Zombie {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

// TestRealProcessesHoldReplaceAndStop runs the real /usr/bin/caffeinate
// against a stand-in anchor (this test binary re-executed as a sleeper that
// carries an anchor argv), and checks start, make-before-break replacement,
// stop, and that a different state_dir's anchor is never touched.
func TestRealProcessesHoldReplaceAndStop(t *testing.T) {
	requireCaffeinate(t)
	t.Setenv("KEEPER_TEST_HELPER", "sleep") // inherited by the children only

	procs := &guardedProcs{OSProcs: NewOSProcs(), t: t, spawned: map[int]bool{}}
	d := newDir(t)
	cfg := AssertionConfig{
		HoldPct: 30, ReleasePct: 20,
		Watchdog: 10 * time.Minute, Heartbeat: 10 * time.Second, Linger: 0,
		StateDir: d.Path(), KeeperVersion: "real-test",
	}
	rc := NewReconciler(d, procs, AssertionOptions{Exe: os.Args[0]})
	t.Cleanup(func() { rc.Stop() })

	// An anchor of a different state_dir, not spawned through procs.
	otherCfg := cfg
	otherCfg.StateDir = t.TempDir()
	foreign := exec.Command(os.Args[0], otherCfg.AnchorArgv(os.Args[0])[1:]...)
	foreign.Env = append(os.Environ(), "KEEPER_TEST_HELPER=sleep")
	if err := foreign.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = foreign.Process.Kill(); _ = foreign.Wait() })

	in := ReconcileInputs{Now: time.Now(), Live: []LiveLease{ll("a", "i")}, Gate: okGate(), Config: cfg}
	res := rc.Reconcile(in)
	noErrs(t, res)
	if !res.Held || !res.Started || res.Adopted || len(res.StoppedOthers) != 0 {
		t.Fatalf("first tick: %+v", res)
	}

	info, err := lease.ProcessInfo(res.AnchorPID)
	if err != nil || len(info.Argv) < 2 || info.Argv[1] != "anchor" {
		t.Fatalf("anchor process = %+v, %v", info, err)
	}
	cinfo, err := lease.ProcessInfo(res.CaffeinatePID)
	if err != nil || cinfo.Name != "caffeinate" || !slices.Equal(cinfo.Argv[1:], []string{"-i", "-w", strconv.Itoa(res.AnchorPID)}) {
		t.Fatalf("caffeinate process = %+v, %v", cinfo, err)
	}
	if pg, err := syscall.Getpgid(res.AnchorPID); err != nil || pg != res.AnchorPID {
		t.Fatalf("anchor must lead its own process group, pgid = %d, %v", pg, err)
	}

	// Flag-set change: make-before-break.
	first := res
	in.Live = []LiveLease{ll("a", "i"), ll("b", "s")}
	res = rc.Reconcile(in)
	noErrs(t, res)
	if !res.Replaced || res.AnchorPID == first.AnchorPID {
		t.Fatalf("replace tick: %+v", res)
	}
	for _, pid := range []int{first.AnchorPID, first.CaffeinatePID} {
		if !gone(pid) {
			t.Errorf("old pid %d still running", pid)
		}
	}
	if st := (lease.SysProber{}).Probe(res.CaffeinatePID); !st.Exists || st.Zombie {
		t.Fatalf("new caffeinate %d is not running", res.CaffeinatePID)
	}

	// A killed caffeinate is restarted by the next tick.
	if err := syscall.Kill(res.CaffeinatePID, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	if !gone(res.CaffeinatePID) {
		t.Fatal("killed caffeinate did not exit")
	}
	anchorPID := res.AnchorPID
	res = rc.Reconcile(in)
	noErrs(t, res)
	if !res.Held || res.AnchorPID != anchorPID || !res.Started {
		t.Fatalf("restart tick: %+v", res)
	}

	// No leases and no linger: stopped.
	in.Live = nil
	res = rc.Reconcile(in)
	noErrs(t, res)
	if res.Held || !res.Stopped {
		t.Fatalf("stop tick: %+v", res)
	}
	if !gone(anchorPID) {
		t.Fatal("anchor still running after stop")
	}

	// The foreign anchor was never signalled.
	if st := (lease.SysProber{}).Probe(foreign.Process.Pid); !st.Exists || st.Zombie {
		t.Fatal("another state_dir's anchor was stopped")
	}
}

// TestRealAdoptionByFile adopts a real pair a first Reconciler left behind,
// the way a restarted keeper does.
func TestRealAdoptionByFile(t *testing.T) {
	requireCaffeinate(t)
	t.Setenv("KEEPER_TEST_HELPER", "sleep")

	procs := &guardedProcs{OSProcs: NewOSProcs(), t: t, spawned: map[int]bool{}}
	d := newDir(t)
	cfg := AssertionConfig{
		HoldPct: 30, ReleasePct: 20,
		Watchdog: 10 * time.Minute, Heartbeat: 10 * time.Second, Linger: time.Minute,
		StateDir: d.Path(),
	}
	first := NewReconciler(d, procs, AssertionOptions{Exe: os.Args[0]})
	in := ReconcileInputs{Now: time.Now(), Live: []LiveLease{ll("a", "i")}, Gate: okGate(), Config: cfg}
	res := first.Reconcile(in)
	noErrs(t, res)
	if !res.Held {
		t.Fatalf("setup: %+v", res)
	}

	// A second keeper (a fresh Reconciler) adopts the pair instead of starting
	// another; the binary path may differ.
	second := NewReconciler(d, procs, AssertionOptions{Exe: "/different/store/path/pg-keep-awake"})
	res2 := second.Reconcile(in)
	noErrs(t, res2)
	if !res2.Adopted || res2.Started || res2.AnchorPID != res.AnchorPID || res2.CaffeinatePID != res.CaffeinatePID {
		t.Fatalf("second keeper: %+v", res2)
	}
	t.Cleanup(func() { second.Stop() })
}
