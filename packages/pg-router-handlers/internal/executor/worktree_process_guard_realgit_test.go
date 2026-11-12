package executor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/x/gitclient"

	"github.com/phillipgreenii/pg-router-handlers/internal/ccpool"
	"github.com/phillipgreenii/pg-router-handlers/internal/item"
	"github.com/phillipgreenii/pg-router-handlers/internal/roles"
)

// Real-git proof of the process-working-directory guard of the dispatch-time
// worktree cleanup (INV-CCH-15) and of its order against the settled-session
// close (INV-CCH-17). The repository and the linked worktree are real (a
// temporary git repository); the live-process probe is an injected fake, so no
// test reads the live process table.

// processGuardRun builds a real linked worktree for realGitBead and a ccpoolRun
// over it whose settled session is an idle, live row. probe receives the
// worktree path and whether the session has been closed yet.
func processGuardRun(t *testing.T, probe func(wt string, closed func() bool) ([]string, error)) (*realGitHarness, *settleCC) {
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
		return probe(h.wt, func() bool {
			cc.mu.Lock()
			defer cc.mu.Unlock()
			return cc.closed
		})
	}
	return h, cc
}

func (h *realGitHarness) finish(t *testing.T) {
	t.Helper()
	d := DispatchContext{Role: h.role, Item: item.Item{ID: realGitBead}}
	if _, err := h.run.finishWait(t.Context(), h.role.CCPool, d, realGitExt, h.wt, nil); err != nil {
		t.Fatalf("finishWait: %v", err)
	}
}

// The dispatch's own idle session holds the worktree as its working directory
// until it is closed; a stranger's process holds it regardless.
func TestFinishWait_ownSessionClosedBeforeTheWorktreeIsRemoved(t *testing.T) {
	h, cc := processGuardRun(t, func(wt string, closed func() bool) ([]string, error) {
		if closed() {
			return []string{"/somewhere/else"}, nil
		}
		return []string{filepath.Join(wt, "sub")}, nil // the own idle claude
	})
	h.finish(t)
	if len(cc.Closed) != 1 || cc.Closed[0] != realGitExt {
		t.Fatalf("the settled session must be closed once; Closed=%v", cc.Closed)
	}
	if h.dirPresent() {
		t.Error("with no stranger holding it, the worktree must be removed after the own session is closed")
	}
	if h.branchPresent() {
		t.Error("the anchor branch must be deleted with the worktree")
	}
}

func TestFinishWait_aStrangerHoldingTheWorktreeKeepsIt(t *testing.T) {
	h, cc := processGuardRun(t, func(wt string, _ func() bool) ([]string, error) {
		return []string{filepath.Join(wt, "company", "store")}, nil
	})
	h.finish(t)
	if len(cc.Closed) != 1 {
		t.Errorf("the settled session is still closed; Closed=%v", cc.Closed)
	}
	if !h.dirPresent() || !h.branchPresent() {
		t.Errorf("a live process inside the worktree must keep it and its branch (dir=%v branch=%v)", h.dirPresent(), h.branchPresent())
	}
}

func TestFinishWait_aFailedProbeKeepsTheWorktree(t *testing.T) {
	h, _ := processGuardRun(t, func(string, func() bool) ([]string, error) {
		return nil, errors.New("lsof: boom")
	})
	h.finish(t)
	if !h.dirPresent() || !h.branchPresent() {
		t.Errorf("a failed probe must keep the worktree and its branch (dir=%v branch=%v)", h.dirPresent(), h.branchPresent())
	}
}

// A session whose close did not take effect is still running: its own process
// then keeps the worktree.
func TestFinishWait_aFailedSettledCloseKeepsTheWorktreeThroughItsOwnProcess(t *testing.T) {
	h, cc := processGuardRun(t, func(wt string, closed func() bool) ([]string, error) {
		if closed() {
			return nil, nil
		}
		return []string{wt}, nil
	})
	cc.CloseErr = errors.New("ccpool close: boom")
	h.finish(t)
	if !h.dirPresent() {
		t.Error("an unclosed own session still holds the worktree: it must be kept")
	}
}

// The reclaim of a dispatch's abandoned worktree shares the guard.
func TestCleanupWorktree_aProcessHoldingTheWorktreeKeepsIt(t *testing.T) {
	h, _ := processGuardRun(t, func(wt string, _ func() bool) ([]string, error) { return []string{wt}, nil })
	h.run.cleanupWorktree(t.Context(), &roles.CCPoolConfig{}, realGitExt, realGitBead, h.wt)
	if !h.dirPresent() {
		t.Error("cleanupWorktree must keep a worktree a live process holds")
	}
}
