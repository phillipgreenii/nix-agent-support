package watchdog

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/pr-pool/internal/ccpool"
	"github.com/phillipgreenii/pr-pool/internal/usage"
)

// recGit records reset/clean calls without touching disk.
type recGit struct{ ran [][]string }

func (g *recGit) Run(_ context.Context, dir string, args ...string) error {
	g.ran = append(g.ran, append([]string{dir}, args...))
	return nil
}

func TestSafeToReset_guard(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	// path == repoRoot -> never
	if safeToReset(ctx, repo, repo, repo) {
		t.Error("must refuse to reset repoRoot")
	}
	// outside worktreeDir -> never
	if safeToReset(ctx, "/somewhere/else", repo, filepath.Join(repo, "wt")) {
		t.Error("must refuse a path outside WorktreeDir")
	}
	// non-existent / not-a-worktree -> never (safe no-op)
	if safeToReset(ctx, filepath.Join(repo, "wt", "ghost"), repo, filepath.Join(repo, "wt")) {
		t.Error("must refuse a non-worktree path")
	}
}

func TestTerminal_unclaimsNotesNoHuman(t *testing.T) {
	cc := &fakeCC{}
	bd := &recBD{}
	wd := newWD(&fakeReader{seq: []usage.Snapshot{{}}}, cc, bd, tokBudget(1000))
	wd.Git = &recGit{}
	// session cwd == repoRoot -> reset is a guarded no-op (the v1 reality)
	cc.list = []ccpool.Session{{ExternalID: "s", CWD: "/repo"}}
	wd.terminal(context.Background(), "s", "zr-1")
	if !bd.has("update zr-1 --status=open --assignee=") {
		t.Errorf("must unclaim; calls=%v", bd.calls)
	}
	for _, c := range bd.calls {
		if c == "update zr-1 --add-label human" {
			t.Error("must NOT add human")
		}
	}
}

// fixtureGitEnv is a minimal, explicit environment for the git commands this
// test file uses to BUILD and INSPECT fixtures: PATH plus a fixed test
// identity, and nothing else from the ambient environment.
//
// HOME is pointed at its own fresh t.TempDir() rather than forwarded, unlike
// production gitenv.Environ (which must forward the real HOME to keep
// working against the operator's actual repos): this machine's real HOME
// wires a global git hook that refuses a commit whose author identity looks
// like a placeholder ("t"/"example.com"/...) -- exactly the identity these
// fixtures use. GIT_CONFIG_NOSYSTEM/_GLOBAL/_SYSTEM back the isolation
// belt-and-suspenders. This function is also deliberately the ONLY place in
// this file GIT_-prefixed variables are set, and it never forwards ambient
// ones -- including GIT_DIR/GIT_WORK_TREE the tests below set via t.Setenv
// to simulate the leak -- so fixture setup/inspection cannot itself be
// disturbed by the exact leak execGit/OSGit.Run are being checked against.
// Mirrors ccpool's gitfacet_test.go testGitEnv (pg2-aqpvr).
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

func runFixtureGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = fixtureGitEnv(t)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git -C %s %v: %v\n%s", dir, args, err, out)
	}
	return string(out)
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

// fixtureConfigGet reads a --local git config key, returning "" if unset.
func fixtureConfigGet(t *testing.T, dir, key string) string {
	t.Helper()
	cmd := exec.Command("git", "-C", dir, "config", "--local", "--get", key)
	cmd.Env = fixtureGitEnv(t)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// TestGitToplevel_ignoresLeakedGitDir is a regression pin for pg2-bh09g:
// execGit (which gitToplevel drives) must resolve the directory it is
// explicitly given, not a repository named by an ambient leaked
// GIT_DIR/GIT_WORK_TREE -- exactly what a `git commit` from a linked
// worktree exports into every descendant process (mechanism write-up:
// pg2-67h4y). Verified to FAIL against the pre-fix code (a bare
// exec.CommandContext with no cmd.Env, which inherits os.Environ()
// wholesale): it then reports the leaked repo's own toplevel instead of
// target's.
func TestGitToplevel_ignoresLeakedGitDir(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	target := initFixtureRepo(t, "target-branch")
	leaked := initFixtureRepo(t, "leaked-branch")

	t.Setenv("GIT_DIR", filepath.Join(leaked, ".git"))
	t.Setenv("GIT_WORK_TREE", leaked)

	tl, err := gitToplevel(context.Background(), target)
	if err != nil {
		t.Fatalf("gitToplevel(%s): %v", target, err)
	}
	if tl != target {
		t.Fatalf("gitToplevel(%s) = %q, want %q -- a leaked GIT_DIR/GIT_WORK_TREE silently overrode -C", target, tl, target)
	}
}

// TestOSGitRun_ignoresLeakedGitDir is the acceptance regression test named on
// pg2-bh09g: OSGit.Run is the MUTATING half of this package's git calls
// (terminal's `reset --hard` / `clean -fd`), so under a leaked ambient
// GIT_DIR/GIT_WORK_TREE it must act on the directory it was explicitly
// given, not the leaked repository -- exactly the pg2-12795/pg2-5ek6b
// incident mechanism, where a `git config` write under a leaked GIT_DIR
// landed in the wrong repo's shared config. Verified to FAIL against the
// pre-fix code (a bare exec.CommandContext with no cmd.Env): it then writes
// the marker into the LEAKED repo's config instead of target's.
func TestOSGitRun_ignoresLeakedGitDir(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	target := initFixtureRepo(t, "target-branch")
	leaked := initFixtureRepo(t, "leaked-branch")

	// Simulate the leak vector: GIT_DIR/GIT_WORK_TREE set in the ambient
	// environment pointing at a DIFFERENT repository than the one OSGit.Run
	// is told to act on.
	t.Setenv("GIT_DIR", filepath.Join(leaked, ".git"))
	t.Setenv("GIT_WORK_TREE", leaked)

	g := OSGit{}
	if err := g.Run(context.Background(), target, "config", "--local", "pgbh09g.marker", "target-value"); err != nil {
		t.Fatalf("OSGit.Run config: %v", err)
	}

	if got := fixtureConfigGet(t, target, "pgbh09g.marker"); got != "target-value" {
		t.Fatalf("target repo's config is missing the value OSGit.Run wrote (got %q) -- a leaked GIT_DIR/GIT_WORK_TREE may have redirected the write elsewhere", got)
	}
	if got := fixtureConfigGet(t, leaked, "pgbh09g.marker"); got != "" {
		t.Fatalf("leaked repo's config carries the marker (%q); OSGit.Run wrote into the LEAKED repository instead of the directory it was explicitly given (%s)", got, target)
	}
}
