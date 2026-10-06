package executor

import (
	"context"
	"errors"
	"testing"

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/ccpool"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/dtest"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/sessionlock"
)

// Worktree liveness lock (bead pg2-ganjb, INV-CCH-19): a dispatch holds the
// per-bead worktree lock SHARED from before it creates the worktree until run()
// returns, so the worktree-keyed sweep cannot take it exclusively meanwhile.

// lockProbeCC records, at the moment CC.Ensure runs (i.e. after the worktree was
// created and before any session row exists), whether the sweep's exclusive
// lock on the bead's worktree is refused.
type lockProbeCC struct {
	*dtest.FakeCC
	lockDir     string
	exclusiveAt string // "" until Ensure ran; then "held" or "free"
}

func (c *lockProbeCC) Ensure(ctx context.Context, externalID, name, cwd string, env, meta map[string]string) error {
	if l, err := sessionlock.TryLock(c.lockDir, sessionlock.WorktreeKey("zr-w")); errors.Is(err, sessionlock.ErrHeld) {
		c.exclusiveAt = "held"
	} else {
		c.exclusiveAt = "free"
		l.Unlock()
	}
	return c.FakeCC.Ensure(ctx, externalID, name, cwd, env, meta)
}

func TestRun_holdsWorktreeLockSharedThroughoutDispatch(t *testing.T) {
	cc := &lockProbeCC{FakeCC: &dtest.FakeCC{EnsureErr: errors.New("ccpool new: did not reach ready")}}
	h := newAbandonedHarness(t, cc, workerRole)
	cc.lockDir = t.TempDir()
	h.run.deps.LockDir = cc.lockDir

	_ = h.dispatch(context.Background())

	if cc.exclusiveAt != "held" {
		t.Fatalf("exclusive worktree lock at CC.Ensure = %q, want held: a live dispatch must block the sweep", cc.exclusiveAt)
	}
	// run() returned: the dispatch released its hold, so the sweep may act.
	l, err := sessionlock.TryLock(cc.lockDir, sessionlock.WorktreeKey("zr-w"))
	if err != nil {
		t.Fatalf("worktree lock still held after the dispatch returned: %v", err)
	}
	l.Unlock()
}

func TestRun_worktreeLockSharedBetweenConcurrentDispatches(t *testing.T) {
	dir := t.TempDir()
	// Another role's dispatch for the same bead already holds the shared lock.
	other, err := sessionlock.TryRLock(dir, sessionlock.WorktreeKey("zr-w"))
	if err != nil {
		t.Fatal(err)
	}
	defer other.Unlock()

	cc := &lockProbeCC{FakeCC: &dtest.FakeCC{EnsureErr: errors.New("boom")}, lockDir: dir}
	h := newAbandonedHarness(t, cc, workerRole)
	h.run.deps.LockDir = dir
	_ = h.dispatch(context.Background())

	// The second dispatch must not have been blocked: it reached CC.Ensure.
	if cc.exclusiveAt == "" {
		t.Fatal("a second dispatch for the same bead was blocked by the first's shared lock")
	}
}

func TestRun_noLockDirIsNoOp(t *testing.T) {
	cc := &dtest.FakeCC{EnsureErr: errors.New("boom")}
	h := newAbandonedHarness(t, cc, workerRole)
	h.run.deps.LockDir = ""
	if err := h.dispatch(context.Background()); err == nil {
		t.Fatal("ensure failure should still error")
	}
}

var _ ccpool.Runner = (*lockProbeCC)(nil)
