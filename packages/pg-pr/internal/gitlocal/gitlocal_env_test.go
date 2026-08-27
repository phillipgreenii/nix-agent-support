package gitlocal

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/gitenv"
)

// TestCLIRunnerStaysInDirUnderLeakedGitDir drives the production Runner with
// GIT_DIR and friends naming a path that is not a repository — the state a
// `git commit` from a LINKED WORKTREE puts every descendant of the pre-commit
// hook into. git's repo discovery consults those vars BEFORE `-C dir`, so
// without gitenv.Command owning the child environment this call reports on the
// leaked repository instead of dir. See internal/gitenv (pg2-lx41y).
func TestCLIRunnerStaysInDirUnderLeakedGitDir(t *testing.T) {
	repo := t.TempDir()
	// Built through gitenv.Command so the fixture cannot touch a real repo
	// even while this test is deliberately leaking GIT_DIR below.
	cmd := gitenv.Command(context.Background(), repo, "init", "-b", "main")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}

	leaked := filepath.Join(t.TempDir(), "leaked-git-dir")
	t.Setenv("GIT_DIR", leaked)
	t.Setenv("GIT_WORK_TREE", leaked)
	t.Setenv("GIT_INDEX_FILE", filepath.Join(leaked, "index"))
	t.Setenv("GIT_COMMON_DIR", leaked)

	out, err := CLIRunner{}.Run(context.Background(), repo, "rev-parse", "--absolute-git-dir")
	if err != nil {
		t.Fatalf("CLIRunner inherited the leaked GIT_DIR and could not resolve %s: %v", repo, err)
	}

	resolved, err := filepath.EvalSymlinks(repo)
	if err != nil {
		resolved = repo
	}
	want := filepath.Join(resolved, ".git")
	if got := strings.TrimSpace(string(out)); got != want {
		t.Fatalf("CLIRunner resolved the wrong repository\n got: %s\nwant: %s", got, want)
	}
}
