package sessionlock

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestTryLock_exclusiveThenReusable(t *testing.T) {
	dir := t.TempDir()
	a, err := TryLock(dir, "pg-router-worker-zr-1")
	if err != nil {
		t.Fatalf("first TryLock: %v", err)
	}
	if _, err := TryLock(dir, "pg-router-worker-zr-1"); !errors.Is(err, ErrHeld) {
		t.Fatalf("second TryLock while held = %v, want ErrHeld", err)
	}
	// A different session is independent.
	other, err := TryLock(dir, "pg-router-worker-zr-2")
	if err != nil {
		t.Fatalf("TryLock of another id: %v", err)
	}
	other.Unlock()
	a.Unlock()
	b, err := TryLock(dir, "pg-router-worker-zr-1")
	if err != nil {
		t.Fatalf("TryLock after Unlock: %v", err)
	}
	b.Unlock()
}

func TestUnlock_nilAndDoubleAreSafe(t *testing.T) {
	var l *Lock
	l.Unlock()
	dir := t.TempDir()
	h, err := TryLock(dir, "x")
	if err != nil {
		t.Fatal(err)
	}
	h.Unlock()
	h.Unlock()
}

// Many goroutines racing for one id: never two holders at once (flock
// conflicts per open file description, so this holds inside one process).
func TestTryLock_concurrentHoldersExactlyOne(t *testing.T) {
	dir := t.TempDir()
	const n = 16
	var holders, maxHolders int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	var won int32
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			l, err := TryLock(dir, "contended")
			if err != nil {
				return
			}
			atomic.AddInt32(&won, 1)
			cur := atomic.AddInt32(&holders, 1)
			for {
				m := atomic.LoadInt32(&maxHolders)
				if cur <= m || atomic.CompareAndSwapInt32(&maxHolders, m, cur) {
					break
				}
			}
			atomic.AddInt32(&holders, -1)
			l.Unlock()
		}()
	}
	close(start)
	wg.Wait()
	if maxHolders != 1 {
		t.Errorf("max simultaneous holders = %d, want 1", maxHolders)
	}
	if won < 1 {
		t.Error("nobody acquired the lock")
	}
}

func TestTryLock_idCannotEscapeDir(t *testing.T) {
	dir := t.TempDir()
	l, err := TryLock(dir, "../../evil/id")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Unlock()
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 || strings.Contains(entries[0].Name(), "/") || filepath.Ext(entries[0].Name()) != ".lock" {
		t.Errorf("lock file must stay inside dir with a sanitized name, got %v", entries)
	}
}

func TestTryLock_emptyIDRejected(t *testing.T) {
	if _, err := TryLock(t.TempDir(), ""); err == nil {
		t.Fatal("empty id must be rejected")
	}
}

func TestDir(t *testing.T) {
	if got := Dir("/s"); got != "/s/locks" {
		t.Errorf("Dir = %q", got)
	}
}

// Shared holders coexist and conflict only with an exclusive holder (bead
// pg2-ganjb: live dispatches hold the worktree lock shared, the sweep takes it
// exclusive).
func TestTryRLock_sharedCoexistExclusiveConflicts(t *testing.T) {
	dir := t.TempDir()
	key := WorktreeKey("zr-1")
	a, err := TryRLock(dir, key)
	if err != nil {
		t.Fatalf("first TryRLock: %v", err)
	}
	b, err := TryRLock(dir, key)
	if err != nil {
		t.Fatalf("second shared TryRLock must coexist: %v", err)
	}
	if _, err := TryLock(dir, key); !errors.Is(err, ErrHeld) {
		t.Fatalf("exclusive TryLock under shared holders = %v, want ErrHeld", err)
	}
	a.Unlock()
	if _, err := TryLock(dir, key); !errors.Is(err, ErrHeld) {
		t.Fatalf("exclusive TryLock with one shared holder left = %v, want ErrHeld", err)
	}
	b.Unlock()
	x, err := TryLock(dir, key)
	if err != nil {
		t.Fatalf("exclusive TryLock after all shared released: %v", err)
	}
	if _, err := TryRLock(dir, key); !errors.Is(err, ErrHeld) {
		t.Fatalf("TryRLock under an exclusive holder = %v, want ErrHeld", err)
	}
	x.Unlock()
}

func TestWorktreeKey_isDistinctFromSessionIDs(t *testing.T) {
	if k := WorktreeKey("zr-1"); k != "worktree--zr-1" || strings.HasPrefix(k, "pg-router-") {
		t.Errorf("WorktreeKey = %q", k)
	}
}

// backdate sets a lock file's mtime to age before now.
func backdate(t *testing.T, dir, id string, age time.Duration) string {
	t.Helper()
	p := path(dir, id)
	old := time.Now().Add(-age)
	if err := os.Chtimes(p, old, old); err != nil {
		t.Fatal(err)
	}
	return p
}

func mkIdle(t *testing.T, dir, id string, age time.Duration) string {
	t.Helper()
	l, err := TryLock(dir, id)
	if err != nil {
		t.Fatal(err)
	}
	l.Unlock()
	return backdate(t, dir, id, age)
}

func present(p string) bool { _, err := os.Lstat(p); return err == nil }

func TestSweepStale_removesIdleOldLocksOnly(t *testing.T) {
	dir := t.TempDir()
	oldSession := mkIdle(t, dir, "pg-router-review-zr-1-20260901T000000.000000000", 10*24*time.Hour)
	oldWorktree := mkIdle(t, dir, WorktreeKey("zr-9.2"), 8*24*time.Hour)
	young := mkIdle(t, dir, WorktreeKey("zr-young"), 24*time.Hour)

	// A non-lock file and a directory named like a lock, both old.
	other := filepath.Join(dir, "events.jsonl")
	if err := os.WriteFile(other, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-30 * 24 * time.Hour)
	_ = os.Chtimes(other, old, old)
	sub := filepath.Join(dir, "dir.lock")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.Chtimes(sub, old, old)

	if n := SweepStale(dir, DefaultStaleAge, time.Now()); n != 2 {
		t.Fatalf("SweepStale removed %d, want 2", n)
	}
	for _, p := range []string{oldSession, oldWorktree} {
		if present(p) {
			t.Errorf("%s should have been removed", p)
		}
	}
	for _, p := range []string{young, other, sub} {
		if !present(p) {
			t.Errorf("%s should have been kept", p)
		}
	}
}

func TestSweepStale_keepsHeldLocks(t *testing.T) {
	dir := t.TempDir()
	key := WorktreeKey("zr-held")
	excl, err := TryLock(dir, "exclusive-held")
	if err != nil {
		t.Fatal(err)
	}
	sh1, err := TryRLock(dir, key)
	if err != nil {
		t.Fatal(err)
	}
	// Acquisition touches mtime; age the files AFTER taking the locks so only the
	// held-ness protects them.
	pe := backdate(t, dir, "exclusive-held", 30*24*time.Hour)
	ps := backdate(t, dir, key, 30*24*time.Hour)

	if n := SweepStale(dir, DefaultStaleAge, time.Now()); n != 0 {
		t.Fatalf("SweepStale removed %d held locks, want 0", n)
	}
	if !present(pe) || !present(ps) {
		t.Fatal("a held lock file was removed")
	}
	// The held locks are still effective: nothing else can take them.
	if _, err := TryLock(dir, "exclusive-held"); !errors.Is(err, ErrHeld) {
		t.Errorf("TryLock of a held, swept-over lock = %v, want ErrHeld", err)
	}
	excl.Unlock()
	sh1.Unlock()
	// Released and still old (re-age: the Try* above touched nothing, but be explicit).
	backdate(t, dir, "exclusive-held", 30*24*time.Hour)
	backdate(t, dir, key, 30*24*time.Hour)
	if n := SweepStale(dir, DefaultStaleAge, time.Now()); n != 2 {
		t.Fatalf("after release SweepStale removed %d, want 2", n)
	}
}

func TestTryLock_acquisitionRefreshesMtime(t *testing.T) {
	dir := t.TempDir()
	p := mkIdle(t, dir, WorktreeKey("zr-reused"), 30*24*time.Hour)
	l, err := TryRLock(dir, WorktreeKey("zr-reused")) // a dispatch for a reused bead id
	if err != nil {
		t.Fatal(err)
	}
	l.Unlock()
	if n := SweepStale(dir, DefaultStaleAge, time.Now()); n != 0 || !present(p) {
		t.Fatalf("a just-used lock was swept: removed=%d present=%v", n, present(p))
	}
}

func TestSweepStale_missingDirIsNoop(t *testing.T) {
	if n := SweepStale(filepath.Join(t.TempDir(), "absent"), time.Hour, time.Now()); n != 0 {
		t.Fatalf("removed %d from a missing dir", n)
	}
}

// Openers racing a sweeper that sees every file as stale: the exclusive lock must
// never have two holders, and removal must never recreate or lose mutual
// exclusion (the inode re-verification in tryFlock).
func TestTryLock_exclusionHoldsUnderConcurrentSweep(t *testing.T) {
	dir := t.TempDir()
	far := time.Now().Add(365 * 24 * time.Hour)
	var holders, violations int32
	stop := make(chan struct{})
	var sw sync.WaitGroup
	sw.Add(1)
	go func() {
		defer sw.Done()
		for {
			select {
			case <-stop:
				return
			default:
				SweepStale(dir, time.Hour, far)
			}
		}
	}()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 400; j++ {
				l, err := TryLock(dir, "hot")
				if err != nil {
					continue // ErrHeld
				}
				if atomic.AddInt32(&holders, 1) != 1 {
					atomic.AddInt32(&violations, 1)
				}
				atomic.AddInt32(&holders, -1)
				l.Unlock()
			}
		}()
	}
	wg.Wait()
	close(stop)
	sw.Wait()
	if v := atomic.LoadInt32(&violations); v != 0 {
		t.Fatalf("%d simultaneous exclusive holders under concurrent sweep", v)
	}
}

// The deterministic form of the open-then-unlink race: the file is unlinked in
// the window between an opener's open and its flock. The opener must notice that
// it locked a dead inode and retry on a fresh file, so a second opener is still
// excluded.
func TestTryLock_retriesWhenFileUnlinkedBetweenOpenAndFlock(t *testing.T) {
	dir := t.TempDir()
	p := path(dir, "raced")
	fired := false
	afterOpenHook = func() {
		if !fired {
			fired = true
			if err := os.Remove(p); err != nil {
				t.Errorf("simulated sweep unlink: %v", err)
			}
		}
	}
	t.Cleanup(func() { afterOpenHook = nil })
	l, err := TryLock(dir, "raced")
	afterOpenHook = nil
	if err != nil {
		t.Fatalf("TryLock: %v", err)
	}
	defer l.Unlock()
	if !fired {
		t.Fatal("hook never fired")
	}
	if !present(p) {
		t.Fatal("the retried lock did not recreate the lock file")
	}
	if _, err := TryLock(dir, "raced"); !errors.Is(err, ErrHeld) {
		t.Fatalf("second TryLock after a raced first = %v, want ErrHeld (first holds a dead inode)", err)
	}
}
