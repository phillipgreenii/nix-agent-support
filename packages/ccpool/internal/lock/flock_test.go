package lock

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestFlock_serializesSameName(t *testing.T) {
	dir := t.TempDir()
	fl := New(dir)

	var mu sync.Mutex
	overlap := false
	active := false
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			unlock, err := fl.Lock("alpha")
			if err != nil {
				t.Errorf("Lock: %v", err)
				return
			}
			mu.Lock()
			if active {
				overlap = true
			}
			active = true
			mu.Unlock()

			time.Sleep(20 * time.Millisecond)

			mu.Lock()
			active = false
			mu.Unlock()
			unlock()
		}()
	}
	wg.Wait()
	if overlap {
		t.Error("two holders of the same-name lock overlapped")
	}
}

func TestFlock_differentNamesDoNotBlock(t *testing.T) {
	fl := New(t.TempDir())
	u1, err := fl.Lock("a")
	if err != nil {
		t.Fatal(err)
	}
	defer u1()
	done := make(chan struct{})
	go func() {
		u2, err := fl.Lock("b") // different name → must not block on "a"
		if err == nil {
			u2()
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Lock on a different name blocked")
	}
}

func TestList_onlyRegularLockFiles(t *testing.T) {
	dir := t.TempDir()
	fl := New(dir)
	u, err := fl.Lock("alpha")
	if err != nil {
		t.Fatal(err)
	}
	u()
	for _, n := range []string{"note.txt", ".lock", "alpha.lock.bak"} {
		if err := os.WriteFile(filepath.Join(dir, n), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "dir.lock"), 0o700); err != nil {
		t.Fatal(err)
	}
	got, err := fl.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "alpha" {
		t.Fatalf("List = %+v, want only alpha", got)
	}
	if es, err := New(filepath.Join(dir, "absent")).List(); err != nil || len(es) != 0 {
		t.Fatalf("List of a missing dir = %v, %v; want empty", es, err)
	}
}

func approve(time.Time) (bool, error) { return true, nil }

func TestRemoveIfFree_removesFreeFile(t *testing.T) {
	dir := t.TempDir()
	fl := New(dir)
	u, _ := fl.Lock("a")
	u()
	removed, err := fl.RemoveIfFree("a", approve)
	if err != nil || !removed {
		t.Fatalf("RemoveIfFree = %v, %v; want true, nil", removed, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "a.lock")); !os.IsNotExist(err) {
		t.Fatalf("file still present: %v", err)
	}
	// A second removal of the now-absent file is a quiet no-op and must not
	// recreate it.
	if removed, err := fl.RemoveIfFree("a", approve); err != nil || removed {
		t.Fatalf("second RemoveIfFree = %v, %v; want false, nil", removed, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "a.lock")); !os.IsNotExist(err) {
		t.Fatalf("RemoveIfFree recreated the file: %v", err)
	}
}

func TestRemoveIfFree_heldLockSurvives(t *testing.T) {
	dir := t.TempDir()
	fl := New(dir)
	u, err := fl.Lock("a")
	if err != nil {
		t.Fatal(err)
	}
	called := false
	removed, err := fl.RemoveIfFree("a", func(time.Time) (bool, error) { called = true; return true, nil })
	if err != nil || removed || called {
		t.Fatalf("RemoveIfFree of a held lock = %v, %v (confirm called=%v); want false, nil, false", removed, err, called)
	}
	if _, err := os.Stat(filepath.Join(dir, "a.lock")); err != nil {
		t.Fatalf("held lock file was removed: %v", err)
	}
	u()
}

func TestRemoveIfFree_confirmVetoAndErrorKeepFile(t *testing.T) {
	dir := t.TempDir()
	fl := New(dir)
	u, _ := fl.Lock("a")
	u()
	if removed, err := fl.RemoveIfFree("a", func(time.Time) (bool, error) { return false, nil }); err != nil || removed {
		t.Fatalf("vetoed removal = %v, %v", removed, err)
	}
	boom := errors.New("boom")
	if removed, err := fl.RemoveIfFree("a", func(time.Time) (bool, error) { return true, boom }); !errors.Is(err, boom) || removed {
		t.Fatalf("erroring confirm = %v, %v; want false, boom", removed, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "a.lock")); err != nil {
		t.Fatalf("file removed despite veto/error: %v", err)
	}
}

func TestRemoveIfFree_rejectsUnsafeNames(t *testing.T) {
	dir := t.TempDir()
	victim := filepath.Join(dir, "victim.lock")
	if err := os.WriteFile(victim, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	fl := New(filepath.Join(dir, "sub"))
	for _, n := range []string{"", ".", "..", "../victim", "a/b"} {
		if removed, err := fl.RemoveIfFree(n, approve); err == nil || removed {
			t.Errorf("RemoveIfFree(%q) = %v, %v; want an error", n, removed, err)
		}
	}
	if _, err := os.Stat(victim); err != nil {
		t.Fatalf("a file outside the lock dir was touched: %v", err)
	}
}

// A Lock that is opened and waiting when the GC unlinks the file must not end up
// holding a lock on the dead inode: it re-verifies and retries on a fresh file,
// so a second Lock of the same name still excludes it.
func TestLock_waiterSurvivesUnlinkAndStaysExclusive(t *testing.T) {
	dir := t.TempDir()
	fl := New(dir)
	// Hold the lock, start a waiter on it, then unlink the file under both
	// (simulating the GC winning the inode while the waiter is blocked in flock).
	holder, err := fl.Lock("a")
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan func(), 1)
	go func() {
		u, err := fl.Lock("a")
		if err != nil {
			t.Errorf("waiter Lock: %v", err)
			got <- func() {}
			return
		}
		got <- u
	}()
	time.Sleep(50 * time.Millisecond) // let the waiter open the old inode and block
	if err := os.Remove(filepath.Join(dir, "a.lock")); err != nil {
		t.Fatal(err)
	}
	holder() // waiter wakes holding the dead inode, must detect it and retry
	waiterUnlock := <-got
	// The waiter now holds the lock on the LIVE file: a third locker must block.
	third := make(chan struct{})
	go func() {
		u, err := fl.Lock("a")
		if err == nil {
			u()
		}
		close(third)
	}()
	select {
	case <-third:
		t.Fatal("third Lock acquired while the waiter holds the live lock: the waiter kept a dead-inode lock")
	case <-time.After(150 * time.Millisecond):
	}
	waiterUnlock()
	select {
	case <-third:
	case <-time.After(2 * time.Second):
		t.Fatal("third Lock never acquired after release")
	}
}

// Hammer Lock against RemoveIfFree: mutual exclusion on the name must hold no
// matter how often the file is unlinked underneath the lockers.
func TestLock_mutualExclusionUnderConcurrentRemoval(t *testing.T) {
	dir := t.TempDir()
	fl := New(dir)
	var inCS, overlaps int32
	stop := make(chan struct{})
	var rm sync.WaitGroup
	rm.Add(1)
	go func() {
		defer rm.Done()
		for {
			select {
			case <-stop:
				return
			default:
				_, _ = fl.RemoveIfFree("a", approve)
			}
		}
	}()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 150; j++ {
				u, err := fl.Lock("a")
				if err != nil {
					t.Errorf("Lock: %v", err)
					return
				}
				if atomic.AddInt32(&inCS, 1) != 1 {
					atomic.AddInt32(&overlaps, 1)
				}
				atomic.AddInt32(&inCS, -1)
				u()
			}
		}()
	}
	wg.Wait()
	close(stop)
	rm.Wait()
	if n := atomic.LoadInt32(&overlaps); n != 0 {
		t.Fatalf("%d critical-section overlaps under concurrent removal", n)
	}
}
