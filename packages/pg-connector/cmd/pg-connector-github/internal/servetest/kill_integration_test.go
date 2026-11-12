//go:build integration

package servetest

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/gofrs/flock"
)

// A killed daemon that was busy forking gh calls leaves its lock held for a
// moment after the process is reaped (the forked child holds the descriptor
// until its exec). Kill must not return before the lock is free, so the start
// that follows never meets "another instance holds the lock". Measured on an
// unfixed Kill: about one kill in ten left the lock held, for up to tens of
// milliseconds, so 25 cycles catch an unfixed Kill with near certainty.
//
// This builds the real daemon and starts it 25 times, so it is integration tier
// (the unit tier guard in cmd/pg-connector enforces the tag).
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
