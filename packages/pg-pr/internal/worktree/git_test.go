package worktree

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// ----------------------------------------------------------------------
// Helpers (local git "origin" fixtures — no network)
// ----------------------------------------------------------------------

// createPullHeadRef forges refs/pull/<pr>/head in dir pointing at HEAD. dir
// stands in for a PR's upstream repo (e.g. a GitHub-hosted origin) so a real,
// network-free `git fetch` against a local path can pull it in exactly the
// way CLIGitClient.FetchPR does against the real remote.
func createPullHeadRef(t *testing.T, dir string, pr int) {
	t.Helper()
	cmd := exec.Command("git", "-C", dir, "update-ref",
		fmt.Sprintf("refs/pull/%d/head", pr), "HEAD")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("update-ref refs/pull/%d/head: %v\n%s", pr, err, out)
	}
}

// advancePullHeadRef commits a new empty commit in dir and repoints
// refs/pull/<pr>/head at it, simulating a PR head advancing (new commits
// pushed to the PR) between two daemon review cycles.
func advancePullHeadRef(t *testing.T, dir string, pr int) {
	t.Helper()
	cmd := exec.Command("git", "-C", dir, "commit", "--allow-empty", "-m", "advance")
	cmd.Env = append(
		os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit --allow-empty: %v\n%s", err, out)
	}
	createPullHeadRef(t, dir, pr)
}

// gitConfig runs `git -C dir config <args...>`, failing the test on error.
func gitConfig(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "config"}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git config %v: %v\n%s", args, err, out)
	}
}

// ----------------------------------------------------------------------
// Regression test: FetchPR must survive fetch.prune=true across re-fetches
// ----------------------------------------------------------------------

// TestCLIGitClient_FetchPR_SurvivesPruneOnReFetch reproduces the daemon's real
// environment (fetch.prune=true) against a real local "origin" and a real
// `git fetch`. Every PR re-review re-fetches a PR head whose
// refs/remotes/origin/pr/<pr> already exists locally from the prior review.
// Before the refspec fix (no leading `+`, no --no-prune), git's prune pass
// deletes that already-present tracking ref (it doesn't match the default
// `refs/heads/*` fetch refspec's source side) and then fails to recreate it
// ("cannot lock ref ... unable to resolve reference"), exiting 1. This test
// must fail on the SECOND FetchPR call against the pre-fix refspec, and pass
// once the refspec is `+pull/<pr>/head:...` fetched with --no-prune.
func TestCLIGitClient_FetchPR_SurvivesPruneOnReFetch(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	originDir := filepath.Join(tmp, "origin")
	repoDir := filepath.Join(tmp, "repo")

	initRepo(t, originDir)
	createPullHeadRef(t, originDir, 7)

	initRepo(t, repoDir)
	// A plain local path works as a real remote for `git fetch` (no network
	// needed) — configureRemote only sets remote.origin.url.
	configureRemote(t, repoDir, originDir)
	// Reproduce the daemon's inherited fetch.prune=true.
	gitConfig(t, repoDir, "fetch.prune", "true")

	git := NewCLIGitClient()

	if err := git.FetchPR(ctx, repoDir, 7); err != nil {
		t.Fatalf("first FetchPR: %v", err)
	}

	// Simulate a re-review after the PR head advances — the scenario that
	// hits prune-then-fail under the old non-forcing refspec even though
	// origin/pr/7 already exists from the fetch above.
	advancePullHeadRef(t, originDir, 7)

	if err := git.FetchPR(ctx, repoDir, 7); err != nil {
		t.Fatalf("second FetchPR (PR head advanced, ref already local, fetch.prune=true) "+
			"must not fail: %v", err)
	}
}
