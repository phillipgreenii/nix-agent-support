package keeper

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-keep-awake/internal/lease"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-keep-awake/internal/policy"
)

// fproc is one fake process.
type fproc struct {
	argv    []string
	startUS int64
	up      bool
	// ppid and exe are optional: a zero ppid reads as unknown, and an empty
	// exe reads as argv[0] (what a keeper-started process has).
	ppid int
	exe  string
}

func (p *fproc) name() string { return filepath.Base(p.argv[0]) }

// role names the process for the call log: anchor, caffeinate or its name.
func (p *fproc) role() string {
	if len(p.argv) > 1 && p.argv[1] == "anchor" {
		return "anchor"
	}
	return p.name()
}

// fakeProcs is the fake process-control seam. It records the call sequence
// (start, signal, wait) and refuses to signal pid 1 or lower.
type fakeProcs struct {
	t       *testing.T
	mu      sync.Mutex
	procs   map[int]*fproc
	next    int
	calls   []string
	noEnum  bool
	startFn func(argv []string) error
	// ignoreTerm lists pids that survive SIGTERM (only SIGKILL ends them).
	ignoreTerm map[int]bool
	signalled  []int
}

func newFakeProcs(t *testing.T) *fakeProcs {
	return &fakeProcs{t: t, procs: map[int]*fproc{}, next: 100, ignoreTerm: map[int]bool{}}
}

func (f *fakeProcs) tag(pid int) string {
	if p, ok := f.procs[pid]; ok {
		return fmt.Sprintf("%s#%d", p.role(), pid)
	}
	return fmt.Sprintf("?#%d", pid)
}

// seed adds a running process without recording a call.
func (f *fakeProcs) seed(argv []string, startUS int64) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.next++
	f.procs[f.next] = &fproc{argv: slices.Clone(argv), startUS: startUS, up: true}
	return f.next
}

// seedChild adds a running process with a parent and an executable path.
func (f *fakeProcs) seedChild(ppid int, exe string, argv []string) int {
	pid := f.seed(argv, 5_000_000)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.procs[pid].ppid, f.procs[pid].exe = ppid, exe
	return pid
}

func (f *fakeProcs) kill(pid int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.procs[pid].up = false
}

func (f *fakeProcs) log() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

func (f *fakeProcs) reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls, f.signalled = nil, nil
}

func (f *fakeProcs) Start(argv []string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.startFn != nil {
		if err := f.startFn(argv); err != nil {
			return 0, err
		}
	}
	f.next++
	p := &fproc{argv: slices.Clone(argv), startUS: int64(1_000_000 + f.next*10), up: true}
	f.procs[f.next] = p
	f.calls = append(f.calls, fmt.Sprintf("start %s#%d", p.role(), f.next))
	return f.next, nil
}

func (f *fakeProcs) Signal(pid int, sig syscall.Signal) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := refusePID(pid); err != nil {
		f.t.Errorf("signal: %v", err)
		return err
	}
	p, ok := f.procs[pid]
	f.signalled = append(f.signalled, pid)
	name := "TERM"
	if sig == syscall.SIGKILL {
		name = "KILL"
	}
	f.calls = append(f.calls, fmt.Sprintf("%s %s", name, f.tag(pid)))
	if !ok || !p.up {
		return errors.New("no such process")
	}
	if sig == syscall.SIGKILL || !f.ignoreTerm[pid] {
		p.up = false
	}
	return nil
}

func (f *fakeProcs) WaitExit(pid int, _ time.Duration) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "wait "+f.tag(pid))
	p, ok := f.procs[pid]
	return !ok || !p.up
}

func (f *fakeProcs) Probe(pid int) lease.ProcState {
	f.mu.Lock()
	defer f.mu.Unlock()
	if p, ok := f.procs[pid]; ok && p.up {
		return lease.ProcState{Exists: true, StartUS: p.startUS, PPID: p.ppid}
	}
	return lease.ProcState{}
}

func (f *fakeProcs) Info(pid int) (lease.ProcInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.procs[pid]
	if !ok || !p.up {
		return lease.ProcInfo{}, lease.ErrNoProcess
	}
	exe := p.exe
	if exe == "" {
		exe = p.argv[0]
	}
	return lease.ProcInfo{Exe: exe, Name: filepath.Base(exe), Argv: slices.Clone(p.argv)}, nil
}

func (f *fakeProcs) PIDs() ([]int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.noEnum {
		return nil, nil
	}
	var out []int
	for pid, p := range f.procs {
		if p.up {
			out = append(out, pid)
		}
	}
	slices.Sort(out)
	return out, nil
}

// rig is a Reconciler over a fake process table and a real temp state dir.
type rig struct {
	t   *testing.T
	dir *lease.Dir
	fp  *fakeProcs
	rc  *Reconciler
	cfg AssertionConfig
	now time.Time
}

const testExe = "/nix/store/aaaa-pg-keep-awake/bin/pg-keep-awake"

var t0 = time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)

func newRig(t *testing.T) *rig {
	t.Helper()
	d := newDir(t)
	fp := newFakeProcs(t)
	cfg := AssertionConfig{
		HoldPct: 30, ReleasePct: 20,
		Watchdog: 10 * time.Minute, Heartbeat: 10 * time.Second, Linger: 30 * time.Second,
		StateDir: d.Path(), KeeperVersion: "test-1",
	}
	return &rig{t: t, dir: d, fp: fp, rc: NewReconciler(d, fp, AssertionOptions{Exe: testExe}), cfg: cfg, now: t0}
}

func okGate() policy.Status {
	return policy.Status{Gate: policy.GateOK, Reading: policy.Reading{Percent: policy.NoPercent}}
}

func gateOf(g policy.Gate) policy.Status {
	return policy.Status{Gate: g, Reading: policy.Reading{Percent: policy.NoPercent}}
}

func ll(id string, flags ...string) LiveLease {
	return LiveLease{ID: id, Owner: "shim", Key: id, Kind: lease.KindShim, Flags: flags}
}

// inputs builds one tick's inputs at the rig's current time.
func (g *rig) inputs(live []LiveLease, gate policy.Status) ReconcileInputs {
	return ReconcileInputs{Now: g.now, Live: live, Gate: gate, Config: g.cfg}
}

// tick reconciles with the gate ok at the rig's current time.
func (g *rig) tick(live ...LiveLease) ReconcileResult {
	g.t.Helper()
	return g.rc.Reconcile(g.inputs(live, okGate()))
}

// tickG reconciles with the given gate.
func (g *rig) tickG(gate policy.Status, live ...LiveLease) ReconcileResult {
	g.t.Helper()
	return g.rc.Reconcile(g.inputs(live, gate))
}

func (g *rig) advance(d time.Duration) { g.now = g.now.Add(d) }

func noErrs(t *testing.T, res ReconcileResult) {
	t.Helper()
	if len(res.Errs) != 0 {
		t.Fatalf("Reconcile errors: %v", res.Errs)
	}
}

func equalCalls(t *testing.T, got []string, want ...string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Fatalf("calls:\n got %q\nwant %q", got, want)
	}
}

func indexOf(t *testing.T, calls []string, want string) int {
	t.Helper()
	i := slices.Index(calls, want)
	if i < 0 {
		t.Fatalf("call %q not in %q", want, calls)
	}
	return i
}

func hasPrefixCall(calls []string, prefix string) bool {
	return slices.ContainsFunc(calls, func(c string) bool { return strings.HasPrefix(c, prefix) })
}

var errTest = errors.New("test: injected failure")

func mustAtoi(t *testing.T, s string) int {
	t.Helper()
	n, err := strconv.Atoi(s)
	if err != nil {
		t.Fatal(err)
	}
	return n
}
