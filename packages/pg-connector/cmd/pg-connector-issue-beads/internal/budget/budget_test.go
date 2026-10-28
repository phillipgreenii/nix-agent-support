package budget

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func newBudget(t *testing.T, cfg Config) *Budget {
	t.Helper()
	if cfg.StateDir == "" {
		cfg.StateDir = t.TempDir()
	}
	b, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func mustAcquire(t *testing.T, b *Budget, write bool) *Slot {
	t.Helper()
	s, err := b.Acquire(context.Background(), write)
	if err != nil {
		t.Fatalf("Acquire(write=%v): %v", write, err)
	}
	if s.File() == nil {
		t.Fatalf("Acquire(write=%v) failed open: %v", write, s.FailOpenErr())
	}
	t.Cleanup(s.Release)
	return s
}

func TestNewRejectsBadConfig(t *testing.T) {
	for name, cfg := range map[string]Config{
		"zero concurrent":       {MaxConcurrent: 0},
		"negative reserve":      {MaxConcurrent: 2, WriteReserve: -1},
		"reserve equals max":    {MaxConcurrent: 2, WriteReserve: 2},
		"reserve exceeds max":   {MaxConcurrent: 2, WriteReserve: 3},
		"negative concurrent":   {MaxConcurrent: -1},
		"reserve with one slot": {MaxConcurrent: 1, WriteReserve: 1},
	} {
		if _, err := New(cfg); err == nil {
			t.Errorf("%s: New returned no error", name)
		}
	}
	if _, err := New(Config{MaxConcurrent: 1, WriteReserve: 0}); err != nil {
		t.Errorf("valid config rejected: %v", err)
	}
}

// Case 1: all slots held, a third read waits WaitBound and is denied.
func TestAcquireDeniedAfterWaitBound(t *testing.T) {
	b := newBudget(t, Config{MaxConcurrent: 2, WaitBound: 150 * time.Millisecond})
	mustAcquire(t, b, false)
	mustAcquire(t, b, false)
	start := time.Now()
	s, err := b.Acquire(context.Background(), false)
	elapsed := time.Since(start)
	if !errors.Is(err, ErrDenied) || s != nil {
		t.Fatalf("got slot=%v err=%v, want ErrDenied", s, err)
	}
	if elapsed < 140*time.Millisecond {
		t.Errorf("denied after %v, want at least the wait bound", elapsed)
	}
}

func TestZeroWaitBoundDeniesImmediately(t *testing.T) {
	b := newBudget(t, Config{MaxConcurrent: 1})
	mustAcquire(t, b, false)
	start := time.Now()
	if _, err := b.Acquire(context.Background(), false); !errors.Is(err, ErrDenied) {
		t.Fatalf("err = %v, want ErrDenied", err)
	}
	if time.Since(start) > 100*time.Millisecond {
		t.Errorf("zero wait bound waited %v", time.Since(start))
	}
}

func TestAcquireWaitsForARelease(t *testing.T) {
	b := newBudget(t, Config{MaxConcurrent: 1, WaitBound: 5 * time.Second})
	first := mustAcquire(t, b, false)
	go func() { time.Sleep(100 * time.Millisecond); first.Release() }()
	s, err := b.Acquire(context.Background(), false)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer s.Release()
	if s.Waited() < 50*time.Millisecond {
		t.Errorf("Waited = %v, want the time spent waiting", s.Waited())
	}
}

func TestAcquireReturnsContextError(t *testing.T) {
	b := newBudget(t, Config{MaxConcurrent: 1, WaitBound: 10 * time.Second})
	mustAcquire(t, b, false)
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := b.Acquire(ctx, false)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Errorf("did not stop at the context end: %v", time.Since(start))
	}
}

// Case 2: with 3 slots and 2 reserved, a read takes one slot only; a write takes all three.
func TestWriteReserve(t *testing.T) {
	b := newBudget(t, Config{MaxConcurrent: 3, WriteReserve: 2, WaitBound: 0})
	mustAcquire(t, b, false)
	if _, err := b.Acquire(context.Background(), false); !errors.Is(err, ErrDenied) {
		t.Fatalf("second read: err = %v, want ErrDenied", err)
	}
	mustAcquire(t, b, true)
	mustAcquire(t, b, true)
	if _, err := b.Acquire(context.Background(), true); !errors.Is(err, ErrDenied) {
		t.Fatalf("fourth call: err = %v, want ErrDenied", err)
	}
}

func TestWritesAloneTakeAllSlots(t *testing.T) {
	b := newBudget(t, Config{MaxConcurrent: 3, WriteReserve: 2, WaitBound: 0})
	for i := 0; i < 3; i++ {
		mustAcquire(t, b, true)
	}
	if _, err := b.Acquire(context.Background(), true); !errors.Is(err, ErrDenied) {
		t.Fatalf("err = %v, want ErrDenied", err)
	}
}

// Case 4: Release frees the slot, and a second Release is harmless.
func TestReleaseFreesSlot(t *testing.T) {
	b := newBudget(t, Config{MaxConcurrent: 1})
	s := mustAcquire(t, b, false)
	if _, err := b.Acquire(context.Background(), false); !errors.Is(err, ErrDenied) {
		t.Fatalf("while held: err = %v, want ErrDenied", err)
	}
	s.Release()
	s.Release()
	s2 := mustAcquire(t, b, false)
	// A stale second Release of the first slot must not free the second one.
	s.Release()
	if _, err := b.Acquire(context.Background(), false); !errors.Is(err, ErrDenied) {
		t.Fatalf("after stale Release: err = %v, want ErrDenied", err)
	}
	s2.Release()
}

// Case 5: an unusable state directory fails open and never errors.
func TestUnwritableStateDirFailsOpen(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	root := t.TempDir()
	ro := filepath.Join(root, "ro")
	if err := os.Mkdir(ro, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(ro, 0o700) })

	for name, dir := range map[string]string{
		"missing subdirectory of a read-only directory": filepath.Join(ro, "state"),
		"existing read-only directory without slots":    ro,
	} {
		b := newBudget(t, Config{MaxConcurrent: 2, WaitBound: time.Second, StateDir: dir})
		s, err := b.Acquire(context.Background(), false)
		if err != nil {
			t.Fatalf("%s: Acquire returned an error: %v", name, err)
		}
		if s.File() != nil || !s.FailedOpen() || s.FailOpenErr() == nil {
			t.Errorf("%s: want a fail-open slot with a nil file, got file=%v failedOpen=%v", name, s.File(), s.FailedOpen())
		}
		if s.Waited() != 0 {
			t.Errorf("%s: Waited = %v, want 0", name, s.Waited())
		}
		s.Release()
		s.Release()
		// Fail-open slots never hold anything: any number can be taken.
		s2, err := b.Acquire(context.Background(), true)
		if err != nil || !s2.FailedOpen() {
			t.Errorf("%s: second Acquire = %v, %v", name, s2, err)
		}
	}
}

func TestHeldSlotIsNotFailedOpen(t *testing.T) {
	b := newBudget(t, Config{MaxConcurrent: 1})
	s := mustAcquire(t, b, false)
	if s.FailedOpen() || s.FailOpenErr() != nil {
		t.Errorf("a real slot reports failed open")
	}
}

func TestDefaultStateDirIgnoresHomeAndXDG(t *testing.T) {
	t.Setenv("HOME", "/nonexistent/home-a")
	t.Setenv("XDG_STATE_HOME", "/nonexistent/xdg-a")
	a := DefaultStateDir("grp")
	t.Setenv("HOME", "/nonexistent/home-b")
	t.Setenv("XDG_STATE_HOME", "/nonexistent/xdg-b")
	c := DefaultStateDir("grp")
	if a != c {
		t.Errorf("state dir changed with the environment: %q vs %q", a, c)
	}
	if filepath.Base(a) != "grp" {
		t.Errorf("state dir %q does not end in the group", a)
	}
	if filepath.Base(DefaultStateDir("")) != "default" {
		t.Errorf("empty group does not map to the default directory")
	}
	b, err := New(Config{MaxConcurrent: 1, Group: "grp"})
	if err != nil || b.Dir() != a {
		t.Errorf("New with a group: dir=%v err=%v, want %q", b.Dir(), err, a)
	}
	b, _ = New(Config{MaxConcurrent: 1, StateDir: "/explicit"})
	if b.Dir() != "/explicit" {
		t.Errorf("explicit StateDir not used verbatim: %q", b.Dir())
	}
}

// windowCfg is a budget whose slots never limit (so only the window does).
func windowCfg(t *testing.T, perMinute int, window, wait time.Duration) *Budget {
	t.Helper()
	return newBudget(t, Config{MaxConcurrent: 20, MaxPerMinute: perMinute, Window: window, WaitBound: wait})
}

func TestNewRejectsBadWindowConfig(t *testing.T) {
	if _, err := New(Config{MaxConcurrent: 2, MaxPerMinute: -1}); err == nil {
		t.Error("negative MaxPerMinute accepted")
	}
	if _, err := New(Config{MaxConcurrent: 2, MaxPerMinute: 1, Window: -time.Second}); err == nil {
		t.Error("negative Window accepted")
	}
}

func TestWindowDeniesAfterWaitBoundOverCap(t *testing.T) {
	b := windowCfg(t, 2, time.Minute, 150*time.Millisecond)
	mustAcquire(t, b, false)
	mustAcquire(t, b, false)
	start := time.Now()
	if _, err := b.Acquire(context.Background(), false); !errors.Is(err, ErrDenied) {
		t.Fatalf("third call: err = %v, want ErrDenied", err)
	}
	if got := time.Since(start); got < 140*time.Millisecond {
		t.Errorf("denied after %v, want a wait of about the 150ms bound", got)
	}
}

func TestWindowSlides(t *testing.T) {
	b := windowCfg(t, 1, time.Second, 20*time.Millisecond)
	mustAcquire(t, b, false)
	if _, err := b.Acquire(context.Background(), false); !errors.Is(err, ErrDenied) {
		t.Fatalf("inside the window: err = %v, want ErrDenied", err)
	}
	time.Sleep(1100 * time.Millisecond)
	mustAcquire(t, b, false)
}

func TestWindowWaitsForSlideWithinBound(t *testing.T) {
	b := windowCfg(t, 1, 200*time.Millisecond, 5*time.Second)
	mustAcquire(t, b, false)
	s, err := b.Acquire(context.Background(), false)
	if err != nil {
		t.Fatalf("second call should wait for the slide: %v", err)
	}
	defer s.Release()
	if s.Waited() < 150*time.Millisecond {
		t.Errorf("waited %v, want about the 200ms window", s.Waited())
	}
}

// A window denial releases the slot it had taken and records no entry.
func TestWindowDenialReleasesSlotAndTakesNoEntry(t *testing.T) {
	dir := t.TempDir()
	b := newBudget(t, Config{MaxConcurrent: 1, MaxPerMinute: 1, StateDir: dir, Window: time.Second})
	first := mustAcquire(t, b, false)
	first.Release()
	// Slot is free again but the window is full: denied, slot not held.
	if _, err := b.Acquire(context.Background(), false); !errors.Is(err, ErrDenied) {
		t.Fatalf("err = %v, want ErrDenied", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, windowFileName))
	if err != nil {
		t.Fatal(err)
	}
	if n := len(strings.Fields(string(raw))); n != 1 {
		t.Errorf("window holds %d entries after a denial, want 1", n)
	}
	time.Sleep(1100 * time.Millisecond)
	// Had the denial kept the slot this would be denied by the semaphore.
	mustAcquire(t, b, false)
}

// Two budgets over one state directory (one Group) share the window.
func TestWindowIsSharedPerStateDir(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{MaxConcurrent: 5, MaxPerMinute: 1, StateDir: dir, WaitBound: 20 * time.Millisecond}
	mustAcquire(t, newBudget(t, cfg), false)
	if _, err := newBudget(t, cfg).Acquire(context.Background(), false); !errors.Is(err, ErrDenied) {
		t.Fatalf("second instance: err = %v, want ErrDenied", err)
	}
}

func TestWindowDisabledWhenZero(t *testing.T) {
	dir := t.TempDir()
	b := newBudget(t, Config{MaxConcurrent: 20, StateDir: dir})
	for i := 0; i < 10; i++ {
		mustAcquire(t, b, false)
	}
	if _, err := os.Stat(filepath.Join(dir, windowFileName)); !os.IsNotExist(err) {
		t.Errorf("window file exists with MaxPerMinute 0: %v", err)
	}
}

func TestWindowConcurrentCallersNeverExceedCap(t *testing.T) {
	b := windowCfg(t, 5, time.Minute, 0)
	var admitted atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, err := b.Acquire(context.Background(), false)
			if err == nil {
				admitted.Add(1)
				s.Release()
			}
		}()
	}
	wg.Wait()
	if got := admitted.Load(); got != 5 {
		t.Errorf("admitted %d, want exactly 5", got)
	}
}

func TestWindowContextCancelled(t *testing.T) {
	b := windowCfg(t, 1, time.Minute, 5*time.Second)
	mustAcquire(t, b, false)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := b.Acquire(ctx, false); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want DeadlineExceeded", err)
	}
}
