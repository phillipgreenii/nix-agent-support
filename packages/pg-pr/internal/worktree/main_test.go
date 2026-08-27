package worktree

import (
	"os"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/gitfixture"
)

// TestMain scrubs GIT_DIR/GIT_WORK_TREE/etc from this process's own
// environment before any test runs (pg2-12795). A git hook (pre-commit/prek,
// invoking `go test` for this very package as its run-unit-tests hook)
// exports GIT_DIR/GIT_WORK_TREE for the commit in progress, and this test
// binary inherits that. `-C <dir>` and even an explicit repo path argument
// passed to CLIGitClient's methods (RepoFromRemote, CreateWorktree, FetchPR,
// ...) do NOT override these — git's own repo discovery consults them FIRST
// — so a test that builds an isolated fixture under t.TempDir() and drives
// this package's real git-invoking code (PRODUCTION runGit, git.go) would
// otherwise silently operate on the AMBIENT repo (this checkout) instead of
// the fixture: confirmed 2026-08-27 (pg2-5ek6b/pg2-12795) —
// TestRepoFromRemoteParsesGitHubURLs and friends reported this checkout's
// own owner/repo instead of the fixture's "owner/repo" under a real
// commit-time hook run, though a plain `go test` from an uncontaminated
// shell never reproduces it. Scrubbing here, once, for the whole process is
// sufficient: nothing in this package's tests or the code under test
// re-sets them. See internal/gitfixture's doc comment for the shared
// rationale and the regression test pinning this behavior.
func TestMain(m *testing.M) {
	gitfixture.ScrubProcessGitEnv()
	os.Exit(m.Run())
}
