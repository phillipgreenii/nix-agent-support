package servetest

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/gofrs/flock"
)

// awaitLockFree is what Daemon.Kill waits on: a lock still held (here by a
// holder that lets go after a while, as a forked child does when its exec
// closes the inherited descriptor) is waited out, not reported as free.
func TestAwaitLockFree_WaitsForTheHolderToLetGo(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.lock")
	holder := flock.New(path)
	if locked, err := holder.TryLock(); err != nil || !locked {
		t.Fatalf("fixture: take the lock: %v, %v", locked, err)
	}
	const hold = 300 * time.Millisecond
	start := time.Now()
	go func() {
		time.Sleep(hold)
		_ = holder.Unlock()
	}()
	if !awaitLockFree(path, 10*time.Second) {
		t.Fatal("the lock was never reported free")
	}
	if got := time.Since(start); got < hold {
		t.Errorf("reported free after %s, while the holder still had it (%s)", got, hold)
	}
}

func TestAwaitLockFree_GivesUpWhileTheLockIsStillHeld(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.lock")
	holder := flock.New(path)
	if locked, err := holder.TryLock(); err != nil || !locked {
		t.Fatalf("fixture: take the lock: %v, %v", locked, err)
	}
	defer func() { _ = holder.Unlock() }()
	if awaitLockFree(path, 150*time.Millisecond) {
		t.Fatal("a held lock was reported free")
	}
}

func TestAwaitLockFree_AFreeOrAbsentLockIsFree(t *testing.T) {
	if !awaitLockFree(filepath.Join(t.TempDir(), "never-created.lock"), time.Second) {
		t.Fatal("an absent lock file was reported held")
	}
}
