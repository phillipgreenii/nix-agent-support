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

// TestDispatch_absorbedClosedLegacyRow_budgetStop_closesOnceAndReportsGoneWorktreeOnce
// is the pg2-92rfu regression. A settled row the handler already closed, unmarked
// (written by a build older than the incomplete marker) and launched long ago, is
// absorbed by a same-event same-head re-request: the budget runs from the original
// launch time and hard-stops at once. That absorb is the one allowed spurious
// attempt on such a row; it MUST
//
//   - stamp the row incomplete (so the next dispatch launches fresh instead of
//     hot-looping on the same session),
//   - NOT close the row again: it is closed and its tmux session gone, and a second
//     ccpool close re-stamps it and appends another close event (268 for one session
//     in the field), and
//   - report the worktree an earlier dispatch already removed as "already removed"
//     at INFO, not as a failed open "left for next sweep" per re-dispatch.
func TestDispatch_absorbedClosedLegacyRow_budgetStop_closesOnceAndReportsGoneWorktreeOnce(t *testing.T) {
	logs := captureLog(t)
	cfg := fastCfg()
	cfg.WorktreeDir = t.TempDir()
	cfg.BudgetTime = time.Hour // the row launched five hours ago is over budget
	role := reviewRole(cfg)
	display := role.DisplayName(cfg.SessionPrefix, "bead-1")
	launched := time.Unix(0, 0).Add(-5 * time.Hour)
	gone := filepath.Join(cfg.WorktreeDir, "bead-1") // never created: an earlier dispatch removed it
	legacy := ccpool.Session{
		ExternalID: "att-1", Name: display, CWD: gone, Live: false, State: ccpool.StateIdle, CloseReason: "handler",
		Meta: map[string]string{
			ccpool.MetaKeyEventID: "review.ready:bead-1", ccpool.MetaKeyHeadSHA: "h1",
			ccpool.MetaKeyLaunchedAt: ccpool.FormatMetaTime(launched),
		},
	}
	bd := &dtest.ScriptBD{StatusSeq: map[string][]string{"bead-1": {"open"}}}
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{legacy}}}
	d := DispatchContext{
		Role: role, EventID: "review.ready:bead-1",
		Item: item.Item{ID: "bead-1", Metadata: map[string]any{"repo": "example/repo", "pr_number": "7", "head_sha": "h1"}},
	}
	deps := newExec(cc, bd, cfg).deps
	deps.ExternalID = "att-2"
	deps.Git = &dtest.NoopGit{}
	deps.GitOpener = func(_ context.Context, dir string) (gitclient.WorktreeManager, error) {
		return nil, fmt.Errorf("gitclient: %s: %w", dir, &fs.PathError{Op: "lstat", Path: dir, Err: fs.ErrNotExist})
	}
	_, err := (ccpoolExecutor{}).Dispatch(context.Background(), d, deps)
	if !errors.Is(err, watchdog.ErrBudgetExceeded) {
		t.Fatalf("absorbing the over-budget legacy row must budget-stop; err=%v", err)
	}
	if len(cc.Ensured) != 0 {
		t.Fatalf("a same-event same-head re-request absorbs the row, it does not launch; Ensured=%v", cc.Ensured)
	}
	if len(cc.Closed) != 0 {
		t.Errorf("a row that is already closed and gone must not be closed again; Closed=%v", cc.Closed)
	}
	stamped := false
	for _, m := range cc.SetMetaCalls() {
		if m.ExternalID == "att-1" && m.Key == ccpool.MetaKeyIncomplete {
			stamped = true
		}
	}
	if !stamped {
		t.Errorf("the failed absorb must stamp the row incomplete so the next dispatch launches fresh; sets=%v", cc.SetMetaCalls())
	}
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

// The unlimited-budget variant: no watchdog, so waitDone's unexplained-death
// branch is what fails the absorb. It stamps the row incomplete and, like the
// budget stop, must not close the already-closed row again (pg2-92rfu).
func TestDispatch_absorbedClosedLegacyRow_deathBranch_closesOnce(t *testing.T) {
	cfg := fastCfg()
	cfg.WorktreeDir = t.TempDir()
	role := noBudget(reviewRole(cfg))
	display := role.DisplayName(cfg.SessionPrefix, "bead-1")
	legacy := ccpool.Session{
		ExternalID: "att-1", Name: display, Live: false, State: ccpool.StateIdle, CloseReason: "handler",
		Meta: map[string]string{ccpool.MetaKeyEventID: "review.ready:bead-1", ccpool.MetaKeyHeadSHA: "h1"},
	}
	bd := &dtest.ScriptBD{StatusSeq: map[string][]string{"bead-1": {"open"}}}
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{legacy}}}
	d := DispatchContext{
		Role: role, EventID: "review.ready:bead-1",
		Item: item.Item{ID: "bead-1", Metadata: map[string]any{"repo": "example/repo", "pr_number": "7", "head_sha": "h1"}},
	}
	deps := newExec(cc, bd, cfg).deps
	deps.ExternalID = "att-2"
	deps.Git = &dtest.NoopGit{}
	deps.GitOpener = (&dtest.NoopGitOpener{}).Open
	if _, err := (ccpoolExecutor{}).Dispatch(context.Background(), d, deps); err == nil {
		t.Fatal("absorbing a closed row whose bead is still open must fail")
	}
	if len(cc.Closed) != 0 {
		t.Errorf("a row that is already closed must not be closed again; Closed=%v", cc.Closed)
	}
}
