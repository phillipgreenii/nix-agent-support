package watchdog

import (
	"context"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-router/internal/handlers/ccpool"
	"github.com/phillipgreenii/pg-router/internal/handlers/usage"
	"github.com/phillipgreenii/x/gitfixture"
	"github.com/phillipgreenii/x/gittest"
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

// TestTerminal_closesSession: the hard stop MUST close the session (pg2-vwb4c),
// else a same-named dispatch re-attaches to the dead session and re-times-out
// (28x for zr-6hrfd.2), and the close MUST precede the unclaim so the bead is
// not re-dispatched while the session is still findable. purge=false keeps the
// transcript for forensics.
func TestTerminal_closesSession(t *testing.T) {
	cc := &fakeCC{}
	bd := &recBD{}
	wd := newWD(&fakeReader{seq: []usage.Snapshot{{}}}, cc, bd, tokBudget(1000))
	wd.terminal(context.Background(), "s", "zr-1", &BudgetError{})
	if len(cc.closed) != 1 || cc.closed[0] != "s" {
		t.Fatalf("hard stop must close session s exactly once; closed=%v", cc.closed)
	}
}

// TestTerminal_closesWithoutAnOutcomeMark: the hard stop's only session write is
// the close. A settled row is never absorbed by a later dispatch (INV-CCH-30,
// ADR 0094), so the incomplete mark the hard stop used to stamp first (bead
// pg2-tc9c3) is gone (bead pg2-qaeb8).
func TestTerminal_closesWithoutAnOutcomeMark(t *testing.T) {
	cc := &fakeCC{}
	wd := newWD(&fakeReader{seq: []usage.Snapshot{{}}}, cc, &recBD{}, tokBudget(1000))
	wd.terminal(context.Background(), "s", "zr-1", &BudgetError{})
	if want := []string{"close"}; !slices.Equal(cc.ops, want) {
		t.Errorf("hard stop must close the row and write nothing else to it; ops=%v want %v", cc.ops, want)
	}
}

func TestTerminal_unclaimsNotesNoHuman(t *testing.T) {
	cc := &fakeCC{}
	bd := &recBD{}
	wd := newWD(&fakeReader{seq: []usage.Snapshot{{}}}, cc, bd, tokBudget(1000))
	wd.Git = &recGit{}
	// session cwd == repoRoot -> reset is a guarded no-op (the v1 reality)
	cc.list = []ccpool.Session{{ExternalID: "s", CWD: "/repo"}}
	wd.terminal(context.Background(), "s", "zr-1", &BudgetError{})
	if !bd.has("update zr-1 --status=open --assignee=") {
		t.Errorf("must unclaim; calls=%v", bd.calls)
	}
	for _, c := range bd.calls {
		if c == "update zr-1 --add-label human" {
			t.Error("must NOT add human")
		}
	}
}

// initFixtureRepo creates a hermetic fixture repository from x/gittest
// (temp root, fixture HOME, environment rebuilt from an allowlist, so a leaked
// ambient GIT_DIR/GIT_WORK_TREE -- including the ones the tests below set via
// t.Setenv to simulate the leak -- can never redirect fixture setup or
// inspection), checked out on branch with one empty commit so HEAD resolves.
// The repository lives at the returned Repo's Dir (symlink-resolved).
func initFixtureRepo(t *testing.T, branch string) *gitfixture.Repo {
	t.Helper()
	fx := gittest.New(t, gitfixture.RepoOptions{Suite: "watchdog-terminal", InitialBranch: branch})
	if _, err := fx.Commit(context.Background(), "init", nil); err != nil {
		t.Fatal(err)
	}
	return fx
}

// fixtureConfigGet reads a --local git config key from the fixture repository,
// returning "" if unset.
func fixtureConfigGet(t *testing.T, fx *gitfixture.Repo, key string) string {
	t.Helper()
	out, err := fx.Client.Run(context.Background(), "config", "--local", "--get", key)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// TestGitClientToplevel_ignoresLeakedGitDir is a regression pin for
// pg2-bh09g, now exercised through the x/gitclient migration (pg2-ljyaj):
// safeToReset's worktree-root backstop opens a client via openGit and calls
// its Locator.Toplevel, which MUST resolve the directory it is explicitly
// anchored at, not a repository named by an ambient leaked
// GIT_DIR/GIT_WORK_TREE -- exactly what a `git commit` from a linked
// worktree exports into every descendant process (mechanism write-up:
// pg2-67h4y). gitclient's hermetic, ALLOWLIST-built child environment
// (design doc §4.4) drops the entire GIT_* family by default (only PATH,
// HOME, SSH_AUTH_SOCK are carried over), so this passes by construction
// rather than by a hand-rolled scrub list the way the retired execGit/
// gitToplevel helpers needed to.
func TestGitClientToplevel_ignoresLeakedGitDir(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	targetFx := initFixtureRepo(t, "target-branch")
	leakedFx := initFixtureRepo(t, "leaked-branch")
	target, leaked := targetFx.Dir, leakedFx.Dir

	t.Setenv("GIT_DIR", filepath.Join(leaked, ".git"))
	t.Setenv("GIT_WORK_TREE", leaked)

	ctx := context.Background()
	client, err := openGit(ctx, target)
	if err != nil {
		t.Fatalf("openGit(%s): %v", target, err)
	}
	tl, err := client.Toplevel(ctx)
	if err != nil {
		t.Fatalf("Toplevel(%s): %v", target, err)
	}
	if resolved, evalErr := filepath.EvalSymlinks(tl); evalErr == nil {
		tl = resolved
	}
	if tl != target {
		t.Fatalf("Toplevel() = %q, want %q -- a leaked GIT_DIR/GIT_WORK_TREE silently overrode the anchor", tl, target)
	}
}

// TestOSGitRun_ignoresLeakedGitDir is the acceptance regression test named on
// pg2-bh09g: OSGit.Run is the MUTATING half of this package's git calls.
// It no longer backs terminal's `reset --hard` / `clean -fd` (those migrated
// onto x/gitclient's Cleaner role, bead pg2-ljyaj, verified by the bounded-
// probe tests below); OSGit.Run is retained -- and this regression pin with
// it -- because internal/executor still injects it as the shared worktree-
// CREATION git seam (see terminal.go's OSGit doc comment), which is a
// separate, not-yet-landed migration (pg2-mj9n0). Under a leaked ambient
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

	targetFx := initFixtureRepo(t, "target-branch")
	leakedFx := initFixtureRepo(t, "leaked-branch")
	target, leaked := targetFx.Dir, leakedFx.Dir

	// Simulate the leak vector: GIT_DIR/GIT_WORK_TREE set in the ambient
	// environment pointing at a DIFFERENT repository than the one OSGit.Run
	// is told to act on.
	t.Setenv("GIT_DIR", filepath.Join(leaked, ".git"))
	t.Setenv("GIT_WORK_TREE", leaked)

	g := OSGit{}
	if err := g.Run(context.Background(), target, "config", "--local", "pgbh09g.marker", "target-value"); err != nil {
		t.Fatalf("OSGit.Run config: %v", err)
	}

	if got := fixtureConfigGet(t, targetFx, "pgbh09g.marker"); got != "target-value" {
		t.Fatalf("target repo's config is missing the value OSGit.Run wrote (got %q) -- a leaked GIT_DIR/GIT_WORK_TREE may have redirected the write elsewhere", got)
	}
	if got := fixtureConfigGet(t, leakedFx, "pgbh09g.marker"); got != "" {
		t.Fatalf("leaked repo's config carries the marker (%q); OSGit.Run wrote into the LEAKED repository instead of the directory it was explicitly given (%s)", got, target)
	}
}
