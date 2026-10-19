package integration

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-keep-awake/internal/keeper"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-keep-awake/internal/lease"
)

// Poll windows from the packet: something that must happen gets 20 seconds,
// something that must not happen a 4-second window.
const (
	mustHappen = 20 * time.Second
	mustNot    = 4 * time.Second
)

// syncBuffer is a goroutine-safe output buffer.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// pair is an anchor and caffeinate the keeper started.
type pair struct {
	anchor, caff lease.ProcDesc
}

// testEnv is one scenario's hermetic world: its own XDG directories, battery
// file and spawn set. Subtests run one at a time.
type testEnv struct {
	t           *testing.T
	stateHome   string
	configHome  string
	home        string
	batteryPath string
	stateDir    string

	mu      sync.Mutex
	spawned map[int]bool
	seen    []pair
}

// newEnv builds a fresh environment, writes the default config and a 64 ac
// battery, and registers the cleanup BEFORE any process is started.
func newEnv(t *testing.T) *testEnv {
	t.Helper()
	requireIntegration(t)
	e := &testEnv{
		t:          t,
		stateHome:  t.TempDir(),
		configHome: t.TempDir(),
		home:       t.TempDir(),
		spawned:    map[int]bool{},
	}
	e.batteryPath = filepath.Join(t.TempDir(), "battery")
	e.stateDir = filepath.Join(e.stateHome, lease.DirName)
	e.setBattery("64 ac")
	e.writeConfig("30s", "0s", "")
	t.Cleanup(e.cleanup)
	return e
}

// env is the explicit child environment: nothing is inherited but PATH.
func (e *testEnv) env(extra ...string) []string {
	return append([]string{
		"XDG_STATE_HOME=" + e.stateHome,
		"XDG_CONFIG_HOME=" + e.configHome,
		"HOME=" + e.home,
		"PATH=" + os.Getenv("PATH"),
		"PG_KEEP_AWAKE_BATTERY_SOURCE=file:" + e.batteryPath,
	}, extra...)
}

// writeConfig writes config.toml with a 200 ms heartbeat. extra is raw TOML
// appended after the [battery] defaults.
func (e *testEnv) writeConfig(watchdog, linger, extra string) {
	e.t.Helper()
	dir := filepath.Join(e.configHome, lease.DirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		e.t.Fatal(err)
	}
	body := "heartbeat_interval = \"200ms\"\n" +
		"watchdog_timeout = \"" + watchdog + "\"\n" +
		"linger = \"" + linger + "\"\n" + extra
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(body), 0o600); err != nil {
		e.t.Fatal(err)
	}
}

// setBattery writes the battery file atomically: a temp file in the same
// directory renamed over it (an in-place write can be read half-written,
// which is a failed read).
func (e *testEnv) setBattery(content string) {
	e.t.Helper()
	tmp := e.batteryPath + ".tmp"
	if err := os.WriteFile(tmp, []byte(content+"\n"), 0o600); err != nil {
		e.t.Fatal(err)
	}
	if err := os.Rename(tmp, e.batteryPath); err != nil {
		e.t.Fatal(err)
	}
}

// removeBattery makes every battery read fail by renaming the file away.
func (e *testEnv) removeBattery() {
	e.t.Helper()
	if err := os.Rename(e.batteryPath, e.batteryPath+".gone"); err != nil {
		e.t.Fatal(err)
	}
}

// proc is a started child.
type proc struct {
	cmd    *exec.Cmd
	stdout *syncBuffer
	stderr *syncBuffer
	done   chan struct{}
	status syscall.WaitStatus
}

// spawn starts a child, adds it to the spawn set and reaps it in the
// background (so it never lingers as a zombie).
func (e *testEnv) spawn(cmd *exec.Cmd) *proc {
	e.t.Helper()
	p := &proc{cmd: cmd, stdout: &syncBuffer{}, stderr: &syncBuffer{}, done: make(chan struct{})}
	cmd.Stdout, cmd.Stderr = p.stdout, p.stderr
	if err := cmd.Start(); err != nil {
		e.t.Fatalf("start %v: %v", cmd.Args, err)
	}
	e.addSpawned(cmd.Process.Pid)
	go func() {
		_ = cmd.Wait()
		if ps := cmd.ProcessState; ps != nil {
			p.status, _ = ps.Sys().(syscall.WaitStatus)
		}
		close(p.done)
	}()
	e.t.Cleanup(func() {
		if p.running() {
			e.signalQuiet(cmd.Process.Pid, syscall.SIGKILL)
			select {
			case <-p.done:
			case <-time.After(5 * time.Second):
			}
		}
	})
	return p
}

func (e *testEnv) addSpawned(pid int) {
	e.mu.Lock()
	e.spawned[pid] = true
	e.mu.Unlock()
}

func (p *proc) pid() int { return p.cmd.Process.Pid }

func (p *proc) running() bool {
	select {
	case <-p.done:
		return false
	default:
		return true
	}
}

// wait blocks until the child exits.
func (p *proc) wait(t *testing.T, timeout time.Duration) syscall.WaitStatus {
	t.Helper()
	select {
	case <-p.done:
		return p.status
	case <-time.After(timeout):
		t.Fatalf("%v did not exit within %v (stderr %q)", p.cmd.Args, timeout, p.stderr.String())
		return 0
	}
}

// keeperProc is a started keeper.
type keeperProc struct {
	*proc
}

// startKeeper starts `pg-keep-awake keeper` (not yet waiting for it to be
// ready) with its stderr captured and logged if the test fails.
func (e *testEnv) startKeeper() *keeperProc {
	e.t.Helper()
	cmd := exec.Command(keepAwakeBin, "keeper")
	cmd.Env = e.env()
	p := e.spawn(cmd)
	e.t.Cleanup(func() {
		if e.t.Failed() {
			e.t.Logf("keeper %d stderr:\n%s", p.pid(), p.stderr.String())
		}
	})
	return &keeperProc{p}
}

// runningKeeper starts a keeper and waits until it is ready.
func (e *testEnv) runningKeeper() *keeperProc {
	e.t.Helper()
	k := e.startKeeper()
	e.waitReady(k)
	return k
}

// waitReady waits until status.json exists and names this keeper's pid.
func (e *testEnv) waitReady(k *keeperProc) {
	e.t.Helper()
	waitFor(e.t, "keeper ready", mustHappen, func() bool {
		if !k.running() {
			e.t.Fatalf("keeper exited early (%v); stderr:\n%s", k.status, k.stderr.String())
		}
		st := e.status()
		return st != nil && st.Keeper.PID == k.pid()
	})
}

// cli runs `pg-keep-awake args...` to completion and returns its exit code,
// stdout and stderr.
func (e *testEnv) cli(args ...string) (int, string, string) {
	e.t.Helper()
	cmd := exec.Command(keepAwakeBin, args...)
	cmd.Env = e.env()
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	code := 0
	if err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			e.t.Fatalf("run %v: %v", cmd.Args, err)
		}
		code = ee.ExitCode()
	}
	return code, out.String(), errb.String()
}

// mustCLI is cli that fails the test unless the exit code is 0.
func (e *testEnv) mustCLI(args ...string) string {
	e.t.Helper()
	code, out, errb := e.cli(args...)
	if code != 0 {
		e.t.Fatalf("pg-keep-awake %v exited %d; stderr %q", args, code, errb)
	}
	return out
}

// acquire creates a pure-timer lease owner.key lasting dur with flags.
func (e *testEnv) acquire(owner, key, dur string, flags ...string) {
	e.t.Helper()
	args := []string{"acquire", "--owner", owner, "--key", key, "--for", dur}
	e.mustCLI(append(args, flags...)...)
}

func (e *testEnv) dir() *lease.Dir {
	e.t.Helper()
	d, err := lease.CheckDir(e.stateDir)
	if err != nil {
		e.t.Fatalf("state directory: %v", err)
	}
	return d
}

func (e *testEnv) leasePath(id string) string {
	return filepath.Join(e.stateDir, "leases", id+".json")
}

func (e *testEnv) leaseExists(id string) bool {
	_, err := os.Lstat(e.leasePath(id))
	return err == nil
}

func (e *testEnv) heartbeatPath() string { return filepath.Join(e.stateDir, "keeper.heartbeat") }

// status reads status.json, or nil when it is not there (or unreadable yet).
func (e *testEnv) status() *lease.StatusFile {
	d, err := lease.CheckDir(e.stateDir)
	if err != nil {
		return nil
	}
	st, err := d.ReadStatus()
	if err != nil {
		return nil
	}
	return st
}

// assertion reads assertion.json, or nil when absent. A pair it reads is
// remembered for the cleanup when it passes the spawn-set checks.
func (e *testEnv) assertion() *lease.AssertionFile {
	d, err := lease.CheckDir(e.stateDir)
	if err != nil {
		return nil
	}
	a, err := d.ReadAssertion()
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			e.t.Logf("reading assertion.json: %v", err)
		}
		return nil
	}
	e.remember(a)
	return a
}

// remember records the pair when assertion.json names it, its start_us still
// matches the live process, the anchor argv is anchor --state-dir <this
// state dir> and the caffeinate argv ends -w <anchor pid>.
func (e *testEnv) remember(a *lease.AssertionFile) {
	if e.ownedPair(a) {
		p := pair{a.Anchor, a.Caffeinate}
		e.mu.Lock()
		defer e.mu.Unlock()
		for _, s := range e.seen {
			if s.anchor.PID == p.anchor.PID && s.anchor.StartUS == p.anchor.StartUS &&
				s.caff.PID == p.caff.PID && s.caff.StartUS == p.caff.StartUS {
				return
			}
		}
		e.seen = append(e.seen, p)
	}
}

func (e *testEnv) ownedPair(a *lease.AssertionFile) bool {
	if a.Anchor.PID <= 1 || a.Caffeinate.PID <= 1 {
		return false
	}
	aref, err := lease.Ref(a.Anchor.PID)
	if err != nil || aref.StartUS != a.Anchor.StartUS {
		return false
	}
	cref, err := lease.Ref(a.Caffeinate.PID)
	if err != nil || cref.StartUS != a.Caffeinate.StartUS {
		return false
	}
	ainfo, err := lease.ProcessInfo(a.Anchor.PID)
	if err != nil || len(ainfo.Argv) < 2 || ainfo.Argv[1] != "anchor" || !argvHas(ainfo.Argv, "--state-dir", e.stateDir) {
		return false
	}
	cinfo, err := lease.ProcessInfo(a.Caffeinate.PID)
	if err != nil || len(cinfo.Argv) < 2 {
		return false
	}
	n := len(cinfo.Argv)
	return cinfo.Argv[n-2] == "-w" && cinfo.Argv[n-1] == strconv.Itoa(a.Anchor.PID)
}

// argvHas reports whether flag is followed by value in argv.
func argvHas(argv []string, flag, value string) bool {
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == flag && argv[i+1] == value {
			return true
		}
	}
	return false
}

// allowed reports whether pid may be signalled.
func (e *testEnv) allowed(pid int) bool {
	if pid <= 1 {
		return false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.spawned[pid] {
		return true
	}
	for _, p := range e.seen {
		for _, d := range []lease.ProcDesc{p.anchor, p.caff} {
			if d.PID != pid {
				continue
			}
			// A recycled pid has a different start time.
			if ref, err := lease.Ref(pid); err == nil && ref.StartUS == d.StartUS {
				return true
			}
		}
	}
	return false
}

// signal is the ONLY way tests signal anything. It fails the test without
// signalling when pid <= 1 or the pid is outside the spawn set: children the
// harness started, plus the anchor and caffeinate of THIS test's keeper that
// assertion.json names with a matching start_us and argv.
func (e *testEnv) signal(pid int, sig syscall.Signal) {
	e.t.Helper()
	if pid > 1 && !e.allowed(pid) {
		e.assertion() // maybe a fresh pair the file now names
	}
	if !e.allowed(pid) {
		e.t.Fatalf("refusing to signal pid %d with %v: not in this test's spawn set", pid, sig)
	}
	if err := syscall.Kill(pid, sig); err != nil && !errors.Is(err, syscall.ESRCH) {
		e.t.Fatalf("signal %d with %v: %v", pid, sig, err)
	}
}

// signalQuiet is signal for cleanup paths: a refusal or error is logged, not
// fatal (Fatalf must not run from a goroutine other than the test's).
func (e *testEnv) signalQuiet(pid int, sig syscall.Signal) {
	if !e.allowed(pid) {
		e.t.Logf("cleanup refusing to signal pid %d: not in the spawn set", pid)
		return
	}
	if err := syscall.Kill(pid, sig); err != nil && !errors.Is(err, syscall.ESRCH) {
		e.t.Logf("cleanup signal %d: %v", pid, err)
	}
}

// cleanup is registered before the keeper starts, so it runs after the
// per-process cleanups: it SIGKILLs every remembered anchor and caffeinate
// and waits up to 5 seconds for all to be gone.
func (e *testEnv) cleanup() {
	e.assertion()
	e.mu.Lock()
	pairs := slices.Clone(e.seen)
	e.mu.Unlock()
	var pids []int
	for _, p := range pairs {
		pids = append(pids, p.anchor.PID, p.caff.PID)
	}
	for _, pid := range pids {
		if alive(pid) {
			e.signalQuiet(pid, syscall.SIGKILL) // refuses a recycled pid
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for _, pid := range pids {
		for time.Now().Before(deadline) && alive(pid) {
			time.Sleep(20 * time.Millisecond)
		}
	}
}

// alive reports whether pid exists and is not a zombie.
func alive(pid int) bool {
	st := lease.SysProber{}.Probe(pid)
	return st.Exists && !st.Zombie
}

// heldPIDs runs pmset -g assertions once and returns the pids of the
// caffeinate assertions it lists (nil when pmset fails).
func heldPIDs() map[int]bool {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, pmsetPath, "-g", "assertions").Output()
	if err != nil {
		return nil
	}
	set := map[int]bool{}
	for _, x := range keeper.ParseAssertions(string(out), 0) {
		if x.Process == "caffeinate" {
			set[x.PID] = true
		}
	}
	return set
}

// held reports whether pmset lists a caffeinate assertion owned by pid.
func held(pid int) bool { return heldPIDs()[pid] }

// waitFor polls cond every 50 ms until it is true.
func waitFor(t *testing.T, what string, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if cond() {
			return
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("timed out after %v waiting for %s", timeout, what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// holdsFor fails if cond is ever false during the window.
func holdsFor(t *testing.T, what string, window time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(window)
	for time.Now().Before(deadline) {
		if !cond() {
			t.Fatalf("%s stopped holding within %v", what, window)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// waitHeld waits until assertion.json exists and its caffeinate is listed by
// pmset, and returns the pair.
func (e *testEnv) waitHeld() *lease.AssertionFile {
	e.t.Helper()
	var a *lease.AssertionFile
	waitFor(e.t, "assertion.json and a held caffeinate", mustHappen, func() bool {
		a = e.assertion()
		return a != nil && held(a.Caffeinate.PID)
	})
	return a
}

// samePair reports whether a names the same anchor and caffeinate as b.
func samePair(a, b *lease.AssertionFile) bool {
	return a != nil && b != nil &&
		a.Anchor.PID == b.Anchor.PID && a.Anchor.StartUS == b.Anchor.StartUS &&
		a.Caffeinate.PID == b.Caffeinate.PID && a.Caffeinate.StartUS == b.Caffeinate.StartUS
}

// gone waits until pid no longer exists or is a zombie.
func gone(t *testing.T, pid int) {
	t.Helper()
	waitFor(t, "pid "+strconv.Itoa(pid)+" to be gone", mustHappen, func() bool { return !alive(pid) })
}

// dirInode returns the inode of path.
func inodeOf(t *testing.T, path string) uint64 {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Sys().(*syscall.Stat_t).Ino
}

// spawnSleeper starts /bin/sleep 600 as a harness child that the harness
// does NOT wait for (so it can become a zombie), and returns it.
func (e *testEnv) spawnSleeper(reap bool) *exec.Cmd {
	e.t.Helper()
	cmd := exec.Command("/bin/sleep", "600")
	cmd.Env = e.env()
	if reap {
		e.spawn(cmd)
		return cmd
	}
	if err := cmd.Start(); err != nil {
		e.t.Fatal(err)
	}
	e.addSpawned(cmd.Process.Pid)
	e.t.Cleanup(func() {
		e.signalQuiet(cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
	})
	return cmd
}
