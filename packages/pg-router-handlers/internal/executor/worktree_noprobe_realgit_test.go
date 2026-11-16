package executor

import (
	"context"
	"os"
	"sync/atomic"
	"testing"

	"github.com/phillipgreenii/x/gitclient"

	"github.com/phillipgreenii/pg-router-handlers/internal/ccpool"
	"github.com/phillipgreenii/pg-router-handlers/internal/item"
)

// The dispatch-time worktree cleanup no longer reads the process table: who
// holds a worktree is a pg-gwm lease question. The repository and the linked
// worktree are real (a temporary git repository); the probe is a counter.

func noProbeRun(t *testing.T, calls *atomic.Int32) (*realGitHarness, *settleCC) {
	t.Helper()
	h := newRealGitHarness(t)
	root, err := h.open(t.Context(), h.repo.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(h.wtDir, 0o755); err != nil {
		t.Fatal(err)
	}
	hd, err := root.CreateWorktree(t.Context(), h.wt, realGitBranch, gitclient.CreateWorktreeOptions{ResetBranch: true})
	if err == nil && hd != nil {
		err = hd.Wait()
	}
	if err != nil || !h.dirPresent() || !h.branchPresent() {
		t.Fatalf("fixture worktree: err=%v dir=%v branch=%v", err, h.dirPresent(), h.branchPresent())
	}
	cc := newSettleCC(realGitExt, fixedState(ccpool.StateIdle))
	h.run.deps.CC = cc
	h.run.deps.ProcessCWDs = func(context.Context) ([]string, error) {
		calls.Add(1)
		return []string{h.wt}, nil // a process holding the tree must not matter
	}
	return h, cc
}

func TestNoProcessProbeAtDispatch(t *testing.T) {
	var calls atomic.Int32
	h, cc := noProbeRun(t, &calls)
	d := DispatchContext{Role: h.role, Item: item.Item{ID: realGitBead}}
	if _, err := h.run.finishWait(t.Context(), h.role.CCPool, d, realGitExt, h.wt, nil); err != nil {
		t.Fatalf("finishWait: %v", err)
	}
	if len(cc.Closed) != 1 || cc.Closed[0] != realGitExt {
		t.Fatalf("the settled session must be closed once; Closed=%v", cc.Closed)
	}
	if h.dirPresent() || h.branchPresent() {
		t.Errorf("the worktree and branch must be removed without a process check (dir=%v branch=%v)", h.dirPresent(), h.branchPresent())
	}
	if n := calls.Load(); n != 0 {
		t.Errorf("the dispatch path called the process probe %d times, want 0", n)
	}
}
