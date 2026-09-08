package worktree

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/x/gitclient"
)

// recWTM records every CreateWorktree call a fake Opener's client receives.
type recWTM struct {
	calls []struct {
		path, branch string
		opts         gitclient.CreateWorktreeOptions
	}
}

func (m *recWTM) CreateWorktree(_ context.Context, path, branch string, opts gitclient.CreateWorktreeOptions) error {
	m.calls = append(m.calls, struct {
		path, branch string
		opts         gitclient.CreateWorktreeOptions
	}{path, branch, opts})
	return nil
}

func (m *recWTM) RemoveWorktree(context.Context, string, bool) error { return nil }
func (m *recWTM) PruneWorktrees(context.Context) error               { return nil }

// recOpener is a fake Opener: it reports gitclient.ErrNotARepository for any
// dir in missingAt, and otherwise succeeds, returning the shared recWTM so
// CreateWorktree calls are observable.
type recOpener struct {
	calls     []string
	missingAt map[string]bool
	wtm       *recWTM
}

func newRecOpener(missingAt map[string]bool) *recOpener {
	return &recOpener{missingAt: missingAt, wtm: &recWTM{}}
}

func (o *recOpener) Open(_ context.Context, dir string) (gitclient.WorktreeManager, error) {
	o.calls = append(o.calls, dir)
	if o.missingAt[dir] {
		return nil, gitclient.ErrNotARepository
	}
	return o.wtm, nil
}

func TestEnsure_createsFreshPerBeadWorktree(t *testing.T) {
	wtDir := t.TempDir()
	want := filepath.Join(wtDir, "zr-6bq.3")
	o := newRecOpener(map[string]bool{want: true}) // the target path doesn't exist yet

	got, err := Ensure(context.Background(), o.Open, wtDir, "/repo", "zr-6bq.3")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if got != want {
		t.Errorf("path = %q, want %q", got, want)
	}
	if len(o.wtm.calls) != 1 {
		t.Fatalf("expected exactly one CreateWorktree call, got %d: %+v", len(o.wtm.calls), o.wtm.calls)
	}
	c := o.wtm.calls[0]
	if c.path != want || c.branch != "pr-pool/zr-6bq.3" || !c.opts.ResetBranch {
		t.Errorf("CreateWorktree(%q, %q, %+v); want (%q, %q, ResetBranch=true)", c.path, c.branch, c.opts, want, "pr-pool/zr-6bq.3")
	}
	// The create call must anchor at repoRoot, not the worktree path itself.
	if len(o.calls) < 2 || o.calls[1] != "/repo" {
		t.Errorf("expected the second open to anchor at repoRoot; opens=%v", o.calls)
	}
}

func TestEnsure_reusesExistingWorktree(t *testing.T) {
	wtDir := t.TempDir()
	path := filepath.Join(wtDir, "zr-1")
	o := newRecOpener(nil) // nothing missing: path probe succeeds ⇒ reuse

	got, err := Ensure(context.Background(), o.Open, wtDir, "/repo", "zr-1")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if got != path {
		t.Errorf("path = %q, want %q", got, path)
	}
	if len(o.wtm.calls) != 0 {
		t.Errorf("existing worktree must be reused, not re-added; calls=%+v", o.wtm.calls)
	}
	if len(o.calls) != 1 || o.calls[0] != path {
		t.Errorf("reuse path must open only the target path once; opens=%v", o.calls)
	}
}

func TestEnsure_probeErrorOtherThanNotARepositoryPropagates(t *testing.T) {
	wtDir := t.TempDir()
	sentinel := context.Canceled
	open := func(context.Context, string) (gitclient.WorktreeManager, error) {
		return nil, sentinel
	}

	_, err := Ensure(context.Background(), open, wtDir, "/repo", "zr-2")
	if err == nil {
		t.Fatal("expected an error to propagate rather than fall through to create")
	}
}

// TestEnsure_probeMissingPathNotWrappedAsNotARepositoryStillCreates is a
// regression pin for pg2-an65v: the REAL gitclient.New (see
// TestEnsure_createsFreshPerBeadWorktree_realGitClient below) does not
// actually return gitclient.ErrNotARepository when the probed path does not
// exist on disk yet -- it fails earlier, in filepath.EvalSymlinks, with a
// plain wrapped "no such file or directory" error. The prior fakes in this
// file (recOpener) all returned the sentinel directly for a "missing" path,
// which is why they could not catch this: they modeled the INTENDED
// contract, not gitclient's actual one. This test's fake instead reproduces
// the real shape (an fs.ErrNotExist-satisfying error that is NOT
// gitclient.ErrNotARepository) and asserts Ensure still falls through to
// create rather than failing the whole dispatch.
func TestEnsure_probeMissingPathNotWrappedAsNotARepositoryStillCreates(t *testing.T) {
	wtDir := t.TempDir()
	path := filepath.Join(wtDir, "zr-3")
	wtm := &recWTM{}
	open := func(_ context.Context, dir string) (gitclient.WorktreeManager, error) {
		if dir == path {
			// The real gitclient.New's actual failure shape for a nonexistent
			// dir: a plain fs.ErrNotExist-satisfying error, NOT
			// gitclient.ErrNotARepository (confirmed empirically, pg2-an65v).
			return nil, fmt.Errorf("gitclient: %s: %w", dir, &os.PathError{Op: "lstat", Path: dir, Err: os.ErrNotExist})
		}
		return wtm, nil
	}

	got, err := Ensure(context.Background(), open, wtDir, "/repo", "zr-3")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if got != path {
		t.Errorf("path = %q, want %q", got, path)
	}
	if len(wtm.calls) != 1 {
		t.Fatalf("expected exactly one CreateWorktree call, got %d: %+v", len(wtm.calls), wtm.calls)
	}
}

// fixtureGitEnv is a minimal, explicit environment for the git commands this
// test file uses to BUILD fixtures directly (not through gitclient): PATH plus
// a fixed test identity, and nothing else from the ambient environment.
// Mirrors internal/watchdog/terminal_test.go's helper of the same name.
func fixtureGitEnv(t *testing.T) []string {
	t.Helper()
	return []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + t.TempDir(),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
	}
}

func runFixtureGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = fixtureGitEnv(t)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git -C %s %v: %v\n%s", dir, args, err, out)
	}
}

// initFixtureRepo creates a fresh git repo at a temp dir, checked out on
// branch, with one empty commit so HEAD resolves.
func initFixtureRepo(t *testing.T, branch string) string {
	t.Helper()
	dir := t.TempDir()
	dir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	runFixtureGit(t, dir, "init", "-q", "-b", branch)
	runFixtureGit(t, dir, "commit", "-q", "--allow-empty", "-m", "init")
	return dir
}

// TestEnsure_createsFreshPerBeadWorktree_realGitClient is the integration
// regression pin for pg2-an65v: it wires the REAL production Opener (the same
// `func(ctx, dir) (gitclient.WorktreeManager, error) { return gitclient.New(ctx,
// dir, ...) }` shape internal/executor.Deps.gitOpener builds) instead of a fake,
// against a real git repository, dispatching a bead id whose worktree path has
// NEVER existed before -- the ordinary first-dispatch case, and exactly the
// scenario that failed 100% of the time before this fix. Every other test in
// this file uses a fake Opener that models the INTENDED contract
// (gitclient.ErrNotARepository for a missing path) rather than gitclient's
// actual behavior, so none of them could have caught this regression.
func TestEnsure_createsFreshPerBeadWorktree_realGitClient(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	repoRoot := initFixtureRepo(t, "main")
	wtDir := t.TempDir()
	homeDir := t.TempDir() // isolate HOME from this machine's real git config/hooks
	open := func(ctx context.Context, dir string) (gitclient.WorktreeManager, error) {
		return gitclient.New(
			ctx, dir,
			gitclient.WithHome(homeDir),
			gitclient.WithoutInherited("SSH_AUTH_SOCK"),
			// gitclient's own env allowlist drops every GIT_* var by default, but
			// NOT the SYSTEM gitconfig itself (e.g. /etc/gitconfig) -- explicitly
			// disable it too, mirroring x/gitfixture.NewRepo, so a machine-wide
			// hooksPath/template/credential-helper cannot slow down or otherwise
			// affect `git worktree add` here.
			gitclient.WithEnv("GIT_CONFIG_NOSYSTEM", "1"),
		)
	}

	got, err := Ensure(context.Background(), open, wtDir, repoRoot, "zr-an65v")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	want := filepath.Join(wtDir, "zr-an65v")
	if got != want {
		t.Errorf("path = %q, want %q", got, want)
	}
	if info, statErr := os.Stat(filepath.Join(got, ".git")); statErr != nil || info == nil {
		t.Fatalf("expected a real linked worktree at %s: stat .git: %v", got, statErr)
	}

	// Redispatching the SAME bead id must reuse the worktree just created, not
	// error or attempt a second `git worktree add` (idempotency, per Ensure's
	// doc comment) -- this exercises the OTHER probe outcome (the path now
	// exists) through the same real gitclient.New path.
	got2, err := Ensure(context.Background(), open, wtDir, repoRoot, "zr-an65v")
	if err != nil {
		t.Fatalf("Ensure (redispatch): %v", err)
	}
	if got2 != want {
		t.Errorf("redispatch path = %q, want %q", got2, want)
	}
}
