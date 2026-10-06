package posted

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestLockerFromEnv(t *testing.T) {
	l := LockerFromEnv(env(map[string]string{EnvStateDir: "/o"}))
	if l.Dir != "/o/locks" || l.Wait != 60*time.Second {
		t.Fatalf("got %+v", l)
	}
	if l := LockerFromEnv(env(nil)); l.Dir != "" {
		t.Fatalf("no home must leave Dir empty, got %q", l.Dir)
	}
	if _, err := LockerFromEnv(env(nil)).Acquire("o", "r", 1); err == nil {
		t.Fatal("no home must be an error")
	}
}

func TestLock_PathUnderLocksDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "locks")
	l := Locker{Dir: dir}
	k, err := l.Acquire("owner", "repo", 3)
	if err != nil {
		t.Fatal(err)
	}
	defer k.Release()
	matches, _ := filepath.Glob(filepath.Join(dir, "owner__repo__3.lock"))
	if len(matches) != 1 {
		t.Fatalf("lock file not at the expected path: %v", matches)
	}
}

func TestLock_RejectsUnsafeParts(t *testing.T) {
	l := Locker{Dir: t.TempDir()}
	if _, err := l.Acquire("..", "repo", 1); err == nil {
		t.Fatal("unsafe owner accepted")
	}
	if _, err := l.Acquire("owner", "repo", 0); err == nil {
		t.Fatal("non-positive PR accepted")
	}
}

func TestLock_TimeoutIsRetryableUnavailable(t *testing.T) {
	l := Locker{Dir: t.TempDir(), Wait: 150 * time.Millisecond, Poll: 10 * time.Millisecond}
	first, err := l.Acquire("owner", "repo", 1)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	second, err := l.Acquire("owner", "repo", 1)
	if err == nil {
		second.Release()
		t.Fatal("second holder must not get the lock")
	}
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
	if d := time.Since(start); d < 150*time.Millisecond || d > 5*time.Second {
		t.Fatalf("waited %v, want about 150ms", d)
	}
	if err := first.Release(); err != nil {
		t.Fatal(err)
	}
	third, err := l.Acquire("owner", "repo", 1)
	if err != nil {
		t.Fatalf("lock must be free after release: %v", err)
	}
	third.Release()
}

func TestLock_DifferentPRsDoNotBlock(t *testing.T) {
	l := Locker{Dir: t.TempDir(), Wait: 100 * time.Millisecond, Poll: 10 * time.Millisecond}
	a, err := l.Acquire("owner", "repo", 1)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Release()
	b, err := l.Acquire("owner", "repo", 2)
	if err != nil {
		t.Fatalf("a different PR must not contend: %v", err)
	}
	b.Release()
}

func TestLock_TwoConcurrentHoldersSerialize(t *testing.T) {
	l := Locker{Dir: t.TempDir(), Wait: 5 * time.Second, Poll: 5 * time.Millisecond}
	first, err := l.Acquire("owner", "repo", 1)
	if err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var order []string
	record := func(s string) { mu.Lock(); order = append(order, s); mu.Unlock() }

	acquired := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		k, err := l.Acquire("owner", "repo", 1)
		if err != nil {
			done <- err
			return
		}
		record("second-acquired")
		close(acquired)
		done <- k.Release()
	}()

	select {
	case <-acquired:
		t.Fatal("second holder got the lock while the first still held it")
	case <-time.After(100 * time.Millisecond):
	}
	record("first-release")
	if err := first.Release(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(order) != 2 || order[0] != "first-release" || order[1] != "second-acquired" {
		t.Fatalf("order = %v", order)
	}
}

func TestLock_ReleaseIsIdempotent(t *testing.T) {
	k, err := (Locker{Dir: t.TempDir()}).Acquire("owner", "repo", 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := k.Release(); err != nil {
		t.Fatal(err)
	}
	if err := k.Release(); err != nil {
		t.Fatalf("second release: %v", err)
	}
	var nilLock *Lock
	if err := nilLock.Release(); err != nil {
		t.Fatal(err)
	}
}
