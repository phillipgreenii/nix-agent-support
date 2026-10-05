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

// --- pg2-mmgk2: flox-executive husk self-heal --------------------------------

// makeHusk builds the leftover a live flox executive recreates after its
// worktree was removed: path/.flox/log/executive.<pid>.log.<date> only.
func makeHusk(t *testing.T, path string) string {
	t.Helper()
	logDir := filepath.Join(path, ".flox", "log")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(logDir, "executive.12345.log.2026-10-05")
	if err := os.WriteFile(f, []byte("log\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return f
}

func mustExist(t *testing.T, p string) {
	t.Helper()
	if _, err := os.Lstat(p); err != nil {
		t.Errorf("%s must be untouched, got %v", p, err)
	}
}

func TestReapFloxHusk_removesExactHusk(t *testing.T) {
	path := filepath.Join(t.TempDir(), "zr-husk")
	makeHusk(t, path)
	// A second log file and a rotated one are still just regular files.
	if err := os.WriteFile(filepath.Join(path, ".flox", "log", "executive.99.log.2026-10-04"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	reaped, err := reapFloxHusk(path)
	if err != nil || !reaped {
		t.Fatalf("reapFloxHusk = (%v, %v), want (true, nil)", reaped, err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("husk dir must be gone, Lstat err = %v", err)
	}
	// The parent worktree dir itself is never touched.
	mustExist(t, filepath.Dir(path))
}

func TestReapFloxHusk_refusesAnythingElse(t *testing.T) {
	mk := func(t *testing.T) (path, keep string) {
		path = filepath.Join(t.TempDir(), "zr-x")
		makeHusk(t, path)
		return path, ""
	}
	cases := []struct {
		name string
		mut  func(t *testing.T, path string) (sentinel string)
	}{
		{"extra file at top level", func(t *testing.T, p string) string {
			f := filepath.Join(p, "README.md")
			_ = os.WriteFile(f, []byte("x"), 0o644)
			return f
		}},
		{"git file present (worktree-shaped)", func(t *testing.T, p string) string {
			f := filepath.Join(p, ".git")
			_ = os.WriteFile(f, []byte("gitdir: /nowhere\n"), 0o644)
			return f
		}},
		{"extra entry under .flox", func(t *testing.T, p string) string {
			f := filepath.Join(p, ".flox", "env.json")
			_ = os.WriteFile(f, []byte("{}"), 0o644)
			return f
		}},
		{"nested dir under log", func(t *testing.T, p string) string {
			d := filepath.Join(p, ".flox", "log", "sub")
			_ = os.MkdirAll(d, 0o755)
			return d
		}},
		{"symlink inside log", func(t *testing.T, p string) string {
			tgt := filepath.Join(filepath.Dir(p), "precious")
			_ = os.WriteFile(tgt, []byte("keep"), 0o644)
			l := filepath.Join(p, ".flox", "log", "link")
			if err := os.Symlink(tgt, l); err != nil {
				t.Fatal(err)
			}
			return tgt
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path, _ := mk(t)
			sentinel := c.mut(t, path)
			reaped, err := reapFloxHusk(path)
			if reaped || err != nil {
				t.Fatalf("reapFloxHusk = (%v, %v), want (false, nil)", reaped, err)
			}
			mustExist(t, sentinel)
			mustExist(t, filepath.Join(path, ".flox", "log", "executive.12345.log.2026-10-05"))
		})
	}
}

func TestReapFloxHusk_refusesSymlinks(t *testing.T) {
	t.Run("path itself is a symlink to a husk-shaped dir", func(t *testing.T) {
		base := t.TempDir()
		real := filepath.Join(base, "real")
		makeHusk(t, real)
		link := filepath.Join(base, "link")
		if err := os.Symlink(real, link); err != nil {
			t.Fatal(err)
		}
		if reaped, err := reapFloxHusk(link); reaped || err != nil {
			t.Fatalf("= (%v, %v), want (false, nil)", reaped, err)
		}
		mustExist(t, link)
		mustExist(t, filepath.Join(real, ".flox", "log", "executive.12345.log.2026-10-05"))
	})
	t.Run(".flox is a symlink", func(t *testing.T) {
		base := t.TempDir()
		elsewhere := filepath.Join(base, "elsewhere")
		makeHusk(t, elsewhere) // elsewhere/.flox/log/...
		path := filepath.Join(base, "zr-y")
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(elsewhere, ".flox"), filepath.Join(path, ".flox")); err != nil {
			t.Fatal(err)
		}
		if reaped, err := reapFloxHusk(path); reaped || err != nil {
			t.Fatalf("= (%v, %v), want (false, nil)", reaped, err)
		}
		mustExist(t, filepath.Join(elsewhere, ".flox", "log", "executive.12345.log.2026-10-05"))
	})
	t.Run("log is a symlink", func(t *testing.T) {
		base := t.TempDir()
		elsewhere := filepath.Join(base, "logs")
		if err := os.MkdirAll(elsewhere, 0o755); err != nil {
			t.Fatal(err)
		}
		keep := filepath.Join(elsewhere, "executive.1.log")
		_ = os.WriteFile(keep, nil, 0o644)
		path := filepath.Join(base, "zr-z")
		if err := os.MkdirAll(filepath.Join(path, ".flox"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(elsewhere, filepath.Join(path, ".flox", "log")); err != nil {
			t.Fatal(err)
		}
		if reaped, err := reapFloxHusk(path); reaped || err != nil {
			t.Fatalf("= (%v, %v), want (false, nil)", reaped, err)
		}
		mustExist(t, keep)
	})
}

func TestReapFloxHusk_missingAndEmptyUntouched(t *testing.T) {
	base := t.TempDir()
	if reaped, err := reapFloxHusk(filepath.Join(base, "absent")); reaped || err != nil {
		t.Errorf("missing path = (%v, %v), want (false, nil)", reaped, err)
	}
	empty := filepath.Join(base, "empty")
	if err := os.Mkdir(empty, 0o755); err != nil {
		t.Fatal(err)
	}
	if reaped, err := reapFloxHusk(empty); reaped || err != nil {
		t.Errorf("empty dir = (%v, %v), want (false, nil)", reaped, err)
	}
	mustExist(t, empty) // git worktree add accepts an empty dir; leave it
}

// Ensure with a fake opener: a husk at the (non-repo) path is cleared before
// the create, and the create is then attempted.
func TestEnsure_clearsFloxHuskBeforeCreate(t *testing.T) {
	wtDir := t.TempDir()
	path := filepath.Join(wtDir, "zr-xvbqa.2")
	makeHusk(t, path)
	o := newRecOpener(map[string]bool{path: true})

	if _, err := Ensure(context.Background(), o.Open, wtDir, "/repo", "zr-xvbqa.2"); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("husk must be removed before create (fake create does not recreate it); err=%v", err)
	}
	if len(o.wtm.calls) != 1 {
		t.Errorf("expected one CreateWorktree call, got %d", len(o.wtm.calls))
	}
}

// Ensure with a fake opener: a dir with ANY other content is not touched.
func TestEnsure_leavesNonHuskDirAlone(t *testing.T) {
	wtDir := t.TempDir()
	path := filepath.Join(wtDir, "zr-keep")
	makeHusk(t, path)
	precious := filepath.Join(path, "work.txt")
	if err := os.WriteFile(precious, []byte("uncommitted work"), 0o644); err != nil {
		t.Fatal(err)
	}
	o := newRecOpener(map[string]bool{path: true})

	_, _ = Ensure(context.Background(), o.Open, wtDir, "/repo", "zr-keep")
	mustExist(t, precious)
	mustExist(t, filepath.Join(path, ".flox", "log", "executive.12345.log.2026-10-05"))
}

func realOpener(t *testing.T) Opener {
	t.Helper()
	homeDir := t.TempDir()
	return func(ctx context.Context, dir string) (gitclient.WorktreeManager, error) {
		return gitclient.New(ctx, dir,
			gitclient.WithHome(homeDir),
			gitclient.WithoutInherited("SSH_AUTH_SOCK"),
			gitclient.WithEnv("GIT_CONFIG_NOSYSTEM", "1"))
	}
}

// Real git: reproduces the incident. Without the fix, `git worktree add` fails
// "already exists" on the husk; with it, Ensure yields a real worktree.
func TestEnsure_floxHusk_realGitClient(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	repoRoot := initFixtureRepo(t, "main")
	wtDir := t.TempDir()
	path := filepath.Join(wtDir, "zr-xvbqa.2")
	makeHusk(t, path)

	got, err := Ensure(context.Background(), realOpener(t), wtDir, repoRoot, "zr-xvbqa.2")
	if err != nil {
		t.Fatalf("Ensure over a flox husk: %v", err)
	}
	if got != path {
		t.Errorf("path = %q, want %q", got, path)
	}
	if _, err := os.Stat(filepath.Join(got, ".git")); err != nil {
		t.Fatalf("expected a real linked worktree at %s: %v", got, err)
	}
}

// Real git: a dir holding anything besides the husk shape keeps failing exactly
// as before and is left intact.
func TestEnsure_nonHusk_realGitClient_stillFailsAndKeepsContent(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	repoRoot := initFixtureRepo(t, "main")
	wtDir := t.TempDir()
	path := filepath.Join(wtDir, "zr-other")
	logFile := makeHusk(t, path)
	precious := filepath.Join(path, "notes.txt")
	if err := os.WriteFile(precious, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := Ensure(context.Background(), realOpener(t), wtDir, repoRoot, "zr-other")
	if err == nil {
		t.Fatal("Ensure must still fail on a non-husk leftover dir")
	}
	mustExist(t, precious)
	mustExist(t, logFile)
}

// Real git: a registered live worktree that happens to contain .flox/log files
// is reused and never reaped (the probe sees a repo, and .git is present).
func TestEnsure_registeredWorktreeWithFloxLogsIsReusedNotReaped(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	repoRoot := initFixtureRepo(t, "main")
	wtDir := t.TempDir()
	open := realOpener(t)
	path, err := Ensure(context.Background(), open, wtDir, repoRoot, "zr-live")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	logFile := makeHusk(t, path)

	got, err := Ensure(context.Background(), open, wtDir, repoRoot, "zr-live")
	if err != nil || got != path {
		t.Fatalf("Ensure (reuse) = (%q, %v)", got, err)
	}
	mustExist(t, logFile)
	mustExist(t, filepath.Join(path, ".git"))
}
