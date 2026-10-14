package executor

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/ccpool"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/dtest"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/item"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/roles"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/watchdog"
	"github.com/phillipgreenii/x/gitclient"
)

// TestDispatch_closedLegacyRow_launchesFresh is the pg2-92rfu scenario under
// INV-CCH-30. A settled row the handler already closed, unmarked (written by a
// build older than the outcome mark) and launched five hours ago, used to be
// absorbed by a same-event same-head re-request, whose budget then ran from the
// original launch and hard-stopped at once, closing the row again on every
// re-dispatch (268 close events for one session in the field). Now the row is
// settled and never absorbed: the dispatch launches its own session, never
// touches the old row and never budget-stops.
func TestDispatch_closedLegacyRow_launchesFresh(t *testing.T) {
	cfg := fastCfg()
	cfg.WorktreeDir = t.TempDir()
	cfg.BudgetTime = time.Hour // the old row launched five hours ago is over budget
	role := reviewRole(cfg)
	display := role.DisplayName(cfg.SessionPrefix, "bead-1")
	launched := time.Unix(0, 0).Add(-5 * time.Hour)
	legacy := ccpool.Session{
		ExternalID: "att-1", Name: display, Live: false, State: ccpool.StateIdle, CloseReason: "handler",
		Meta: map[string]string{
			ccpool.MetaKeyEventID: "review.ready:bead-1", ccpool.MetaKeyHeadSHA: "h1",
			ccpool.MetaKeyLaunchedAt: ccpool.FormatMetaTime(launched),
		},
	}
	bd := &dtest.ScriptBD{StatusSeq: map[string][]string{"bead-1": {"in_progress", "closed"}}}
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{
		{legacy},
		{legacy, {ExternalID: "att-2", Name: display, Live: true, State: ccpool.StateWorking}},
	}}
	d := DispatchContext{
		Role: role, EventID: "review.ready:bead-1",
		Item: item.Item{ID: "bead-1", Metadata: map[string]any{"repo": "example/repo", "pr_number": "7", "head_sha": "h1"}},
	}
	deps := newExec(cc, bd, cfg).deps
	deps.ExternalID = "att-2"
	deps.Git = &dtest.NoopGit{}
	deps.GitOpener = (&dtest.NoopGitOpener{}).Open
	_, err := (ccpoolExecutor{}).Dispatch(context.Background(), d, deps)
	if errors.Is(err, watchdog.ErrBudgetExceeded) {
		t.Fatalf("the dispatch must not budget-stop on the old row's launch time: %v", err)
	}
	if err != nil {
		t.Fatalf("the fresh session should run to completion (bead closed), got %v", err)
	}
	if got := cc.Ensured; len(got) != 1 || got[0] != "att-2" {
		t.Errorf("a closed settled row is never absorbed; a fresh session must launch; Ensured=%v", got)
	}
	for _, id := range cc.Closed {
		if id == "att-1" {
			t.Errorf("the old row is closed and gone and must not be closed again; Closed=%v", cc.Closed)
		}
	}
	for _, m := range cc.SetMetaCalls() {
		if m.ExternalID == "att-1" {
			t.Errorf("nothing may be written to the old row; SetMeta=%v", cc.SetMetaCalls())
		}
	}
}

// A worktree an earlier dispatch already removed is reported once, at INFO, as
// already removed, never as a failed open "left for next sweep" (pg2-92rfu).
func TestCleanupWorktree_alreadyRemoved_reportedOnce(t *testing.T) {
	logs := captureLog(t)
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{}}}
	e := newExec(cc, &dtest.ScriptBD{}, fastCfg())
	gone := filepath.Join(t.TempDir(), "bead-1") // never created
	e.deps.GitOpener = func(_ context.Context, dir string) (gitclient.WorktreeManager, error) {
		return nil, fmt.Errorf("gitclient: %s: %w", dir, &fs.PathError{Op: "lstat", Path: dir, Err: fs.ErrNotExist})
	}
	e.cleanupWorktree(context.Background(), &roles.CCPoolConfig{}, "s", "bead-1", gone)
	out := logs.String()
	if strings.Contains(out, "open failed") {
		t.Errorf("an already-removed worktree is not a failed open; logs:\n%s", out)
	}
	if n := strings.Count(out, "worktree cleanup: already removed"); n != 1 {
		t.Errorf("the gone worktree must be reported exactly once, got %d; logs:\n%s", n, out)
	}
}

// A worktree whose open fails for any reason other than "already gone" is still
// left for the sweep with the original WARN.
func TestCleanupWorktree_openFailureOtherThanGone_stillWarns(t *testing.T) {
	logs := captureLog(t)
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{}}}
	e := newExec(cc, &dtest.ScriptBD{}, fastCfg())
	e.deps.GitOpener = func(context.Context, string) (gitclient.WorktreeManager, error) {
		return nil, errors.New("open failed")
	}
	e.cleanupWorktree(context.Background(), &roles.CCPoolConfig{}, "s", "bead-1", "/tmp/pg2-92rfu/bead-1")
	if !strings.Contains(logs.String(), "open failed (left for next sweep)") {
		t.Errorf("a non-ENOENT open failure must keep the sweep WARN; logs:\n%s", logs)
	}
}

// An absorbed turn whose own (still running) handler closes it while this
// dispatch waits too: the wait's death branch finds the row already closed and
// must not close it a second time, which would re-stamp it and append another
// close event (pg2-92rfu).
func TestDispatch_absorbedTurnClosedByItsHandler_deathBranch_closesOnce(t *testing.T) {
	cfg := fastCfg()
	cfg.WorktreeDir = t.TempDir()
	role := noBudget(reviewRole(cfg))
	display := role.DisplayName(cfg.SessionPrefix, "bead-1")
	turn := ccpool.Session{
		ExternalID: "att-1", Name: display, Live: true, State: ccpool.StateWorking,
		Meta: map[string]string{ccpool.MetaKeyEventID: "review.ready:bead-1", ccpool.MetaKeyHeadSHA: "h1"},
	}
	closed := turn
	closed.Live, closed.State, closed.CloseReason = false, ccpool.StateIdle, "handler"
	bd := &dtest.ScriptBD{StatusSeq: map[string][]string{"bead-1": {"in_progress"}}}
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{turn}, {closed}}}
	d := DispatchContext{
		Role: role, EventID: "review.ready:bead-1",
		Item: item.Item{ID: "bead-1", Metadata: map[string]any{"repo": "example/repo", "pr_number": "7", "head_sha": "h1"}},
	}
	deps := newExec(cc, bd, cfg).deps
	deps.ExternalID = "att-2"
	deps.Git = &dtest.NoopGit{}
	deps.GitOpener = (&dtest.NoopGitOpener{}).Open
	if _, err := (ccpoolExecutor{}).Dispatch(context.Background(), d, deps); err == nil {
		t.Fatal("a turn that ended with its bead still open must fail the absorbing dispatch")
	}
	if len(cc.Ensured) != 0 {
		t.Errorf("the same event's turn in progress is absorbed, not launched beside; Ensured=%v", cc.Ensured)
	}
	if len(cc.Closed) != 0 {
		t.Errorf("a row that is already closed must not be closed again; Closed=%v", cc.Closed)
	}
}
