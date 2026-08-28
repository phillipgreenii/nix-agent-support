package detectors

import (
	"testing"

	"github.com/phillipgreenii/pa-monitor/internal/labels"
	"github.com/phillipgreenii/x/gitfixture"
	"github.com/phillipgreenii/x/gittest"
)

func TestNormaliseGitOrigin(t *testing.T) {
	cases := map[string]string{
		"git@github.com:owner/repo.git":      "github.com/owner/repo",
		"https://github.com/owner/repo.git":  "github.com/owner/repo",
		"https://github.com/owner/repo":      "github.com/owner/repo",
		"ssh://git@github.com/owner/repo":    "github.com/owner/repo",
		"git@gitlab.com:group/sub/repo.git":  "gitlab.com/group/sub/repo",
		"http://internal.example/owner/repo": "internal.example/owner/repo",
	}
	for in, want := range cases {
		if got := NormaliseOrigin(in); got != want {
			t.Errorf("NormaliseOrigin(%q) = %q, want %q", in, got, want)
		}
	}
}

type fakeRepoCache struct {
	calls []string
	val   string
	ok    bool
}

func (f *fakeRepoCache) RepoLabel(cwd string) (string, bool) {
	f.calls = append(f.calls, cwd)
	return f.val, f.ok
}

// When a Cache is wired, Detect delegates to it and does NOT spawn git.
func TestRepo_UsesCacheWhenSet(t *testing.T) {
	fc := &fakeRepoCache{val: "acme/x", ok: true}
	r := Repo{Cache: fc}
	got := r.Detect(labels.Session{ID: "s1", CWD: "/some/cwd"})
	if got["workspace.repo"] != "acme/x" {
		t.Fatalf("Detect via cache = %v, want workspace.repo=acme/x", got)
	}
	if len(fc.calls) != 1 || fc.calls[0] != "/some/cwd" {
		t.Fatalf("cache not consulted with the cwd: %v", fc.calls)
	}
}

func TestRepo_CacheMissReturnsNil(t *testing.T) {
	fc := &fakeRepoCache{ok: false}
	r := Repo{Cache: fc}
	if got := r.Detect(labels.Session{ID: "s1", CWD: "/some/cwd"}); got != nil {
		t.Fatalf("cache miss should yield nil label set, got %v", got)
	}
}

// TestRepoLabelFor_StillWorksUnleaked pins the ordinary, no-leak path
// through the real production git call (RepoLabelFor -> gitclient.Discover
// -> RemoteURL) so the leak-immunity test below cannot pass by RepoLabelFor
// never actually resolving anything.
func TestRepoLabelFor_StillWorksUnleaked(t *testing.T) {
	repo := gittest.New(t, gitfixture.RepoOptions{Suite: "plain"})
	if _, err := repo.Commit(t.Context(), "init", nil); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if _, err := repo.Client.Run(t.Context(), "remote", "add", "origin", "https://github.com/acme/plain.git"); err != nil {
		t.Fatalf("remote add: %v", err)
	}

	got, ok := RepoLabelFor(repo.Dir)
	if !ok {
		t.Fatalf("RepoLabelFor(%s) ok = false, want true", repo.Dir)
	}
	if got != "github.com/acme/plain" {
		t.Fatalf("RepoLabelFor(%s) = %q, want %q", repo.Dir, got, "github.com/acme/plain")
	}
}

// TestRepoLabelFor_IgnoresLeakedGitDir drives the real production
// RepoLabelFor (not a stub) with GIT_DIR/GIT_WORK_TREE naming a DIFFERENT
// repository than the cwd argument -- exactly what `git commit` from a
// linked worktree exports into every descendant process (pg2-67h4y's
// mechanism write-up) -- and asserts RepoLabelFor reports the repository it
// was HANDED, not the leaked one. RepoLabelFor resolves through
// gitclient.Discover (bead pg2-lv9jc), whose child environment is built
// from an explicit allowlist (PATH/HOME/SSH_AUTH_SOCK) and never inherits
// GIT_DIR/GIT_WORK_TREE/etc from the calling process's environment
// regardless of what it contains, so this proves the same guarantee
// gh.ExecBranchResolver.CurrentBranch's leak test
// (TestExecBranchResolver_CurrentBranch_IgnoresLeakedGitDir) proves for
// CETA's own resolver -- this is that test's sibling for pa-monitor,
// closing pg2-vc5bp's remaining regression-test gap for RepoLabelFor.
func TestRepoLabelFor_IgnoresLeakedGitDir(t *testing.T) {
	target := gittest.New(t, gitfixture.RepoOptions{Suite: "target"})
	if _, err := target.Commit(t.Context(), "init", nil); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if _, err := target.Client.Run(t.Context(), "remote", "add", "origin", "https://github.com/acme/target.git"); err != nil {
		t.Fatalf("remote add (target): %v", err)
	}

	leaked := gittest.New(t, gitfixture.RepoOptions{Suite: "leaked"})
	if _, err := leaked.Commit(t.Context(), "init", nil); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if _, err := leaked.Client.Run(t.Context(), "remote", "add", "origin", "https://github.com/acme/leaked.git"); err != nil {
		t.Fatalf("remote add (leaked): %v", err)
	}

	// Simulate the leak vector: GIT_DIR/GIT_WORK_TREE set in the ambient
	// environment (e.g. by an invoking git hook) pointing at a DIFFERENT
	// repository than the one RepoLabelFor is asked about.
	t.Setenv("GIT_DIR", leaked.Dir+"/.git")
	t.Setenv("GIT_WORK_TREE", leaked.Dir)

	got, ok := RepoLabelFor(target.Dir)
	if !ok {
		t.Fatalf("RepoLabelFor(%s) ok = false, want true", target.Dir)
	}
	if got != "github.com/acme/target" {
		t.Fatalf("RepoLabelFor(%s) = %q, want %q -- a leaked GIT_DIR/GIT_WORK_TREE silently overrode target %s", target.Dir, got, "github.com/acme/target", target.Dir)
	}
}
