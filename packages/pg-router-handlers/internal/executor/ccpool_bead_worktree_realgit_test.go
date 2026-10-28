package executor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/phillipgreenii/x/gitclient"
	"github.com/phillipgreenii/x/gitfixture"
	"github.com/phillipgreenii/x/gittest"

	"github.com/phillipgreenii/pg-router-handlers/internal/ccpool"
	"github.com/phillipgreenii/pg-router-handlers/internal/dtest"
	"github.com/phillipgreenii/pg-router-handlers/internal/roles"
)

// Real-git guards for "bead-worktree" isolation (bead pg2-b49ydt): a role that
// only LOOKS at the worktree a drain worker left on drain/<bead>. A dispatch
// MUST run no `git worktree add` and no `git worktree remove`, its session's
// working directory is the found worktree (or the configured fallback), and
// neither the dispatch's early returns nor its worktree cleanup may touch what
// it found. Repositories come from x/gittest (hermetic); every verdict reads
// real git state AND a recording wrapper over the real client.

const drainBranch = "drain/" + realGitBead

// withBeadWorktree switches the harness's role to bead-worktree isolation over
// repos (default: the fixture repo) with a fresh, plain fallback directory, and
// routes every worktree mutation through a recorder.
func (h *realGitHarness) withBeadWorktree(repos ...string) (fallback string, calls func() []string) {
	h.t.Helper()
	if len(repos) == 0 {
		repos = []string{h.repo.Dir}
	}
	fallback = filepath.Join(filepath.Dir(h.repo.Dir), "workspace-root")
	if err := os.Mkdir(fallback, 0o755); err != nil {
		h.t.Fatal(err)
	}
	h.role.CCPool.Isolation = roles.IsolationConfig{Type: BeadWorktreeIsolation, Repos: repos, Fallback: fallback}

	var mu sync.Mutex
	var rec []string
	h.run.deps.GitOpener = func(ctx context.Context, dir string) (gitclient.WorktreeManager, error) {
		wtm, err := h.open(ctx, dir)
		if err != nil {
			return nil, err
		}
		return recordingWTM{WorktreeManager: wtm, mu: &mu, calls: &rec}, nil
	}
	return fallback, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), rec...)
	}
}

// leaveDrainWorktree creates repo's worktree on drain/<bead> at dir the way a
// drain worker does, with one commit and one uncommitted file, and returns the
// branch tip.
func leaveDrainWorktree(t *testing.T, repo *gitfixture.Repo, dir string) (tip string) {
	t.Helper()
	ctx := t.Context()
	if out, err := repo.Client.Run(ctx, "worktree", "add", "-b", drainBranch, dir); err != nil {
		t.Fatalf("git worktree add: %v\n%s", err, out)
	}
	wt, err := gitclient.New(ctx, dir, gitclient.WithHome(filepath.Join(filepath.Dir(repo.Dir), "home")), gitclient.WithEnv("GIT_CONFIG_NOSYSTEM", "1"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "work.txt"), []byte("the worker's commit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "work.txt"}, {"-c", "user.name=t", "-c", "user.email=t@example.test", "commit", "-q", "-m", "worker commit"}} {
		if out, err := wt.Run(ctx, args...); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "uncommitted.txt"), []byte("not committed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := repo.Client.Run(ctx, "rev-parse", "--verify", "refs/heads/"+drainBranch)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

// wantDrainWorktreeIntact fails unless git still lists dir on drain/<bead> at
// tip and both the committed and the uncommitted file are still there.
func wantDrainWorktreeIntact(t *testing.T, repo *gitfixture.Repo, dir, tip string) {
	t.Helper()
	list, err := repo.Client.Run(t.Context(), "worktree", "list", "--porcelain")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(list), "worktree "+dir+"\n") || !strings.Contains(string(list), "branch refs/heads/"+drainBranch) {
		t.Errorf("git must still list %s on %s:\n%s", dir, drainBranch, list)
	}
	out, err := repo.Client.Run(t.Context(), "rev-parse", "--verify", "refs/heads/"+drainBranch)
	if err != nil || strings.TrimSpace(string(out)) != tip {
		t.Errorf("branch %s must still be at %s, got %q (err %v)", drainBranch, tip, out, err)
	}
	for _, f := range []string{"work.txt", "uncommitted.txt"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("%s must be left in place: %v", f, err)
		}
	}
}

func newOtherRepo(t *testing.T) *gitfixture.Repo {
	t.Helper()
	repo := gittest.New(t, gitfixture.RepoOptions{Suite: "bead-worktree-other"})
	if _, err := repo.Commit(t.Context(), "init", map[string]string{"tracked.txt": "committed\n"}); err != nil {
		t.Fatalf("fixture initial commit: %v", err)
	}
	return repo
}

// A bead with a drain/<bead> worktree launches its session in THAT worktree, runs
// no worktree add/remove, and a dispatch that is abandoned at either early return
// (the two that reclaim a worktree for a worktree-isolation role) leaves it
// exactly as it was, uncommitted file included.
func TestRealGitBeadWorktree_foundWorktreeIsTheCwdAndSurvivesTheDispatch(t *testing.T) {
	for _, p := range abandonPoints {
		t.Run(p.name, func(t *testing.T) {
			h := newRealGitHarness(t)
			found := filepath.Join(filepath.Dir(h.repo.Dir), "drain-wt")
			tip := leaveDrainWorktree(t, h.repo, found)
			_, calls := h.withBeadWorktree()

			cc := p.cc(h.launchHook(nil))
			if err := h.dispatch(cc); err == nil {
				t.Fatal("an abandoned dispatch must report an error")
			}

			if l := h.atLaunch; !l.called || l.cwd != found {
				t.Fatalf("the session must launch with cwd = the found worktree %s; saw %+v", found, l)
			}
			if cc.EnsuredEnv["WORKSPACE_ROOT"] != found {
				t.Errorf("WORKSPACE_ROOT = %q, want the found worktree %s", cc.EnsuredEnv["WORKSPACE_ROOT"], found)
			}
			if cc.EnsuredMeta[ccpool.MetaKeyKeepWorktree] == "" {
				t.Errorf("the session row must carry %s so no teardown removes the directory; meta=%v", ccpool.MetaKeyKeepWorktree, cc.EnsuredMeta)
			}
			if got := calls(); len(got) != 0 {
				t.Errorf("bead-worktree isolation must run no worktree add/remove; calls = %v", got)
			}
			if h.dirPresent() || h.branchPresent() {
				t.Errorf("no per-bead worktree %s or anchor branch %s may be created", h.wt, realGitBranch)
			}
			wantDrainWorktreeIntact(t, h.repo, found, tip)
		})
	}
}

// With no drain/<bead> worktree the session runs in the configured fallback and
// nothing is created: no worktree, no branch, no directory.
func TestRealGitBeadWorktree_noWorktreeUsesTheFallbackAndCreatesNothing(t *testing.T) {
	for _, p := range abandonPoints {
		t.Run(p.name, func(t *testing.T) {
			h := newRealGitHarness(t)
			fallback, calls := h.withBeadWorktree()
			before := h.worktreeList()

			cc := p.cc(h.launchHook(nil))
			if err := h.dispatch(cc); err == nil {
				t.Fatal("an abandoned dispatch must report an error")
			}

			if l := h.atLaunch; !l.called || l.cwd != fallback {
				t.Fatalf("the session must launch with cwd = the fallback %s; saw %+v", fallback, l)
			}
			if got := calls(); len(got) != 0 {
				t.Errorf("no drain worktree means no git worktree add or remove at all; calls = %v", got)
			}
			if after := h.worktreeList(); after != before {
				t.Errorf("git's worktree list must not change:\n%s\nvs\n%s", before, after)
			}
			if h.dirPresent() || h.branchPresent() {
				t.Errorf("no per-bead worktree %s or anchor branch %s may be created", h.wt, realGitBranch)
			}
			if _, err := os.Stat(filepath.Join(fallback, ".git")); err == nil {
				t.Errorf("the fallback must stay a non-git directory")
			}
		})
	}
}

// A fallback directory that does not exist is an error: nothing is created to
// make it work.
func TestRealGitBeadWorktree_missingFallbackFailsAndCreatesNothing(t *testing.T) {
	h := newRealGitHarness(t)
	fallback, calls := h.withBeadWorktree()
	if err := os.Remove(fallback); err != nil {
		t.Fatal(err)
	}

	err := h.dispatch(launchHookCC{FakeCC: &dtest.FakeCC{}, onLaunch: h.launchHook(nil)})
	if err == nil || !strings.Contains(err.Error(), "fallback") {
		t.Fatalf("want an error naming the fallback, got %v", err)
	}
	if h.atLaunch.called {
		t.Errorf("no session may launch; saw %+v", h.atLaunch)
	}
	if _, statErr := os.Stat(fallback); statErr == nil {
		t.Errorf("the fallback must not be created")
	}
	if got := calls(); len(got) != 0 {
		t.Errorf("no git worktree call may run; calls = %v", got)
	}
}

// A bead found in two configured repositories fails the dispatch with an error
// naming both, in either configured order; no session launches and both worktrees
// are left alone.
func TestRealGitBeadWorktree_foundInTwoReposFailsLoudAndTouchesNothing(t *testing.T) {
	for _, reversed := range []bool{false, true} {
		name := "configured order"
		if reversed {
			name = "reversed order"
		}
		t.Run(name, func(t *testing.T) {
			h := newRealGitHarness(t)
			other := newOtherRepo(t)
			wtA := filepath.Join(filepath.Dir(h.repo.Dir), "drain-wt-a")
			wtB := filepath.Join(filepath.Dir(other.Dir), "drain-wt-b")
			tipA := leaveDrainWorktree(t, h.repo, wtA)
			tipB := leaveDrainWorktree(t, other, wtB)
			repos := []string{h.repo.Dir, other.Dir}
			if reversed {
				repos = []string{other.Dir, h.repo.Dir}
			}
			_, calls := h.withBeadWorktree(repos...)

			err := h.dispatch(launchHookCC{FakeCC: &dtest.FakeCC{}, onLaunch: h.launchHook(nil)})
			if err == nil {
				t.Fatal("a bead found in two repositories must fail the dispatch, not pick one")
			}
			for _, want := range []string{"more than one", wtA, wtB} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error must name %q; got: %v", want, err)
				}
			}
			if h.atLaunch.called {
				t.Errorf("no session may launch; saw %+v", h.atLaunch)
			}
			if got := calls(); len(got) != 0 {
				t.Errorf("no git worktree call may run; calls = %v", got)
			}
			wantDrainWorktreeIntact(t, h.repo, wtA, tipA)
			wantDrainWorktreeIntact(t, other, wtB, tipB)
		})
	}
}

// A bead found in only the SECOND configured repository is found there.
func TestRealGitBeadWorktree_searchesEveryConfiguredRepo(t *testing.T) {
	h := newRealGitHarness(t)
	other := newOtherRepo(t)
	found := filepath.Join(filepath.Dir(other.Dir), "drain-wt-b")
	tip := leaveDrainWorktree(t, other, found)
	h.withBeadWorktree(h.repo.Dir, other.Dir)

	cc := abandonPoints[0].cc(h.launchHook(nil))
	_ = h.dispatch(cc)
	if l := h.atLaunch; !l.called || l.cwd != found {
		t.Fatalf("the session must launch in %s, found through the second repo; saw %+v", found, l)
	}
	wantDrainWorktreeIntact(t, other, found, tip)
}

// The settle step's worktree cleanup (cleanupWorktree, which removes a
// worktree-isolation role's per-bead worktree once its session is done) must
// leave a found drain worktree alone. A nil session list means no live peer, no
// needs_input and no activity: every guard but the isolation type is open.
func TestRealGitBeadWorktree_cleanupWorktreeLeavesTheFoundWorktree(t *testing.T) {
	h := newRealGitHarness(t)
	found := filepath.Join(filepath.Dir(h.repo.Dir), "drain-wt")
	tip := leaveDrainWorktree(t, h.repo, found)
	_, calls := h.withBeadWorktree()
	h.run.deps.CC = &dtest.FakeCC{}

	h.run.cleanupWorktree(h.t.Context(), h.role.CCPool, realGitExt, realGitBead, found)
	h.run.reclaimAbandonedWorktree(h.t.Context(), h.role.CCPool, realGitBead, found, true)

	if got := calls(); len(got) != 0 {
		t.Errorf("no worktree removal may run; calls = %v", got)
	}
	wantDrainWorktreeIntact(t, h.repo, found, tip)
}
