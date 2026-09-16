package store

import (
	"testing"
	"time"
)

// TestLock_SameKeySerializes is the mutual-exclusion half: a second Lock
// for the SAME (repo, type, id) must not succeed while the first holds it,
// and must succeed once the first releases. This is the packet's required
// "lock contention test (two concurrent lock attempts on the same (type,
// id) — one blocks/fails per the ported semantics)".
func TestLock_SameKeySerializes(t *testing.T) {
	l := NewLocker(LockerOptions{LockDir: t.TempDir()})

	unlock1, err := l.Lock("owner/repo", "pull_request", "42")
	if err != nil {
		t.Fatalf("first Lock: %v", err)
	}

	acquired := make(chan struct{})
	go func() {
		unlock2, err := l.Lock("owner/repo", "pull_request", "42")
		if err != nil {
			t.Errorf("second Lock: %v", err)
			return
		}
		close(acquired)
		unlock2()
	}()

	select {
	case <-acquired:
		t.Fatalf("second Lock succeeded while first was still held")
	case <-time.After(100 * time.Millisecond):
		// expected: still contended
	}

	unlock1()

	select {
	case <-acquired:
		// expected: second Lock succeeded once released
	case <-time.After(2 * time.Second):
		t.Fatalf("second Lock did not succeed after first was released")
	}
}

// TestLock_TimesOutOnContention confirms Lock gives up with ErrLockTimeout
// (rather than blocking forever) when the key stays held past the
// configured Timeout.
func TestLock_TimesOutOnContention(t *testing.T) {
	l := NewLocker(LockerOptions{
		LockDir:      t.TempDir(),
		Timeout:      50 * time.Millisecond,
		pollInterval: 5 * time.Millisecond,
	})

	unlock1, err := l.Lock("owner/repo", "pull_request", "42")
	if err != nil {
		t.Fatalf("first Lock: %v", err)
	}
	defer unlock1()

	_, err = l.Lock("owner/repo", "pull_request", "42")
	if err == nil {
		t.Fatalf("second Lock succeeded, want ErrLockTimeout")
	}
}

// TestLock_DifferentKeysDoNotContend confirms distinct (repo, type, id)
// triples never serialize against each other.
func TestLock_DifferentKeysDoNotContend(t *testing.T) {
	l := NewLocker(LockerOptions{LockDir: t.TempDir()})

	unlock1, err := l.Lock("owner/repo", "pull_request", "42")
	if err != nil {
		t.Fatalf("Lock (42): %v", err)
	}
	defer unlock1()

	unlock2, err := l.Lock("owner/repo", "pull_request", "43")
	if err != nil {
		t.Fatalf("Lock (43) contended with a different key: %v", err)
	}
	unlock2()

	unlock3, err := l.Lock("other/repo", "pull_request", "42")
	if err != nil {
		t.Fatalf("Lock (other repo, same type/id) contended: %v", err)
	}
	unlock3()
}

// TestLock_RepoDistinguishesOtherwiseIdenticalKeys confirms the lock is
// additionally keyed by repo, per the design doc's section 7.6 ("Rows are
// keyed by repo so a second repository is additive") — two entities with
// the same (type, id) but different repos must not collide onto the same
// lock file.
func TestLock_RepoDistinguishesOtherwiseIdenticalKeys(t *testing.T) {
	if sanitizeLockKey("repo-a/pull_request/1") == sanitizeLockKey("repo-b/pull_request/1") {
		t.Fatalf("sanitizeLockKey collided across different repos for the same (type, id)")
	}
}
