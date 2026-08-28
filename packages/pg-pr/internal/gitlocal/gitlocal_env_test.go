package gitlocal

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/gitfixture"
)

// initRepo creates a fixture repo at dir with two commits on main -- an
// initial commit, then a second commit that adds file.txt -- and returns
// the second commit's SHA. Every git call goes through internal/gitfixture's
// allowlisted, hermetic environment (pg2-12795) so fixture setup itself
// cannot touch a real repo/config, even from inside a git hook that leaked
// GIT_DIR/GIT_WORK_TREE into this test process's own environment.
func initRepo(t *testing.T, dir string) string {
	t.Helper()
	run := func(args ...string) string {
		t.Helper()
		return gitfixture.MustRun(t, dir, args...)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := gitfixture.Run(t, dir, "init", "-b", "main"); err != nil {
		// Fallback for older git versions.
		if _, err2 := gitfixture.Run(t, dir, "init"); err2 != nil {
			t.Fatalf("git init: %v", err2)
		}
		run("symbolic-ref", "HEAD", "refs/heads/main")
	}

	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "README.md")
	run("commit", "-m", "init")

	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "file.txt")
	run("commit", "-m", "add file")
	return run("rev-parse", "HEAD")
}

// leakGitLocationVars puts this process into the state a `git commit` from
// a LINKED WORKTREE exports into every descendant of the pre-commit hook:
// GIT_DIR and friends naming a path that is NOT the directory the caller
// will pass. The value points at a path that is not a repository at all,
// so a call that inherits it fails loudly instead of silently redirecting
// onto some other real repo. See pg2-lx41y/pg2-bh09g for the mechanism.
func leakGitLocationVars(t *testing.T) {
	t.Helper()
	leaked := filepath.Join(t.TempDir(), "leaked-git-dir")
	t.Setenv("GIT_DIR", leaked)
	t.Setenv("GIT_WORK_TREE", leaked)
	t.Setenv("GIT_INDEX_FILE", filepath.Join(leaked, "index"))
	t.Setenv("GIT_COMMON_DIR", leaked)
}

// TestChangedFiles_StaysInDirUnderLeakedGitDir and
// TestCommits_StaysInDirUnderLeakedGitDir are regression pins for
// pg2-lx41y/pg2-bh09g through gitlocal's migration onto x/gitclient
// (pg2-kcucl): ChangedFiles/Commits, driven with r == nil (the real
// production path through openGit), must resolve the dir they were
// explicitly given, not a repository named by a leaked ambient
// GIT_DIR/GIT_WORK_TREE. Before this migration the mechanism under test
// here was internal/gitenv's CLIRunner (pg2-lx41y); x/gitclient's Client
// builds its child environment from its own allowlist (PATH/HOME/
// SSH_AUTH_SOCK plus explicit extras) rather than inheriting os.Environ(),
// so this passes by construction now.
func TestChangedFiles_StaysInDirUnderLeakedGitDir(t *testing.T) {
	repo := t.TempDir()
	initRepo(t, repo)
	leakGitLocationVars(t)

	files, err := ChangedFiles(context.Background(), nil, repo, "HEAD~1")
	if err != nil {
		t.Fatalf("ChangedFiles inherited the leaked GIT_DIR and could not resolve %s: %v", repo, err)
	}
	if len(files) != 1 || files[0].Path != "file.txt" {
		t.Fatalf("ChangedFiles resolved the wrong repository: got %+v, want [file.txt]", files)
	}
}

func TestCommits_StaysInDirUnderLeakedGitDir(t *testing.T) {
	repo := t.TempDir()
	wantSHA := initRepo(t, repo)
	leakGitLocationVars(t)

	commits, err := Commits(context.Background(), nil, repo, "HEAD~1")
	if err != nil {
		t.Fatalf("Commits inherited the leaked GIT_DIR and could not resolve %s: %v", repo, err)
	}
	if len(commits) != 1 || commits[0].SHA != wantSHA {
		t.Fatalf("Commits resolved the wrong repository: got %+v, want SHA %s", commits, wantSHA)
	}
}
