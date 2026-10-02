package orphan

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// fakeParent scripts Getppid: the first call returns first, every later call
// returns whatever set last stored.
type fakeParent struct {
	calls atomic.Int64
	first int
	later atomic.Int64
}

func (f *fakeParent) Getppid() int {
	if f.calls.Add(1) == 1 {
		return f.first
	}
	return int(f.later.Load())
}

type exitRecorder struct {
	mu    sync.Mutex
	codes []int
	log   []string
	done  chan struct{}
	once  sync.Once
}

func newExitRecorder() *exitRecorder { return &exitRecorder{done: make(chan struct{})} }

func (e *exitRecorder) Cleanup() {
	e.mu.Lock()
	e.log = append(e.log, "cleanup")
	e.mu.Unlock()
}

func (e *exitRecorder) Exit(code int) {
	e.mu.Lock()
	e.codes = append(e.codes, code)
	e.log = append(e.log, "exit")
	e.mu.Unlock()
	e.once.Do(func() { close(e.done) })
}

func (e *exitRecorder) snapshot() ([]int, []string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]int(nil), e.codes...), append([]string(nil), e.log...)
}

func (e *exitRecorder) waitExit(t *testing.T) {
	t.Helper()
	select {
	case <-e.done:
	case <-time.After(5 * time.Second):
		t.Fatal("never exited although orphaned")
	}
}

const fast = 2 * time.Millisecond

func TestOrphanedParentRunsCleanupThenExits(t *testing.T) {
	for name, newParent := range map[string]int{"reparented to init": 1, "reparented to a subreaper": 4242} {
		t.Run(name, func(t *testing.T) {
			p := &fakeParent{first: 100}
			p.later.Store(int64(newParent))
			rec := newExitRecorder()
			stop := WatchWith(Options{Interval: fast, Getppid: p.Getppid, Cleanup: rec.Cleanup, Exit: rec.Exit})
			defer stop()
			rec.waitExit(t)
			time.Sleep(20 * fast) // more ticks must not run cleanup or exit again
			codes, log := rec.snapshot()
			if len(codes) != 1 || codes[0] != ExitCode || ExitCode != 1 {
				t.Errorf("exit codes = %v; want exactly [1]", codes)
			}
			if strings.Join(log, ",") != "cleanup,exit" {
				t.Errorf("order = %v; cleanup must run once, before exit", log)
			}
		})
	}
}

func TestSameParentNeverExits(t *testing.T) {
	p := &fakeParent{first: 100}
	p.later.Store(100)
	rec := newExitRecorder()
	stop := WatchWith(Options{Interval: fast, Getppid: p.Getppid, Cleanup: rec.Cleanup, Exit: rec.Exit})
	defer stop()
	deadline := time.Now().Add(10 * time.Second) // generous: a loaded machine must not make this flaky
	for time.Now().Before(deadline) && p.calls.Load() < 10 {
		time.Sleep(fast)
	}
	if p.calls.Load() < 10 {
		t.Fatalf("watcher barely ran: %d checks", p.calls.Load())
	}
	if codes, log := rec.snapshot(); len(codes) != 0 || len(log) != 0 {
		t.Errorf("a live parent must not trigger cleanup or exit: %v %v", codes, log)
	}
}

// A process that starts with parent 1 never had a wrapper to lose.
func TestStartedUnderInitIsNeverOrphaned(t *testing.T) {
	p := &fakeParent{first: 1}
	p.later.Store(1)
	rec := newExitRecorder()
	stop := WatchWith(Options{Interval: fast, Getppid: p.Getppid, Exit: rec.Exit})
	defer stop()
	for p.calls.Load() < 10 {
		time.Sleep(fast)
	}
	if codes, _ := rec.snapshot(); len(codes) != 0 {
		t.Errorf("exited although the parent never changed: %v", codes)
	}
}

func TestStopEndsTheWatchAndIsIdempotent(t *testing.T) {
	p := &fakeParent{first: 100}
	p.later.Store(100)
	rec := newExitRecorder()
	stop := WatchWith(Options{Interval: fast, Getppid: p.Getppid, Cleanup: rec.Cleanup, Exit: rec.Exit})
	for p.calls.Load() < 3 {
		time.Sleep(fast)
	}
	stop()
	stop() // must not panic
	time.Sleep(10 * fast)
	seen := p.calls.Load()
	time.Sleep(10 * fast)
	if p.calls.Load() > seen+1 {
		t.Errorf("still polling after stop: %d -> %d", seen, p.calls.Load())
	}
	// Orphaning after stop is ignored.
	p.later.Store(1)
	time.Sleep(10 * fast)
	if codes, _ := rec.snapshot(); len(codes) != 0 {
		t.Errorf("exited after stop: %v", codes)
	}
}

func TestNilCleanupIsAllowed(t *testing.T) {
	p := &fakeParent{first: 100}
	p.later.Store(1)
	rec := newExitRecorder()
	stop := WatchWith(Options{Interval: fast, Getppid: p.Getppid, Exit: rec.Exit})
	defer stop()
	rec.waitExit(t)
	if codes, _ := rec.snapshot(); len(codes) != 1 || codes[0] != 1 {
		t.Errorf("codes = %v", codes)
	}
}

func TestDefaults(t *testing.T) {
	if DefaultInterval != time.Second {
		t.Errorf("DefaultInterval = %v; the contract says every second", DefaultInterval)
	}
	// The real parent is alive for the whole test, so the real Watch must be quiet.
	stop := Watch(func() { t.Error("cleanup ran although the parent is alive") })
	stop()
}

// ---- a real orphan ------------------------------------------------------
//
// The test binary re-executes itself in two roles. "parent" starts a "child"
// that runs the real Watch, waits until the child is watching, then exits,
// orphaning it. The child must run its cleanup and go away.

func TestOrphanHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	dir := os.Getenv("ORPHAN_TEST_DIR")
	switch os.Getenv("GO_ORPHAN_ROLE") {
	case "parent":
		child := exec.Command(os.Args[0], "-test.run=^TestOrphanHelperProcess$")
		child.Env = append(os.Environ(), "GO_ORPHAN_ROLE=child")
		if err := child.Start(); err != nil {
			os.Exit(2)
		}
		for i := 0; i < 500; i++ {
			if _, err := os.Stat(filepath.Join(dir, "watching")); err == nil {
				os.Exit(0) // orphan the child
			}
			time.Sleep(10 * time.Millisecond)
		}
		os.Exit(3)
	case "child":
		time.AfterFunc(30*time.Second, func() { os.Exit(4) }) // never leak a stray process
		WatchWith(Options{
			Interval: 20 * time.Millisecond,
			Cleanup: func() {
				_ = os.WriteFile(filepath.Join(dir, "cleanup"), []byte("ran"), 0o600)
			},
		})
		_ = os.WriteFile(filepath.Join(dir, "pid"), []byte(strconv.Itoa(os.Getpid())), 0o600)
		_ = os.WriteFile(filepath.Join(dir, "watching"), nil, 0o600)
		select {}
	}
}

func TestRealOrphanRunsCleanupAndExits(t *testing.T) {
	dir := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestOrphanHelperProcess$")
	cmd.Env = append(os.Environ(), "GO_WANT_HELPER_PROCESS=1", "GO_ORPHAN_ROLE=parent", "ORPHAN_TEST_DIR="+dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("parent role failed: %v\n%s", err, out)
	}
	var pid int
	if b, err := os.ReadFile(filepath.Join(dir, "pid")); err == nil {
		pid, _ = strconv.Atoi(string(b))
	}
	if pid == 0 {
		t.Fatal("the child never reported its pid")
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(dir, "cleanup")); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "cleanup")); err != nil || string(b) != "ran" {
		t.Fatalf("the orphaned child did not run its cleanup hook: %q %v", b, err)
	}
	for time.Now().Before(deadline) {
		if gone(pid) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Error("the orphaned child is still running after its cleanup")
}

// gone reports whether pid has exited. A zombie that nothing has reaped yet
// (possible when pid 1 of a build sandbox does not wait) counts as exited.
func gone(pid int) bool {
	if err := syscall.Kill(pid, 0); err != nil {
		return true
	}
	stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return false // no /proc (macOS): kill(0) succeeded, so it is alive
	}
	// "pid (comm) S ...": the state follows the last ')'.
	i := strings.LastIndexByte(string(stat), ')')
	return i >= 0 && i+2 < len(stat) && stat[i+2] == 'Z'
}
