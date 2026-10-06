package sessionlock

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
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
