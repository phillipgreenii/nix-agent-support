package executor

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/ccpool"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/dtest"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/item"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/roles"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/worktree"
	"github.com/phillipgreenii/x/gitclient"
	"github.com/phillipgreenii/x/gitfixture"
	"github.com/phillipgreenii/x/gittest"
)

// Real-git regression guards for the abandoned-worktree reclaim (pg2-w3usi),
// added by pg2-dicwc for its acceptance criterion "a pre-existing, dirty or
// live-session worktree is never removed by the reclaim path".
//
// ccpool_abandoned_worktree_test.go proves the same guards against
// dtest.NoopGitOpener, which records calls and never runs git. A recorder
// cannot show what git itself does with a dirty tree (a non-force
// `git worktree remove` refuses it) or that the pg-router/<bead> branch really
// survives, so here run()'s two early returns -- the ones that call
// reclaimAbandonedWorktree -- are driven against a REAL repository:
//
//   - the repository comes from x/gittest (hermetic by construction: a temp
//     root, a fixture HOME and an allowlisted git environment);
//   - the code under test reaches git through the real x/gitclient opener,
//     built with the fixture's own child-environment options;
//   - sessions stay on dtest.FakeCC (ccpool itself is not what is proven);
//   - every verdict reads the real worktree directory and real git state,
//     never a recorded call.
//
// Each scenario runs once per early return, so a call site that stops passing
// the "created here" fact (or that skips the guards) is caught on its own.

const (
	realGitBead   = "bead-1"
	realGitBranch = "pg-router/" + realGitBead
	// realGitExt is the per-attempt ExternalID the orchestrator would stamp.
	realGitExt = "pg-router-worker-" + realGitBead
)

// launchHookCC runs onLaunch inside CC.Ensure -- after the real worktree exists
// and before any session owns it -- then answers like the wrapped FakeCC, so an
// EnsureErr still fails the launch.
type launchHookCC struct {
	*dtest.FakeCC
	onLaunch func(cwd string)
}

func (c launchHookCC) Ensure(ctx context.Context, externalID, name, cwd string, env, meta map[string]string) error {
	err := c.FakeCC.Ensure(ctx, externalID, name, cwd, env, meta)
	if c.onLaunch != nil {
		c.onLaunch(cwd)
	}
	return err
}

// abandonPoint is one of the two early returns of run() that call
// reclaimAbandonedWorktree. cc scripts the session fake so the dispatch gives up
// there; onLaunch runs inside CC.Ensure, with the worktree path, while the
// worktree exists and no session owns it.
type abandonPoint struct {
	name string
	cc   func(onLaunch func(cwd string)) launchHookCC
}

var abandonPoints = []abandonPoint{
	{"session launch fails", func(onLaunch func(string)) launchHookCC {
		return launchHookCC{FakeCC: &dtest.FakeCC{EnsureErr: errors.New("ccpool new: did not reach ready")}, onLaunch: onLaunch}
	}},
	{"nudge not ingested", func(onLaunch func(string)) launchHookCC {
		return launchHookCC{FakeCC: &dtest.FakeCC{SendErr: ccpool.ErrPromptNotIngested}, onLaunch: onLaunch}
	}},
}

// launchSeen is what launchHook observed inside CC.Ensure.
type launchSeen struct {
	called        bool
	cwd           string
	dirExisted    bool
	branchExisted bool
}

type realGitHarness struct {
	t       *testing.T
	repo    *gitfixture.Repo
	gitOpts []gitclient.Option // the fixture's own child-environment options
	wtDir   string             // Cfg.WorktreeDir, beside the fixture repository
	wt      string             // wtDir/<bead>: where run() puts this bead's worktree
	run     *ccpoolRun
	role    roles.Role

	atLaunch launchSeen
}

// newRealGitHarness builds a real repository (one commit, so HEAD resolves and
// the dirty cases have a committed file to modify) and a ccpoolRun whose git
// opener is the production shape, gitclient.New anchored at the requested
// directory, over the fixture's hermetic environment. Everything lives under the
// fixture's temp root.
func newRealGitHarness(t *testing.T) *realGitHarness {
	t.Helper()
	repo := gittest.New(t, gitfixture.RepoOptions{})
	if _, err := repo.Commit(t.Context(), "init", map[string]string{"tracked.txt": "committed\n"}); err != nil {
		t.Fatalf("fixture initial commit: %v", err)
	}
	root := filepath.Dir(repo.Dir) // gitfixture layout: <root>/{repo,home,hooks}
	h := &realGitHarness{
		t:    t,
		repo: repo,
		gitOpts: []gitclient.Option{
			gitclient.WithHome(filepath.Join(root, "home")),
			gitclient.WithoutInherited("SSH_AUTH_SOCK"),
			gitclient.WithCeiling(root),
			gitclient.WithEnv("GIT_CONFIG_NOSYSTEM", "1"),
		},
		wtDir: filepath.Join(root, "worktrees"),
	}
	h.wt = filepath.Join(h.wtDir, realGitBead)

	cfg := fastCfg()
	cfg.RepoRoot = repo.Dir
	cfg.WorktreeDir = h.wtDir
	bd := &dtest.ScriptBD{Show: map[string]string{realGitBead: `{"id":"` + realGitBead + `","status":"open","labels":[]}`}}
	h.role = workerRole(cfg)
	h.run = &ccpoolRun{deps: newExec(&dtest.FakeCC{}, bd, cfg).deps}
	h.run.deps.ExternalID = realGitExt
	h.run.deps.Git = &dtest.NoopGit{} // the watchdog's reset/clean seam; the early returns never reach it
	h.run.deps.GitOpener = h.open
	return h
}

func (h *realGitHarness) client(ctx context.Context, dir string) (*gitclient.Client, error) {
	return gitclient.New(ctx, dir, h.gitOpts...)
}

// open is a worktree.Opener returning a REAL gitclient. A nil *Client must not
// leak into the interface, so the error path returns an untyped nil.
func (h *realGitHarness) open(ctx context.Context, dir string) (gitclient.WorktreeManager, error) {
	c, err := h.client(ctx, dir)
	if err != nil {
		return nil, err
	}
	return c, nil
}

func (h *realGitHarness) dispatch(cc ccpool.Runner) error {
	h.t.Helper()
	h.run.deps.CC = cc
	_, err := h.run.run(h.t.Context(), DispatchContext{Role: h.role, Item: item.Item{ID: realGitBead}})
	return err
}

// launchHook returns the CC.Ensure hook for abandonPoint.cc. It records whether
// the real worktree and anchor branch exist at the moment the session would be
// launched, then runs act (if any) on the worktree.
func (h *realGitHarness) launchHook(act func(cwd string)) func(cwd string) {
	return func(cwd string) {
		h.atLaunch = launchSeen{called: true, cwd: cwd, dirExisted: h.dirPresent(), branchExisted: h.branchPresent()}
		if act != nil {
			act(cwd)
		}
	}
}

// wantLaunchedIntoRealWorktree fails unless the dispatch really had a worktree
// and its anchor branch in place when it was abandoned. Without it a "kept"
// verdict could pass for a dispatch that never got that far.
func (h *realGitHarness) wantLaunchedIntoRealWorktree() {
	h.t.Helper()
	if l := h.atLaunch; !l.called || l.cwd != h.wt || !l.dirExisted || !l.branchExisted {
		h.t.Fatalf("the dispatch must reach the session launch inside a real worktree at %s on branch %s; saw %+v", h.wt, realGitBranch, l)
	}
}

func (h *realGitHarness) dirPresent() bool {
	_, err := os.Stat(h.wt)
	return err == nil
}

func (h *realGitHarness) branchPresent() bool {
	h.t.Helper()
	ok, err := h.repo.Client.RefExists(h.t.Context(), "refs/heads/"+realGitBranch)
	if err != nil {
		h.t.Fatalf("git rev-parse refs/heads/%s: %v", realGitBranch, err)
	}
	return ok
}

func (h *realGitHarness) branchTip() string {
	h.t.Helper()
	out, err := h.repo.Client.Run(h.t.Context(), "rev-parse", "--verify", "refs/heads/"+realGitBranch)
	if err != nil {
		h.t.Fatalf("git rev-parse %s: %v", realGitBranch, err)
	}
	return strings.TrimSpace(string(out))
}

// registered reports whether git itself still lists the worktree, which a bare
// directory check cannot tell from a leftover directory.
func (h *realGitHarness) registered() bool {
	h.t.Helper()
	out, err := h.repo.Client.Run(h.t.Context(), "worktree", "list", "--porcelain")
	if err != nil {
		h.t.Fatalf("git worktree list: %v", err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if line == "worktree "+h.wt {
			return true
		}
	}
	return false
}

func (h *realGitHarness) wantReclaimed() {
	h.t.Helper()
	if h.dirPresent() {
		h.t.Errorf("worktree directory %s must be removed", h.wt)
	}
	if h.registered() {
		h.t.Errorf("git must no longer list the worktree %s", h.wt)
	}
	if h.branchPresent() {
		h.t.Errorf("branch %s must be deleted", realGitBranch)
	}
}

func (h *realGitHarness) wantKept() {
	h.t.Helper()
	if !h.dirPresent() {
		h.t.Errorf("worktree directory %s must be kept", h.wt)
	}
	if !h.registered() {
		h.t.Errorf("git must still list the worktree %s", h.wt)
	}
	if !h.branchPresent() {
		h.t.Errorf("branch %s must be kept", realGitBranch)
	}
}

// leavePreviousAttempt puts the worktree and anchor branch in place BEFORE the
// dispatch, the way an earlier attempt of the same bead left them (through
// worktree.Ensure, which dispatch itself uses), plus a commit of that attempt's
// work on the branch. It returns the branch tip.
func (h *realGitHarness) leavePreviousAttempt() string {
	h.t.Helper()
	ctx := h.t.Context()
	if _, err := worktree.Ensure(ctx, h.open, h.wtDir, h.repo.Dir, realGitBead); err != nil {
		h.t.Fatalf("previous attempt's worktree: %v", err)
	}
	c, err := h.client(ctx, h.wt)
	if err != nil {
		h.t.Fatalf("open previous attempt's worktree: %v", err)
	}
	if _, err := c.Run(ctx, "commit", "--allow-empty", "-m", "previous attempt's work"); err != nil {
		h.t.Fatalf("previous attempt's commit: %v", err)
	}
	return h.branchTip()
}

// captureRealGitLog redirects slog's default logger into the returned buffer
// until the test ends (the technique the other executor tests use).
func captureRealGitLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	return &buf
}

func wantLogged(t *testing.T, logs *bytes.Buffer, wants ...string) {
	t.Helper()
	out := logs.String()
	for _, w := range wants {
		if !strings.Contains(out, w) {
			t.Errorf("log must contain %q; got:\n%s", w, out)
		}
	}
}

// Control: a clean worktree this dispatch created, with no live session row, IS
// reclaimed -- directory, git's registration of it, and the anchor branch. The
// "kept" tests below change one thing each, so this proves the harness reaches
// the reclaim and that git really removes what is kept elsewhere.
func TestRealGitReclaim_cleanWorktreeCreatedHere_isReclaimed(t *testing.T) {
	for _, p := range abandonPoints {
		t.Run(p.name, func(t *testing.T) {
			logs := captureRealGitLog(t)
			h := newRealGitHarness(t)

			if err := h.dispatch(p.cc(h.launchHook(nil))); err == nil {
				t.Fatal("an abandoned dispatch must report an error")
			}

			h.wantLaunchedIntoRealWorktree()
			h.wantReclaimed()
			wantLogged(t, logs,
				"dispatch: reclaiming worktree of a dispatch that never reached a running session",
				"dispatch: worktree removed",
				"dispatch: branch deleted")
		})
	}
}

// A worktree holding uncommitted work by the time the reclaim runs is kept: the
// removal is non-force, so git itself refuses a dirty tree, and the anchor
// branch is only deleted once the worktree is gone.
func TestRealGitReclaim_dirtyWorktree_isKept(t *testing.T) {
	dirtiers := []struct{ name, file, content string }{
		{"untracked file", "wip.txt", "uncommitted work\n"},
		{"modified tracked file", "tracked.txt", "edited, not committed\n"},
	}
	for _, p := range abandonPoints {
		t.Run(p.name, func(t *testing.T) {
			for _, d := range dirtiers {
				t.Run(d.name, func(t *testing.T) {
					logs := captureRealGitLog(t)
					h := newRealGitHarness(t)
					dirty := func(cwd string) {
						if err := os.WriteFile(filepath.Join(cwd, d.file), []byte(d.content), 0o644); err != nil {
							t.Fatalf("dirty the worktree: %v", err)
						}
					}

					if err := h.dispatch(p.cc(h.launchHook(dirty))); err == nil {
						t.Fatal("an abandoned dispatch must report an error")
					}

					h.wantLaunchedIntoRealWorktree()
					h.wantKept()
					if got, err := os.ReadFile(filepath.Join(h.wt, d.file)); err != nil || string(got) != d.content {
						t.Errorf("the uncommitted work must survive intact: content %q, err %v", got, err)
					}
					// It is git's own refusal that kept it, not some other early exit.
					wantLogged(t, logs, "worktree cleanup: remove failed", "modified or untracked files")
				})
			}
		})
	}
}

// A worktree that already existed before this dispatch (an earlier attempt's,
// with that attempt's commit on the anchor branch) is never reclaimed by it,
// even though it is clean and no session is live: it is left to finishWait and
// the reconciles.
func TestRealGitReclaim_preexistingWorktree_isKept(t *testing.T) {
	for _, p := range abandonPoints {
		t.Run(p.name, func(t *testing.T) {
			h := newRealGitHarness(t)
			previousTip := h.leavePreviousAttempt()

			if err := h.dispatch(p.cc(h.launchHook(nil))); err == nil {
				t.Fatal("an abandoned dispatch must report an error")
			}

			h.wantLaunchedIntoRealWorktree()
			h.wantKept()
			if !h.branchPresent() {
				return // wantKept has already reported it
			}
			if got := h.branchTip(); got != previousTip {
				t.Errorf("branch %s must still hold the previous attempt's commit %s, got %s", realGitBranch, previousTip, got)
			}
		})
	}
}

// A session of this dispatch that is still live, or whose liveness cannot be
// read, is never deprived of its working directory.
func TestRealGitReclaim_liveOwnSession_isKept(t *testing.T) {
	scenarios := []struct {
		name   string
		script func(h *realGitHarness, cc launchHookCC)
		logged string
	}{
		{
			name: "own session row is live",
			script: func(h *realGitHarness, cc launchHookCC) {
				// The purge-close did not stop it, so it may still reach ready.
				cc.CloseErr = errors.New("ccpool close: boom")
				cc.ListSeq = [][]ccpool.Session{{{ExternalID: realGitExt, Live: true, State: ccpool.StateStarting, CWD: h.wt}}}
			},
			logged: "abandoned worktree kept -- its session is still live",
		},
		{
			name: "session list unreadable",
			script: func(_ *realGitHarness, cc launchHookCC) {
				cc.ListErr = errors.New("ccpool list: transient")
			},
			logged: "abandoned worktree left for next sweep -- session list failed",
		},
	}
	for _, p := range abandonPoints {
		t.Run(p.name, func(t *testing.T) {
			for _, s := range scenarios {
				t.Run(s.name, func(t *testing.T) {
					logs := captureRealGitLog(t)
					h := newRealGitHarness(t)
					cc := p.cc(h.launchHook(nil))
					s.script(h, cc)

					if err := h.dispatch(cc); err == nil {
						t.Fatal("an abandoned dispatch must report an error")
					}

					h.wantLaunchedIntoRealWorktree()
					h.wantKept()
					wantLogged(t, logs, s.logged)
				})
			}
		})
	}
}
