package hygiene

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router/internal/handlers/dtest"

	"github.com/phillipgreenii/x/gitclient"
	"github.com/phillipgreenii/x/gitfixture"

	"github.com/phillipgreenii/pg-router/internal/handlers/config"
	"github.com/phillipgreenii/pg-router/internal/handlers/eventlog"
	"github.com/phillipgreenii/pg-router/internal/handlers/executor"
	"github.com/phillipgreenii/pg-router/internal/handlers/roles"
	"github.com/phillipgreenii/pg-router/internal/handlers/sharedrepo"
	"github.com/phillipgreenii/pg-router/internal/handlers/worktree"
)

// Real-git harness for the teardown and reconcile tests: a
// fixture repository from x/gittest (hermetic by construction: temp root, fixture
// HOME, environment rebuilt from an allowlist), real linked worktrees under a temp
// WorktreeDir, and the real gitclient opener.
//
// Bead pg2-vvra0: this file once built its repository by hand with `git init`
// and an enumerated scrub of inherited GIT_* variables. Under a git hook (the
// commit's own pre-commit run) GIT_DIR points at the OUTER repository, and an
// unscrubbed `git init` wrote the fixture's temp path into that repository's
// .git/config as core.worktree; once the temp dir was deleted every git command in
// the clone failed with "fatal: Invalid path". Every git child here now goes
// through gitclient's allowlist environment instead.

type sweepHarness struct {
	t        *testing.T
	repo     string
	wtDir    string
	lockDir  string
	cc       *dtest.OrphanCC
	deps     executor.Deps
	env      orphanEnv
	logPath  string
	role     roles.Role
	opener   worktree.Opener
	homeDir  string
	fixture  string // fixture root: repo, home and hooks live under it
	nowFixed time.Time
}

// The tests share ONE real repository for the whole package: building a
// fixture (git init, its configuration and the first commit) costs about half a
// second under load, and most tests only add linked worktrees and pg-router/*
// branches, which sharedSweepRepo's reset removes between tests. Tests run
// sequentially, so no two tests ever see the repository at once. A test that commits to the main checkout or must
// build its own fixture uses newFreshSweepHarness.
var sharedSweep sharedrepo.Shared

func sharedSweepRepo(t *testing.T) *gitfixture.Repo {
	t.Helper()
	repo, err := sharedSweep.Get(context.Background(), "ccpool-handler-worktreesweep", func(ctx context.Context, r *gitfixture.Repo) error {
		_, err := r.Commit(ctx, "init", nil)
		return err
	})
	if err != nil {
		t.Fatalf("shared sweep fixture: %v", err)
	}
	if err := sharedrepo.Reset(context.Background(), repo); err != nil {
		t.Fatalf("shared sweep fixture reset: %v", err)
	}
	return repo
}

// removeSharedSweepFixture deletes the shared repository; TestMain calls it.
func removeSharedSweepFixture() { sharedSweep.Remove() }

func newSweepHarness(t *testing.T) *sweepHarness {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	fx := sharedSweepRepo(t)
	h := &sweepHarness{repo: fx.Dir, fixture: filepath.Dir(fx.Dir)}
	h.homeDir = filepath.Join(h.fixture, "home") // gitfixture.NewRepo layout: <root>/{repo,home,hooks}
	h.bind(t)
	return h
}

// forRow returns a harness for one row (subtest) of a table over the SAME
// repository as h; see newSweepHarness. Everything a row mutates outside the
// repository (worktree directory, lock directory, event log, session fake) is
// fresh, and the repository is reset first.
func (h *sweepHarness) forRow(t *testing.T) *sweepHarness {
	t.Helper()
	sharedSweepRepo(t)
	row := &sweepHarness{repo: h.repo, homeDir: h.homeDir, fixture: h.fixture}
	row.bind(t)
	return row
}

// bind gives the harness its per-test directories, event log and fakes, over
// the repository and fixture HOME already set on it.
func (h *sweepHarness) bind(t *testing.T) {
	t.Helper()
	h.t = t
	h.lockDir = t.TempDir()
	wtDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h.wtDir = wtDir
	h.opener = func(ctx context.Context, dir string) (gitclient.WorktreeManager, error) {
		return gitclient.New(
			ctx, dir,
			gitclient.WithHome(h.homeDir),
			gitclient.WithoutInherited("SSH_AUTH_SOCK"),
			gitclient.WithEnv("GIT_CONFIG_NOSYSTEM", "1"),
		)
	}

	h.cc = &dtest.OrphanCC{}
	h.logPath = filepath.Join(t.TempDir(), "events.jsonl")
	lw, err := eventlog.New(h.logPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lw.Close() })
	cfg := config.Default()
	cfg.RepoRoot = h.repo
	cfg.WorktreeDir = wtDir
	cfg.SessionPrefix = "pg-router-"
	h.nowFixed = time.Now()
	h.deps = executor.Deps{CC: h.cc, BD: &dtest.OrphanBD{}, Cfg: cfg, Log: lw, Now: func() time.Time { return h.nowFixed }}
	h.env = orphanEnv{open: h.opener, lockDir: h.lockDir}
	h.role = orphanRole(roles.CloseOnly, 0)
}

// client returns a gitclient anchored at dir (the fixture repo or one of its
// linked worktrees) with the fixture's HOME and an allowlisted environment.
func (h *sweepHarness) client(dir string) *gitclient.Client {
	h.t.Helper()
	c, err := gitclient.New(
		context.Background(), dir,
		gitclient.WithHome(h.homeDir),
		gitclient.WithoutInherited("SSH_AUTH_SOCK"),
		gitclient.WithEnv("GIT_CONFIG_NOSYSTEM", "1"),
	)
	if err != nil {
		h.t.Fatalf("gitclient.New(%s): %v", dir, err)
	}
	return c
}

func (h *sweepHarness) git(dir string, args ...string) string {
	h.t.Helper()
	out, err := h.client(dir).Run(context.Background(), args...)
	if err != nil {
		h.t.Fatalf("git -C %s %v: %v\n%s", dir, args, err, out)
	}
	return string(out)
}

// leak creates the per-bead worktree exactly as dispatch does (worktree.Ensure:
// pg-router/<bead> off HEAD) and ages it.
func (h *sweepHarness) leak(bead string) string {
	h.t.Helper()
	path, err := worktree.Ensure(context.Background(), h.opener, h.wtDir, h.repo, bead)
	if err != nil {
		h.t.Fatalf("worktree.Ensure(%s): %v", bead, err)
	}
	old := h.nowFixed.Add(-time.Hour)
	if err := os.Chtimes(filepath.Join(path, ".git"), old, old); err != nil {
		h.t.Fatal(err)
	}
	return path
}

func (h *sweepHarness) exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func (h *sweepHarness) branchExists(bead string) bool {
	_, err := h.client(h.repo).Run(context.Background(), "rev-parse", "--verify", "--quiet", "refs/heads/pg-router/"+bead)
	return err == nil
}

// addWorktree adds a linked worktree <wtDir>/<bead> on branch pg-router/<bead>
// (clean until a caller adds files).
func (h *sweepHarness) addWorktree(bead string) string {
	h.t.Helper()
	path := filepath.Join(h.wtDir, bead)
	if out, err := h.client(h.repo).Run(h.t.Context(), "worktree", "add", "-b", "pg-router/"+bead, path); err != nil {
		h.t.Fatalf("fixture worktree: %v\n%s", err, out)
	}
	return path
}
