package drain

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/pb/internal/run"
)

// gitEnvVars are the variables git exports into a hook's environment (GIT_DIR,
// GIT_INDEX_FILE, GIT_WORK_TREE, GIT_PREFIX, GIT_OBJECT_DIRECTORY,
// GIT_COMMON_DIR). Inherited by a child process, they repoint tempdir git calls
// at the REAL repo instead of the test's t.TempDir() — breaking hermeticity
// whenever this package's tests run inside a `git commit` hook (this repo's own
// run-unit-tests pre-commit hook runs `go test ./...`). Fixed the same way as
// packages/pb/internal/patchid/patchid_test.go's hermeticEnviron helper.
var gitEnvVars = []string{
	"GIT_DIR", "GIT_INDEX_FILE", "GIT_WORK_TREE",
	"GIT_PREFIX", "GIT_OBJECT_DIRECTORY", "GIT_COMMON_DIR",
}

// hermeticEnviron returns os.Environ() with the git hook-inherited variables
// removed. Mirrors internal/patchid's hermeticEnviron.
func hermeticEnviron() []string {
	skip := make(map[string]bool, len(gitEnvVars))
	for _, v := range gitEnvVars {
		skip[v] = true
	}
	var env []string
	for _, kv := range os.Environ() {
		if k := strings.SplitN(kv, "=", 2)[0]; !skip[k] {
			env = append(env, kv)
		}
	}
	return env
}

// TestMain scrubs the git hook-inherited env vars from THIS PROCESS before any
// test runs. gitTest below rebuilds its own child env from hermeticEnviron(),
// but Isolate (production code) runs git via run.CLIRunner{} with opts.Env left
// nil — which os/exec fills in from the process environment at inherit time —
// so the process itself must be clean for the tests' Isolate calls to be
// hermetic too. See the mandatory amendment in this task's brief.
func TestMain(m *testing.M) {
	for _, v := range gitEnvVars {
		os.Unsetenv(v)
	}
	os.Exit(m.Run())
}

// gitTest runs git with a hermetic config (no user/global gitconfig) and a
// hermetic environment (no inherited hook git-state vars).
func gitTest(t *testing.T, dir string, args ...string) string {
	t.Helper()
	r := run.CLIRunner{}
	full := append([]string{
		"-C", dir,
		"-c", "user.email=test@example.com", "-c", "user.name=test",
		"-c", "commit.gpgsign=false",
	}, args...)
	res, err := r.Run(context.Background(), "git", full, run.Options{
		Env: append(hermeticEnviron(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null"),
	})
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, res.Stderr)
	}
	return res.Stdout
}

// newRepo creates a repo on branch main with one commit and returns its path.
// The tempdir is symlink-resolved up front (macOS t.TempDir() lives under
// /var/folders → /private/var/folders; git reports resolved paths, so the test
// must compare in resolved space).
func newRepo(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	gitTest(t, dir, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, dir, "add", "f.txt")
	gitTest(t, dir, "commit", "-m", "init")
	return dir
}

func TestIsolate_freshWorktree(t *testing.T) {
	repo := newRepo(t)
	out, err := Isolate(context.Background(), run.CLIRunner{}, Params{RepoPath: repo, BeadID: "pg2-x1"})
	if err != nil {
		t.Fatalf("Isolate: %v", err)
	}
	want := filepath.Join(repo, ".worktrees", "pg2-x1")
	if out.Worktree != want || out.Branch != "drain/pg2-x1" || out.Reused != "none" || out.Precommit != "none" {
		t.Errorf("out = %+v", out)
	}
	if _, err := os.Stat(filepath.Join(want, "f.txt")); err != nil {
		t.Errorf("worktree not materialized: %v", err)
	}
}

func TestIsolate_reusesExistingWorktree(t *testing.T) {
	repo := newRepo(t)
	if _, err := Isolate(context.Background(), run.CLIRunner{}, Params{RepoPath: repo, BeadID: "pg2-x2"}); err != nil {
		t.Fatal(err)
	}
	out, err := Isolate(context.Background(), run.CLIRunner{}, Params{RepoPath: repo, BeadID: "pg2-x2"})
	if err != nil {
		t.Fatalf("second Isolate: %v", err)
	}
	if out.Reused != "worktree" {
		t.Errorf("Reused = %q, want worktree", out.Reused)
	}
}

func TestIsolate_reusesParkedBranch(t *testing.T) {
	repo := newRepo(t)
	if _, err := Isolate(context.Background(), run.CLIRunner{}, Params{RepoPath: repo, BeadID: "pg2-x3"}); err != nil {
		t.Fatal(err)
	}
	gitTest(t, repo, "worktree", "remove", filepath.Join(repo, ".worktrees", "pg2-x3"))
	out, err := Isolate(context.Background(), run.CLIRunner{}, Params{RepoPath: repo, BeadID: "pg2-x3"})
	if err != nil {
		t.Fatalf("re-Isolate: %v", err)
	}
	if out.Reused != "branch" {
		t.Errorf("Reused = %q, want branch (parked branch must be reused, not recreated)", out.Reused)
	}
}

func TestIsolate_conflictingCheckoutErrors(t *testing.T) {
	repo := newRepo(t)
	// occupy the worktree path with the WRONG branch
	gitTest(t, repo, "worktree", "add", filepath.Join(repo, ".worktrees", "pg2-x4"), "-b", "other", "main")
	_, err := Isolate(context.Background(), run.CLIRunner{}, Params{RepoPath: repo, BeadID: "pg2-x4"})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
}

func TestIsolate_branchCheckedOutElsewhereErrors(t *testing.T) {
	repo := newRepo(t)
	gitTest(t, repo, "worktree", "add", filepath.Join(repo, "elsewhere"), "-b", "drain/pg2-x5", "main")
	_, err := Isolate(context.Background(), run.CLIRunner{}, Params{RepoPath: repo, BeadID: "pg2-x5"})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
}

func TestIsolate_linksPrecommitConfig(t *testing.T) {
	repo := newRepo(t)
	target := filepath.Join(t.TempDir(), "generated-config.yaml")
	if err := os.WriteFile(target, []byte("repos: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// canonical clones carry a gitignored SYMLINK to the nix-generated config
	src := filepath.Join(repo, ".pre-commit-config.yaml")
	if err := os.Symlink(target, src); err != nil {
		t.Fatal(err)
	}
	out, err := Isolate(context.Background(), run.CLIRunner{}, Params{RepoPath: repo, BeadID: "pg2-x6"})
	if err != nil {
		t.Fatalf("Isolate: %v", err)
	}
	if out.Precommit != "linked" {
		t.Errorf("Precommit = %q, want linked", out.Precommit)
	}
	// The worktree links to the CANONICAL config path (symlink-to-symlink), so a
	// later hook re-install in the canonical clone propagates to the worktree.
	got, err := os.Readlink(filepath.Join(out.Worktree, ".pre-commit-config.yaml"))
	if err != nil || got != src {
		t.Errorf("worktree config link = %q, %v; want %q", got, err, src)
	}
}

func TestIsolate_precommitPresentOnReuse(t *testing.T) {
	repo := newRepo(t)
	target := filepath.Join(t.TempDir(), "generated-config.yaml")
	if err := os.WriteFile(target, []byte("repos: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// canonical clones carry a gitignored SYMLINK to the nix-generated config
	src := filepath.Join(repo, ".pre-commit-config.yaml")
	if err := os.Symlink(target, src); err != nil {
		t.Fatal(err)
	}
	if _, err := Isolate(context.Background(), run.CLIRunner{}, Params{RepoPath: repo, BeadID: "pg2-xb"}); err != nil {
		t.Fatal(err)
	}
	out, err := Isolate(context.Background(), run.CLIRunner{}, Params{RepoPath: repo, BeadID: "pg2-xb"})
	if err != nil {
		t.Fatalf("second Isolate: %v", err)
	}
	if out.Reused != "worktree" {
		t.Errorf("Reused = %q, want worktree", out.Reused)
	}
	if out.Precommit != "present" {
		t.Errorf("Precommit = %q, want present (already-linked config on a reused worktree)", out.Precommit)
	}
}

func TestIsolate_danglingPrecommitLinkIsRepointed(t *testing.T) {
	repo := newRepo(t)
	target := filepath.Join(t.TempDir(), "generated-config.yaml")
	if err := os.WriteFile(target, []byte("repos: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// canonical clones carry a gitignored SYMLINK to the nix-generated config
	src := filepath.Join(repo, ".pre-commit-config.yaml")
	if err := os.Symlink(target, src); err != nil {
		t.Fatal(err)
	}
	out, err := Isolate(context.Background(), run.CLIRunner{}, Params{RepoPath: repo, BeadID: "pg2-xc"})
	if err != nil {
		t.Fatalf("Isolate: %v", err)
	}
	// Break the WORKTREE's link (not the canonical one) by repointing it at a
	// nonexistent target — this is the shape a half-finished/corrupted worktree
	// link takes, and it must be REPAIRED, not mistaken for "present".
	wtCfg := filepath.Join(out.Worktree, ".pre-commit-config.yaml")
	if err := os.Remove(wtCfg); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/nonexistent/target", wtCfg); err != nil {
		t.Fatal(err)
	}
	out2, err := Isolate(context.Background(), run.CLIRunner{}, Params{RepoPath: repo, BeadID: "pg2-xc"})
	if err != nil {
		t.Fatalf("second Isolate: %v", err)
	}
	if out2.Precommit != "linked" {
		t.Errorf("Precommit = %q, want linked (a dangling link must be repointed, never read as present)", out2.Precommit)
	}
	got, err := os.Readlink(wtCfg)
	if err != nil || got != src {
		t.Errorf("worktree config link = %q, %v; want repointed to canonical %q", got, err, src)
	}
}

func TestIsolate_detachedWorktreeAtPathConflicts(t *testing.T) {
	repo := newRepo(t)
	sha := strings.TrimSpace(gitTest(t, repo, "rev-parse", "HEAD"))
	gitTest(t, repo, "worktree", "add", "--detach", filepath.Join(repo, ".worktrees", "pg2-x9"), sha)
	_, err := Isolate(context.Background(), run.CLIRunner{}, Params{RepoPath: repo, BeadID: "pg2-x9"})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict (detached-HEAD occupant, not exit-1 noise)", err)
	}
}

func TestIsolate_staleWorktreeEntryIsPrunedAndRecreated(t *testing.T) {
	repo := newRepo(t)
	if _, err := Isolate(context.Background(), run.CLIRunner{}, Params{RepoPath: repo, BeadID: "pg2-xa"}); err != nil {
		t.Fatal(err)
	}
	// delete the directory WITHOUT `git worktree remove` — a stale registration
	if err := os.RemoveAll(filepath.Join(repo, ".worktrees", "pg2-xa")); err != nil {
		t.Fatal(err)
	}
	out, err := Isolate(context.Background(), run.CLIRunner{}, Params{RepoPath: repo, BeadID: "pg2-xa"})
	if err != nil {
		t.Fatalf("re-Isolate after stale entry: %v", err)
	}
	if out.Reused != "branch" {
		t.Errorf("Reused = %q, want branch (entry pruned, surviving branch reused)", out.Reused)
	}
	if _, err := os.Stat(filepath.Join(out.Worktree, "f.txt")); err != nil {
		t.Errorf("worktree not re-materialized on disk: %v", err)
	}
}

func TestIsolate_primaryBranchFromGitConfig(t *testing.T) {
	repo := newRepo(t)
	gitTest(t, repo, "branch", "trunk")
	gitTest(t, repo, "config", "pgii-integrate-branch.primaryBranch", "trunk")
	// advance main past trunk so basing on the wrong branch is detectable
	if err := os.WriteFile(filepath.Join(repo, "g.txt"), []byte("y\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, repo, "add", "g.txt")
	gitTest(t, repo, "commit", "-m", "main moves on")
	out, err := Isolate(context.Background(), run.CLIRunner{}, Params{RepoPath: repo, BeadID: "pg2-x7"})
	if err != nil {
		t.Fatalf("Isolate: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(out.Worktree, "g.txt")); statErr == nil {
		t.Error("worktree contains main's commit; branch was not based on the configured primary (trunk)")
	}
}

func TestIsolate_notAGitRepoErrors(t *testing.T) {
	if _, err := Isolate(context.Background(), run.CLIRunner{}, Params{RepoPath: t.TempDir(), BeadID: "pg2-x8"}); err == nil {
		t.Fatal("expected error for non-repo path")
	}
}
