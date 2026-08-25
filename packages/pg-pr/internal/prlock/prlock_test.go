package prlock

import (
	"context"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// TestAcquire_SameKeySerializes is the mutual-exclusion half: a second
// Acquire for the SAME key must not succeed while the first holds it, and
// must succeed once the first releases. Each Acquire call opens its own fd
// via os.OpenFile, so — exactly like internal/sync/daemon_test.go's
// TestDaemon_LockHeldReturnsError — this exercises real cross-process BSD
// flock semantics (ownership by open file description, not by process) even
// though both calls happen to run in the same test process.
func TestAcquire_SameKeySerializes(t *testing.T) {
	l := New(Options{LockDir: t.TempDir()})
	ctx := context.Background()

	release1, err := l.Acquire(ctx, "o/r#1")
	if err != nil {
		t.Fatalf("first Acquire: %v", err)
	}

	acquired := make(chan struct{})
	go func() {
		release2, err := l.Acquire(ctx, "o/r#1")
		if err != nil {
			t.Errorf("second Acquire: %v", err)
			return
		}
		close(acquired)
		release2()
	}()

	select {
	case <-acquired:
		t.Fatal("second Acquire succeeded while the first still held the lock")
	case <-time.After(150 * time.Millisecond):
		// Expected: still blocked.
	}

	release1()

	select {
	case <-acquired:
	case <-time.After(2 * time.Second):
		t.Fatal("second Acquire never succeeded after the first released")
	}
}

// TestAcquire_ActuallyHoldsOSLevelFlock proves Acquire's success means the
// OS-level lock is held, not just internal bookkeeping: a wholly independent
// fd opened directly on the same lock file must itself fail a non-blocking
// flock attempt while release() has not been called.
func TestAcquire_ActuallyHoldsOSLevelFlock(t *testing.T) {
	dir := t.TempDir()
	l := New(Options{LockDir: dir})

	release, err := l.Acquire(context.Background(), "o/r#9")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer release()

	path := filepath.Join(dir, SanitizeKey("o/r#9")+".lock")
	f, err := os.OpenFile(path, os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("open lock file directly: %v", err)
	}
	defer func() { _ = f.Close() }()

	if ferr := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); ferr == nil {
		_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
		t.Fatal("an independent fd was able to flock the same file while Acquire holds it")
	}
}

// TestAcquire_TimesOutWhenContended drives Timeout down to milliseconds and
// confirms Acquire gives up with a typed error (wrapping ErrTimeout) rather
// than blocking forever or silently proceeding unlocked.
func TestAcquire_TimesOutWhenContended(t *testing.T) {
	l := New(Options{
		LockDir:      t.TempDir(),
		Timeout:      100 * time.Millisecond,
		pollInterval: 5 * time.Millisecond,
	})
	ctx := context.Background()

	release1, err := l.Acquire(ctx, "o/r#2")
	if err != nil {
		t.Fatalf("first Acquire: %v", err)
	}
	defer release1()

	start := time.Now()
	release2, err := l.Acquire(ctx, "o/r#2")
	elapsed := time.Since(start)

	if release2 != nil {
		t.Fatal("second Acquire returned a non-nil release despite giving up")
	}
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("second Acquire error = %v, want an error wrapping ErrTimeout", err)
	}
	if elapsed < 100*time.Millisecond {
		t.Fatalf("gave up before the configured Timeout elapsed: %s", elapsed)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("gave up far later than the configured Timeout: %s", elapsed)
	}
}

// TestAcquire_DistinctKeysStayConcurrent guards against an over-broad lock:
// while key "o/r#1" is held, Acquire on the DIFFERENT key "o/r#2" must
// succeed promptly rather than wait behind the first key's holder.
func TestAcquire_DistinctKeysStayConcurrent(t *testing.T) {
	l := New(Options{LockDir: t.TempDir()})
	ctx := context.Background()

	release1, err := l.Acquire(ctx, "o/r#1")
	if err != nil {
		t.Fatalf("Acquire o/r#1: %v", err)
	}
	defer release1()

	done := make(chan error, 1)
	go func() {
		release2, err := l.Acquire(ctx, "o/r#2")
		if err == nil {
			release2()
		}
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Acquire of a DIFFERENT key failed: %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Acquire of a different key did not complete promptly — keys are contending")
	}
}

// TestAcquire_ContextCancelWhileWaiting confirms Acquire honours ctx
// cancellation while polling for a contended key, rather than sleep-polling
// until its own Timeout regardless of ctx.
func TestAcquire_ContextCancelWhileWaiting(t *testing.T) {
	l := New(Options{
		LockDir:      t.TempDir(),
		Timeout:      2 * time.Second,
		pollInterval: 5 * time.Millisecond,
	})

	release1, err := l.Acquire(context.Background(), "o/r#3")
	if err != nil {
		t.Fatalf("first Acquire: %v", err)
	}
	defer release1()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := l.Acquire(ctx, "o/r#3")
		done <- err
	}()

	// Give the waiting Acquire time to enter its poll loop before cancelling.
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Acquire error after ctx cancel = %v, want context.Canceled", err)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("Acquire did not return promptly after ctx cancellation — Timeout is 2s, so this would be sleep-polling past ctx")
	}
}

// TestSanitizeKey pins the encoding as reversible (percent-encoding), which
// is what makes it injective: SanitizeKey followed by url.QueryUnescape
// recovers the original key exactly.
func TestSanitizeKey(t *testing.T) {
	for _, key := range []string{
		"owner/name#123",
		"org/team/repo#7",
		"o/r#999999",
		"no-special-chars",
	} {
		t.Run(key, func(t *testing.T) {
			got := SanitizeKey(key)
			if strings.ContainsAny(got, "/\x00") {
				t.Fatalf("SanitizeKey(%q) = %q still contains a path separator or NUL", key, got)
			}
			back, err := url.QueryUnescape(got)
			if err != nil {
				t.Fatalf("SanitizeKey(%q) = %q is not reversible: %v", key, got, err)
			}
			if back != key {
				t.Fatalf("SanitizeKey(%q) roundtrip = %q, want %q", key, back, key)
			}
		})
	}
}

// TestSanitizeKey_DistinctKeysNeverCollide is the injectivity check the bead
// spec calls out explicitly: two different real PR keys — including ones
// built from the same characters with the separators rearranged, which is
// exactly the shape a naive "replace / and # with _" scheme would collapse —
// must sanitise to two different filenames.
func TestSanitizeKey_DistinctKeysNeverCollide(t *testing.T) {
	keys := []string{
		"o/r#1",
		"o#r/1", // same chars, separators swapped — a replace-based scheme collides these
		"o_r_1", // literal underscore form — what a replace-based scheme would produce
		"o/r#12",
		"o/r#1x",
		"a/b#1",
		"a/b#10",
		"owner/name#123",
		"owner/name#1234",
	}
	seen := map[string]string{}
	for _, k := range keys {
		s := SanitizeKey(k)
		if prev, ok := seen[s]; ok && prev != k {
			t.Fatalf("SanitizeKey collision: %q and %q both map to %q", prev, k, s)
		}
		seen[s] = k
	}
}

// TestNew_AppliesDefaults pins the zero-value-safe defaults: an empty
// Options resolves to DefaultTimeout and a LockDir under a "pg-pr/locks"
// path (sibling of internal/sync's daemon.lock root).
func TestNew_AppliesDefaults(t *testing.T) {
	l := New(Options{})
	if l.timeout != DefaultTimeout {
		t.Errorf("default Timeout = %s, want %s", l.timeout, DefaultTimeout)
	}
	if l.lockDir == "" {
		t.Fatal("default LockDir is empty")
	}
	if got, want := filepath.Base(l.lockDir), "locks"; got != want {
		t.Errorf("default LockDir base = %q, want %q", got, want)
	}
	if got, want := filepath.Base(filepath.Dir(l.lockDir)), "pg-pr"; got != want {
		t.Errorf("default LockDir parent = %q, want %q", got, want)
	}
}

// TestAcquire_CreatesDirAndFileWithExpectedModes pins the directory/file
// mode conventions the bead spec requires to match internal/sync/daemon.go's
// daemon.lock (dir 0o700, file 0o600). Neither mode has group/other bits set
// to begin with, so a typical process umask (022, 077, 002, ...) cannot
// widen the observed permissions and this assertion is umask-independent.
func TestAcquire_CreatesDirAndFileWithExpectedModes(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "locks-subdir")
	l := New(Options{LockDir: dir})

	release, err := l.Acquire(context.Background(), "o/r#5")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer release()

	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat lock dir: %v", err)
	}
	if perm := dirInfo.Mode().Perm(); perm != 0o700 {
		t.Errorf("lock dir mode = %o, want 0700", perm)
	}

	path := filepath.Join(dir, SanitizeKey("o/r#5")+".lock")
	fileInfo, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat lock file: %v", err)
	}
	if perm := fileInfo.Mode().Perm(); perm != 0o600 {
		t.Errorf("lock file mode = %o, want 0600", perm)
	}
}

// TestXdgRuntimeDir_FallsBackToTempDir mirrors internal/sync's identically
// named test for its own copy of the same small helper.
func TestXdgRuntimeDir_FallsBackToTempDir(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "")
	if got := xdgRuntimeDir(); got != os.TempDir() {
		t.Fatalf("xdgRuntimeDir without env: got %q want %q", got, os.TempDir())
	}
	t.Setenv("XDG_RUNTIME_DIR", "/custom/runtime")
	if got := xdgRuntimeDir(); got != "/custom/runtime" {
		t.Fatalf("xdgRuntimeDir with env: got %q", got)
	}
}
