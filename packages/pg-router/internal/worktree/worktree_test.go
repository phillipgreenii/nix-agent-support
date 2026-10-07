package worktree

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/x/gitclient"
	"github.com/phillipgreenii/x/gitfixture"
	"github.com/phillipgreenii/x/gittest"
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
	if c.path != want || c.branch != "pg-router/zr-6bq.3" || !c.opts.ResetBranch {
		t.Errorf("CreateWorktree(%q, %q, %+v); want (%q, %q, ResetBranch=true)", c.path, c.branch, c.opts, want, "pg-router/zr-6bq.3")
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

// failingCreateWTM is a gitclient.WorktreeManager whose CreateWorktree always
// fails with a configured error -- for exercising Ensure's isDiskFull
// classification (pg2-8vn8t) without depending on recWTM's own
// call-recording, which always returns nil.
type failingCreateWTM struct{ err error }

func (f failingCreateWTM) CreateWorktree(context.Context, string, string, gitclient.CreateWorktreeOptions) error {
	return f.err
}
func (failingCreateWTM) RemoveWorktree(context.Context, string, bool) error { return nil }
func (failingCreateWTM) PruneWorktrees(context.Context) error               { return nil }

// TestEnsure_createWorktreeDiskFullWrapsErrLowDisk is the regression pin for
// pg2-8vn8t: a `git worktree add` failure whose stderr names the OS's
// out-of-space wording must be classifiable via errors.Is(err, ErrLowDisk),
// not just surfaced as an opaque exit-128 *gitclient.GitError -- exactly the
// 2026-09-23 incident's 'git worktree add ... exit 128: ... error: unable to
// write/create file' failures, which pg-router could not distinguish from any
// other exit-128 git failure before this fix.
func TestEnsure_createWorktreeDiskFullWrapsErrLowDisk(t *testing.T) {
	wtDir := t.TempDir()
	path := filepath.Join(wtDir, "zr-lowdisk")
	gitErr := &gitclient.GitError{
		Args:     []string{"worktree", "add", "-B", "pg-router/zr-lowdisk", path},
		ExitCode: 128,
		Stderr:   "fatal: Unable to create '/repo/.git/worktrees/zr-lowdisk/index.lock': No space left on device",
	}
	open := func(_ context.Context, dir string) (gitclient.WorktreeManager, error) {
		if dir == path {
			return nil, gitclient.ErrNotARepository
		}
		return failingCreateWTM{err: gitErr}, nil
	}

	_, err := Ensure(context.Background(), open, wtDir, "/repo", "zr-lowdisk")
	if err == nil {
		t.Fatal("expected CreateWorktree's failure to propagate")
	}
	if !errors.Is(err, ErrLowDisk) {
		t.Errorf("Ensure error = %v, want errors.Is(err, ErrLowDisk)", err)
	}
	var ge *gitclient.GitError
	if !errors.As(err, &ge) {
		t.Errorf("Ensure error must still carry the original *gitclient.GitError; got %v", err)
	}
}

// TestEnsure_createWorktreeOtherFailureNotLowDisk proves isDiskFull does not
// false-positive on an ordinary git failure sharing the same exit code 128
// (e.g. a stale lock, a bad ref) -- only a stderr naming a known disk-full
// marker is classified ErrLowDisk.
func TestEnsure_createWorktreeOtherFailureNotLowDisk(t *testing.T) {
	wtDir := t.TempDir()
	path := filepath.Join(wtDir, "zr-otherfail")
	gitErr := &gitclient.GitError{ExitCode: 128, Stderr: "fatal: 'pg-router/zr-otherfail' already exists"}
	open := func(_ context.Context, dir string) (gitclient.WorktreeManager, error) {
		if dir == path {
			return nil, gitclient.ErrNotARepository
		}
		return failingCreateWTM{err: gitErr}, nil
	}

	_, err := Ensure(context.Background(), open, wtDir, "/repo", "zr-otherfail")
	if err == nil {
		t.Fatal("expected CreateWorktree's failure to propagate")
	}
	if errors.Is(err, ErrLowDisk) {
		t.Errorf("an unrelated exit-128 git failure must not classify as ErrLowDisk; err=%v", err)
	}
}

// TestIsDiskFull_caseInsensitiveAndOtherMarker exercises isDiskFull directly:
// the "no space left on device" match is case-insensitive (git's own
// locale/wrapper could plausibly vary casing) and the EDQUOT sibling marker
// ("disk quota exceeded") is recognized too.
func TestIsDiskFull_caseInsensitiveAndOtherMarker(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"lowercase enospc", &gitclient.GitError{ExitCode: 128, Stderr: "fatal: unable to write file: no space left on device"}, true},
		{"mixed case enospc", &gitclient.GitError{ExitCode: 128, Stderr: "Fatal: No Space Left On Device"}, true},
		{"edquot", &gitclient.GitError{ExitCode: 128, Stderr: "fatal: cannot write: Disk quota exceeded"}, true},
		{"unrelated", &gitclient.GitError{ExitCode: 128, Stderr: "fatal: not a valid ref"}, false},
		{"not a GitError at all", errors.New("boom"), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isDiskFull(c.err); got != c.want {
				t.Errorf("isDiskFull(%v) = %v, want %v", c.err, got, c.want)
			}
		})
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

// newCommittedRepo returns the root of a hermetic fixture repository
// (x/gittest: allowlisted child env, fixture HOME, GIT_CEILING_DIRECTORIES, no
// ambient GIT_DIR/GIT_WORK_TREE), checked out on main with one empty commit so
// HEAD resolves.
func newCommittedRepo(t *testing.T) string {
	t.Helper()
	repo := gittest.New(t, gitfixture.RepoOptions{InitialBranch: "main"})
	if _, err := repo.Commit(context.Background(), "init", nil); err != nil {
		t.Fatalf("fixture initial commit: %v", err)
	}
	return repo.Dir
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

	repoRoot := newCommittedRepo(t)
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
