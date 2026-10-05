package executor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/ccpool"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/config"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/dtest"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/item"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/roles"
	"github.com/phillipgreenii/x/gitclient"
)

// Tests for pg2-w3usi: a dispatch that dies between isolation.Ensure (which
// creates the per-bead worktree + pg-router/<bead> branch) and a running
// session must reclaim them itself, since no session row exists for any
// reconcile to find. Every git operation goes through the recording
// dtest.NoopGitOpener and every path lives under t.TempDir(); nothing touches
// real git state.

const abandonedExt = "pg-router-worker-zr-w"

// ctxCheckingOpener wraps a NoopGitOpener and refuses every Open whose
// context is already cancelled, mirroring what a real git/ccpool call does on
// a dead ctx. It proves the reclaim runs on a cancellation-immune context.
type ctxCheckingOpener struct {
	inner *dtest.NoopGitOpener
}

func (o ctxCheckingOpener) Open(ctx context.Context, dir string) (gitclient.WorktreeManager, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return o.inner.Open(ctx, dir)
}

// cancelOnEnsureCC cancels the dispatch context from inside CC.Ensure and
// fails with ctx.Err(): the handler being SIGTERMed (or its ctx expiring)
// between isolation.Ensure and the session row's creation.
type cancelOnEnsureCC struct {
	*dtest.FakeCC
	cancel context.CancelFunc
}

func (c cancelOnEnsureCC) Ensure(ctx context.Context, externalID, name, cwd string, env, meta map[string]string) error {
	_ = c.FakeCC.Ensure(ctx, externalID, name, cwd, env, meta)
	c.cancel()
	return ctx.Err()
}

type abandonedHarness struct {
	cfg    config.Config
	wt     string
	opener *dtest.NoopGitOpener
	run    *ccpoolRun
	role   roles.Role
}

func newAbandonedHarness(t *testing.T, cc ccpool.Runner, role func(config.Config) roles.Role) *abandonedHarness {
	t.Helper()
	cfg := fastCfg()
	cfg.WorktreeDir = t.TempDir()
	h := &abandonedHarness{
		cfg:    cfg,
		wt:     filepath.Join(cfg.WorktreeDir, "zr-w"),
		opener: &dtest.NoopGitOpener{},
		role:   role(cfg),
	}
	bd := &dtest.ScriptBD{Show: map[string]string{"zr-w": `{"id":"zr-w","status":"open","labels":[]}`}}
	h.run = &ccpoolRun{deps: newExec(&dtest.FakeCC{}, bd, cfg).deps}
	h.run.deps.CC = cc
	h.run.deps.ExternalID = abandonedExt
	h.run.deps.Git = &dtest.NoopGit{}
	h.run.deps.GitOpener = ctxCheckingOpener{inner: h.opener}.Open
	return h
}

func (h *abandonedHarness) dispatch(ctx context.Context) error {
	_, err := h.run.run(ctx, DispatchContext{Role: h.role, Item: item.Item{ID: "zr-w"}})
	return err
}

func (h *abandonedHarness) reclaimed() bool {
	return hasRemoveCall(h.opener, h.wt, false) && hasDeleteBranchCall(h.opener, "pg-router/zr-w", true)
}

// The kill/cancel window: ctx is cancelled after the worktree exists but
// before the session row does. The handler must still remove both.
func TestRun_cancelBetweenWorktreeAndSession_reclaimsWorktreeAndBranch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fake := &dtest.FakeCC{}
	h := newAbandonedHarness(t, cancelOnEnsureCC{FakeCC: fake, cancel: cancel}, workerRole)

	err := h.dispatch(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want it to wrap context.Canceled", err)
	}
	if !h.reclaimed() {
		t.Fatalf("worktree and anchor branch must be reclaimed despite the cancelled ctx; calls=%v", h.opener.WTM.Calls)
	}
}

func TestRun_sessionEnsureFails_reclaimsWorktreeAndBranch(t *testing.T) {
	cc := &dtest.FakeCC{EnsureErr: errors.New("ccpool new: did not reach ready")}
	h := newAbandonedHarness(t, cc, workerRole)

	if err := h.dispatch(context.Background()); err == nil {
		t.Fatal("ensure failure should error")
	}
	if !h.reclaimed() {
		t.Fatalf("worktree and anchor branch must be reclaimed after a failed session Ensure; calls=%v", h.opener.WTM.Calls)
	}
}

// A failed purge that leaves the abandoned session live (it may still reach
// ready) must keep its working directory.
func TestRun_sessionEnsureFails_sessionStillLive_keepsWorktree(t *testing.T) {
	cc := &dtest.FakeCC{
		EnsureErr: errors.New("ccpool new: did not reach ready"),
		CloseErr:  errors.New("ccpool close: boom"),
		ListSeq:   [][]ccpool.Session{{{ExternalID: abandonedExt, Live: true, State: ccpool.StateStarting, CWD: "x"}}},
	}
	h := newAbandonedHarness(t, cc, workerRole)

	_ = h.dispatch(context.Background())
	if anyRemoveCall(h.opener) || anyDeleteBranchCall(h.opener) {
		t.Fatalf("a still-live session's worktree must be kept; calls=%v", h.opener.WTM.Calls)
	}
}

func TestRun_sessionEnsureFails_listError_keepsWorktree(t *testing.T) {
	cc := &dtest.FakeCC{
		EnsureErr: errors.New("ccpool new: did not reach ready"),
		ListErr:   errors.New("ccpool list: transient"),
	}
	h := newAbandonedHarness(t, cc, workerRole)

	_ = h.dispatch(context.Background())
	if anyRemoveCall(h.opener) || anyDeleteBranchCall(h.opener) {
		t.Fatalf("a can't-tell session list must fail toward NOT deleting; calls=%v", h.opener.WTM.Calls)
	}
}

// Only a worktree THIS dispatch created is reclaimed; one that already existed
// (a previous attempt's, possibly with commits) is left to finishWait and the
// reconciles.
func TestRun_sessionEnsureFails_preexistingWorktree_isKept(t *testing.T) {
	cc := &dtest.FakeCC{EnsureErr: errors.New("ccpool new: did not reach ready")}
	h := newAbandonedHarness(t, cc, workerRole)
	if err := os.MkdirAll(h.wt, 0o755); err != nil {
		t.Fatal(err)
	}

	_ = h.dispatch(context.Background())
	if anyRemoveCall(h.opener) || anyDeleteBranchCall(h.opener) {
		t.Fatalf("a worktree that pre-existed the dispatch must not be reclaimed; calls=%v", h.opener.WTM.Calls)
	}
}

// RemoveWorktree runs with force=false, so git refuses a dirty tree; the
// anchor branch must then survive too.
func TestRun_sessionEnsureFails_dirtyWorktree_keepsBranch(t *testing.T) {
	cc := &dtest.FakeCC{EnsureErr: errors.New("ccpool new: did not reach ready")}
	h := newAbandonedHarness(t, cc, workerRole)
	var opens []string
	h.run.deps.GitOpener = func(_ context.Context, dir string) (gitclient.WorktreeManager, error) {
		opens = append(opens, dir)
		if dir == h.wt && len(opens) == 1 {
			return nil, gitclient.ErrNotARepository // first probe: not created yet
		}
		return failingRemoveWTM{}, nil
	}

	_ = h.dispatch(context.Background())
	// probe(wt), open(repoRoot) for create, open(wt) for the failed remove;
	// a branch delete would add a second open of repoRoot.
	repoOpens := 0
	for _, o := range opens {
		if o == h.run.deps.Cfg.RepoRoot {
			repoOpens++
		}
	}
	if repoOpens != 1 {
		t.Fatalf("a refused (dirty) removal must not be followed by a branch delete; opens=%v", opens)
	}
}

func TestRun_nudgeNotIngested_reclaimsWorktreeAndBranch(t *testing.T) {
	cc := &dtest.FakeCC{SendErr: ccpool.ErrPromptNotIngested}
	h := newAbandonedHarness(t, cc, workerRole)

	if err := h.dispatch(context.Background()); err == nil {
		t.Fatal("dropped nudge must fail the dispatch")
	}
	if !h.reclaimed() {
		t.Fatalf("worktree and anchor branch must be reclaimed when the nudge was never ingested; calls=%v", h.opener.WTM.Calls)
	}
}

// A generic Send failure leaves a live session behind (nothing closes it), so
// its working directory must not be pulled out from under it.
func TestRun_sendFails_liveSession_keepsWorktree(t *testing.T) {
	cc := &dtest.FakeCC{
		SendErr: dtest.ErrSend,
		ListSeq: [][]ccpool.Session{{{ExternalID: abandonedExt, Live: true, State: ccpool.StateReady}}},
	}
	h := newAbandonedHarness(t, cc, workerRole)

	if err := h.dispatch(context.Background()); err == nil {
		t.Fatal("send failure should error")
	}
	if anyRemoveCall(h.opener) || anyDeleteBranchCall(h.opener) {
		t.Fatalf("a live session's worktree must be kept after a generic send failure; calls=%v", h.opener.WTM.Calls)
	}
}

// Isolation strategies other than "worktree" hand back a directory this
// executor never created exclusively; none may be removed.
func TestRun_sessionEnsureFails_nonWorktreeIsolation_removesNothing(t *testing.T) {
	for _, typ := range []string{"none", "path"} {
		t.Run(typ, func(t *testing.T) {
			cc := &dtest.FakeCC{EnsureErr: errors.New("ccpool new: did not reach ready")}
			h := newAbandonedHarness(t, cc, workerRole)
			h.role.CCPool.Isolation = roles.IsolationConfig{Type: typ, Path: filepath.Join(h.cfg.WorktreeDir, "scratch")}

			_ = h.dispatch(context.Background())
			if anyRemoveCall(h.opener) || anyDeleteBranchCall(h.opener) {
				t.Fatalf("isolation %q must never be removed; calls=%v", typ, h.opener.WTM.Calls)
			}
		})
	}
}
