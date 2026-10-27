package executor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/phillipgreenii/pg-router-handlers/internal/roles"
	"github.com/phillipgreenii/x/gitclient"
)

// Real-git guards for a role that runs with `path` isolation (bead pg2-96ogzy):
// the triager roles that need no working tree of their own. A dispatch of such a
// role MUST run no `git worktree add` and no `git worktree remove`, and the
// session's working directory MUST be the configured path.
//
// The repository comes from x/gittest through newRealGitHarness (hermetic: temp
// root, fixture HOME, allowlisted git environment) and stands in for the
// handler-wide RepoRoot a worktree-isolation role would have cut a worktree
// from. The configured path is a plain directory beside it, not a git
// repository, like the non-git workspace root the deployment points the role at.
// Every verdict reads real git state (`git worktree list`, `git branch`) AND a
// recording wrapper over the real client, so both a skipped call and a swallowed
// one are caught.

// recordingWTM forwards to a REAL gitclient and records each worktree mutation.
type recordingWTM struct {
	gitclient.WorktreeManager
	mu    *sync.Mutex
	calls *[]string
}

func (r recordingWTM) CreateWorktree(ctx context.Context, path, branch string, opts gitclient.CreateWorktreeOptions) (*gitclient.Handle, error) {
	r.record("add " + path)
	return r.WorktreeManager.CreateWorktree(ctx, path, branch, opts)
}

func (r recordingWTM) RemoveWorktree(ctx context.Context, path string, force bool) error {
	r.record("remove " + path)
	return r.WorktreeManager.RemoveWorktree(ctx, path, force)
}

func (r recordingWTM) record(c string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	*r.calls = append(*r.calls, c)
}

// withPathIsolation switches the harness's role to path isolation at a fresh
// non-git directory and routes every worktree mutation through a recorder.
func (h *realGitHarness) withPathIsolation() (path string, calls func() []string) {
	h.t.Helper()
	path = filepath.Join(filepath.Dir(h.repo.Dir), "workspace-root")
	h.role.CCPool.Isolation = roles.IsolationConfig{Type: "path", Path: path}

	var mu sync.Mutex
	var rec []string
	h.run.deps.GitOpener = func(ctx context.Context, dir string) (gitclient.WorktreeManager, error) {
		wtm, err := h.open(ctx, dir)
		if err != nil {
			return nil, err
		}
		return recordingWTM{WorktreeManager: wtm, mu: &mu, calls: &rec}, nil
	}
	return path, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), rec...)
	}
}

func (h *realGitHarness) worktreeList() string {
	h.t.Helper()
	out, err := h.repo.Client.Run(h.t.Context(), "worktree", "list", "--porcelain")
	if err != nil {
		h.t.Fatalf("git worktree list: %v", err)
	}
	return string(out)
}

// A path-isolated dispatch launches its session in the configured directory and
// makes no worktree, no anchor branch and no worktree removal, at either early
// return that reclaims a worktree for a worktree-isolation role.
func TestRealGitPathIsolation_dispatchRunsNoWorktreeAddOrRemove(t *testing.T) {
	for _, p := range abandonPoints {
		t.Run(p.name, func(t *testing.T) {
			h := newRealGitHarness(t)
			path, calls := h.withPathIsolation()

			if err := h.dispatch(p.cc(h.launchHook(nil))); err == nil {
				t.Fatal("an abandoned dispatch must report an error")
			}

			if l := h.atLaunch; !l.called || l.cwd != path {
				t.Fatalf("the session must launch with cwd = the configured path %s; saw %+v", path, l)
			}
			if l := h.atLaunch; l.dirExisted || l.branchExisted {
				t.Errorf("no per-bead worktree directory or anchor branch may exist at launch; saw %+v", l)
			}
			if got := calls(); len(got) != 0 {
				t.Errorf("path isolation must run no worktree add/remove; calls = %v", got)
			}
			if st, err := os.Stat(path); err != nil || !st.IsDir() {
				t.Errorf("the configured path must exist as a directory: %v", err)
			}
			if _, err := os.Stat(filepath.Join(path, ".git")); err == nil {
				t.Errorf("the configured path must stay a non-git directory")
			}
			if h.dirPresent() {
				t.Errorf("no per-bead worktree directory %s may be created", h.wt)
			}
			if h.branchPresent() {
				t.Errorf("no anchor branch %s may be created", realGitBranch)
			}
			// Only the main checkout is registered.
			if n := strings.Count(h.worktreeList(), "worktree "); n != 1 {
				t.Errorf("git must list exactly the main checkout; got:\n%s", h.worktreeList())
			}
		})
	}
}

// A worktree an earlier worktree-isolation attempt left for the same bead is not
// touched by a path-isolated dispatch (it is neither this role's nor removed).
func TestRealGitPathIsolation_leavesAnExistingBeadWorktreeAlone(t *testing.T) {
	for _, p := range abandonPoints {
		t.Run(p.name, func(t *testing.T) {
			h := newRealGitHarness(t)
			previousTip := h.leavePreviousAttempt()
			_, calls := h.withPathIsolation()

			if err := h.dispatch(p.cc(h.launchHook(nil))); err == nil {
				t.Fatal("an abandoned dispatch must report an error")
			}

			if got := calls(); len(got) != 0 {
				t.Errorf("path isolation must run no worktree add/remove; calls = %v", got)
			}
			h.wantKept()
			if got := h.branchTip(); got != previousTip {
				t.Errorf("branch %s must keep the previous attempt's commit %s, got %s", realGitBranch, previousTip, got)
			}
		})
	}
}
