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

// A killed daemon that was busy forking gh calls leaves its lock held for a
// moment after the process is reaped (the forked child holds the descriptor
// until its exec). Kill must not return before the lock is free, so the start
// that follows never meets "another instance holds the lock". Measured on an
// unfixed Kill: about one kill in ten left the lock held, for up to tens of
// milliseconds, so 25 cycles catch an unfixed Kill with near certainty.
func TestKill_ReturnsOnlyOnceTheLockIsFree(t *testing.T) {
	d := NewDaemon(t, Options{
		Hooks: "poll_interval=2ms,viewer_interval=2ms",
		Before: func(d *Daemon) {
			d.Shim.SetPRs(MineExpr, PR{NodeID: "PR_a", Repo: "o/w", Number: 1})
			d.Shim.SetPRs(TeamExpr, PR{NodeID: "PR_a", Repo: "o/w", Number: 1})
		},
	})
	for i := 0; i < 25; i++ {
		d.Start(t)
		time.Sleep(time.Duration(i%7) * 20 * time.Millisecond)
		d.Kill(t)
		lk := flock.New(filepath.Join(d.StateDir, "daemon.lock"))
		locked, err := lk.TryLock()
		if err != nil || !locked {
			t.Fatalf("cycle %d: Kill returned with the lock still held (%v, %v)", i, locked, err)
		}
		_ = lk.Unlock()
	}
}
