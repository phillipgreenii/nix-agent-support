package drain

import (
	"context"
	"errors"
	"fmt"
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
		_ = os.Unsetenv(v)
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

// TestIsolate_corruptedCoreWorktreeDoesNotRedirectRepo guards the confirmed
// real-world mechanism behind pg2-x4e06 / pg2-12795: an unrelated bug (a
// git-fixture test-isolation escape in packages/pg-pr, tracked separately as
// pg2-5ek6b) can leave the CANONICAL clone's own .git/config with
// core.worktree pointing at some OTHER existing worktree's path. Before this
// fix, Isolate derived its repo root from `git rev-parse --show-toplevel`,
// which under that corruption silently reports the OTHER worktree's path
// instead of the repo's own -- exit 0, no error -- so `.worktrees/<bead>`
// got joined onto the wrong directory and a new worktree was nested inside
// an unrelated one instead of created as a sibling of the canonical clone.
func TestIsolate_corruptedCoreWorktreeDoesNotRedirectRepo(t *testing.T) {
	repo := newRepo(t)

	other, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	other = filepath.Join(other, "elsewhere-worktree")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	gitTest(t, repo, "config", "core.worktree", other)

	// Sanity: confirm the corruption actually fools plain rev-parse against
	// this repo, so the rest of the test exercises the real mechanism
	// rather than a hypothetical one that no longer applies to this git
	// version.
	if got := strings.TrimSpace(gitTest(t, repo, "rev-parse", "--show-toplevel")); got != other {
		t.Fatalf("corruption setup didn't take: rev-parse --show-toplevel = %q, want %q", got, other)
	}

	out, err := Isolate(context.Background(), run.CLIRunner{}, Params{RepoPath: repo, BeadID: "pg2-xd"})
	if err != nil {
		t.Fatalf("Isolate: %v", err)
	}
	want := filepath.Join(repo, ".worktrees", "pg2-xd")
	if out.Worktree != want {
		t.Errorf("Worktree = %q, want %q (must anchor to repo, not the corrupted core.worktree target)", out.Worktree, want)
	}
	if strings.HasPrefix(out.Worktree, other) {
		t.Fatalf("worktree nested inside the corrupted core.worktree target %q: got %q", other, out.Worktree)
	}
	if _, statErr := os.Stat(filepath.Join(out.Worktree, "f.txt")); statErr != nil {
		t.Errorf("worktree not materialized at the correct path: %v", statErr)
	}
}

// TestIsolate_concurrentIsolateCallsStayAnchoredToRepo is the concurrency
// stress test the bead's acceptance criteria ask for: many concurrent
// Isolate calls against a --repo whose core.worktree is ALSO corrupted (the
// confirmed mechanism) and which already has other worktrees present (the
// shape of the observed incident -- pg2-4dz88.8.5 was a pre-existing,
// unrelated worktree, not one created by the misfiring call). Every
// resulting worktree path must be a direct child of <repo>/.worktrees/,
// never nested under another bead's worktree or the corrupted target.
//
// The concurrent calls all hit the REUSE path (worktrees pre-created
// sequentially below) rather than creating fresh worktrees concurrently:
// `git worktree add` against the same repo from several goroutines at once
// hits git's own worktree-metadata locking and is flaky independent of
// anything Isolate does (confirmed by hand: concurrent fresh-creates
// intermittently fail with "failed to read .git/worktrees/.../commondir",
// reproducing with or without this bead's fix) -- a separate, pre-existing
// git-level concern, not the silent-misdirection bug this test guards.
func TestIsolate_concurrentIsolateCallsStayAnchoredToRepo(t *testing.T) {
	repo := newRepo(t)

	// A pre-existing, unrelated worktree -- the shape of the observed
	// incident.
	preexisting := filepath.Join(repo, ".worktrees", "preexisting")
	gitTest(t, repo, "worktree", "add", preexisting, "-b", "drain/preexisting", "main")

	const n = 12
	beads := make([]string, n)
	for i := range beads {
		beads[i] = fmt.Sprintf("pg2-conc%d", i)
		if _, err := Isolate(context.Background(), run.CLIRunner{}, Params{RepoPath: repo, BeadID: beads[i]}); err != nil {
			t.Fatalf("seeding Isolate(%s): %v", beads[i], err)
		}
	}

	other, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	other = filepath.Join(other, "elsewhere-worktree")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	gitTest(t, repo, "config", "core.worktree", other)

	type outcome struct {
		bead string
		out  Result
		err  error
	}
	results := make(chan outcome, n)
	for _, bead := range beads {
		go func(bead string) {
			out, err := Isolate(context.Background(), run.CLIRunner{}, Params{RepoPath: repo, BeadID: bead})
			results <- outcome{bead: bead, out: out, err: err}
		}(bead)
	}

	for i := 0; i < n; i++ {
		res := <-results
		if res.err != nil {
			t.Errorf("Isolate(%s): %v", res.bead, res.err)
			continue
		}
		if res.out.Reused != "worktree" {
			t.Errorf("Isolate(%s): Reused = %q, want worktree (pre-seeded)", res.bead, res.out.Reused)
		}
		want := filepath.Join(repo, ".worktrees", res.bead)
		if res.out.Worktree != want {
			t.Errorf("Isolate(%s): Worktree = %q, want %q", res.bead, res.out.Worktree, want)
		}
		if strings.HasPrefix(res.out.Worktree, preexisting) || strings.HasPrefix(res.out.Worktree, other) {
			t.Errorf("Isolate(%s): worktree %q nested inside another worktree's tree", res.bead, res.out.Worktree)
		}
	}
}

func TestIsolate_notAGitRepoErrors(t *testing.T) {
	if _, err := Isolate(context.Background(), run.CLIRunner{}, Params{RepoPath: t.TempDir(), BeadID: "pg2-x8"}); err == nil {
		t.Fatal("expected error for non-repo path")
	}
}
