package internal

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/x/gitfixture"
	"github.com/phillipgreenii/x/gittest"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-scm-git/internal/gitenv"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// Every repository in this file comes from x/gittest: hermetic by
// construction (fixture root under t.TempDir(), fixture HOME, an allowlisted
// child environment that never inherits GIT_DIR/GIT_WORK_TREE, discovery
// ceiling at the fixture root, hooks redirected to an empty directory).

// sandboxAmbientHome points THIS PROCESS's HOME (and XDG_CONFIG_HOME, which
// git also reads global config from) at the fixture's own empty HOME, and
// drops the system config, via t.Setenv (auto-restored at test cleanup). The
// provider under test
// in this file (New(NewExecRunner())) never receives the fixture's
// environment: its Runner (gitexec.go's execRunner) builds every child git
// process through gitenv.Command -> gitenv.Environ(), which reads this
// process's ambient os.Environ(). Without this, the provider under test would
// inherit the real developer/CI ~/.gitconfig (review finding 35).
func sandboxAmbientHome(t *testing.T, repo *gitfixture.Repo) {
	t.Helper()
	home := filepath.Join(repo.Root(), "home")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
}

// mustGit runs `git <args...>` in repo through its hermetic client and fails
// the test on error.
func mustGit(t *testing.T, repo *gitfixture.Repo, args ...string) string {
	t.Helper()
	out, err := repo.Client.Run(context.Background(), args...)
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out))
}

// newRealGitFixture creates a throwaway repo with one commit on branch
// "main", with this process's ambient HOME sandboxed for the provider under
// test. repo.Dir is absolute and symlink-resolved (matching how git itself
// reports paths on macOS, where a t.TempDir() lives under a /var symlink to
// /private/var).
func newRealGitFixture(t *testing.T) *gitfixture.Repo {
	t.Helper()
	repo := gittest.New(t, gitfixture.RepoOptions{InitialCommit: true})
	sandboxAmbientHome(t, repo)
	return repo
}

// TestProvider_RealGit_AmbientHomeIsSandboxed is the regression test for
// review finding 35: the provider under test reads THIS PROCESS's ambient
// environment, not the fixture's own, so fixture setup must re-sandbox the
// ambient HOME. It poisons the real HOME with a global git config value no
// fixture ever sets, builds a fixture, and then runs a git command through
// the EXACT seam the provider's Runner uses (gitenv.Command) to assert the
// poisoned value is unreachable.
func TestProvider_RealGit_AmbientHomeIsSandboxed(t *testing.T) {
	poisonedHome := t.TempDir()
	if err := os.WriteFile(filepath.Join(poisonedHome, ".gitconfig"), []byte("[user]\n\temail = poisoned@example.com\n"), 0o644); err != nil {
		t.Fatalf("write poisoned gitconfig: %v", err)
	}
	t.Setenv("HOME", poisonedHome)

	repo := newRealGitFixture(t)

	if out, err := gitenv.Command(context.Background(), repo.Dir, "config", "--global", "--get", "user.email").Output(); err == nil {
		t.Fatalf("git config --global --get user.email = %q, want an error: the ambient HOME must be the fixture's empty HOME, never the poisoned real HOME's ~/.gitconfig", strings.TrimSpace(string(out)))
	}
}

// TestProvider_RealGit_WorktreeAddListRemoveAndBranchDetect is the
// packet's required test verifying this Provider against a REAL git
// checkout, not just the fakeRunner-backed tests above — the packet's own
// AC: "backed by real local git state (verified against a real git
// checkout in tests, not just mocks, for at least one method)". It
// exercises all four scm.Provider methods, not just one.
func TestProvider_RealGit_WorktreeAddListRemoveAndBranchDetect(t *testing.T) {
	fx := newRealGitFixture(t)
	repo := fx.Dir
	mustGit(t, fx, "branch", "feature")

	// WorktreeAdd/WorktreeRemove/WorktreeList carry no repo/cwd wire
	// argument of their own (interfaces.md's scm op catalog) — they resolve "the current
	// repository" from this process's own working directory, exactly as a
	// real pg-connector-scm-git process would (its cwd is whatever the
	// caller invoked it from). t.Chdir scopes that to this test and
	// restores it afterward.
	t.Chdir(repo)
	p := New(NewExecRunner())
	ctx := context.Background()

	added, err := p.WorktreeAdd(ctx, "feature")
	if err != nil {
		t.Fatalf("WorktreeAdd: %v", err)
	}
	// This backend nests every worktree it creates under its own dedicated
	// namespace inside `.worktrees/`, not bare `.worktrees/<name>` — see
	// scmGitWorktreeNamespace's own doc comment [bug: pg2-jn22x, review
	// finding 22].
	wantPath := filepath.Join(repo, ".worktrees", scmGitWorktreeNamespace, "feature")
	if added.Path != wantPath || added.Branch != "feature" || added.Ref != "feature" {
		t.Fatalf("WorktreeAdd result = %+v, want Path=%s Branch=feature Ref=feature", added, wantPath)
	}

	branchInfo, err := p.BranchDetect(ctx, added.Path)
	if err != nil {
		t.Fatalf("BranchDetect: %v", err)
	}
	if branchInfo.Branch != "feature" || branchInfo.Repo != filepath.Base(repo) {
		t.Fatalf("BranchDetect result = %+v, want Branch=feature Repo=%s", branchInfo, filepath.Base(repo))
	}

	list, err := p.WorktreeList(ctx)
	if err != nil {
		t.Fatalf("WorktreeList: %v", err)
	}
	found := false
	for _, w := range list {
		if w.Path == added.Path && w.Branch == "feature" {
			found = true
		}
	}
	if !found {
		t.Fatalf("WorktreeList = %+v, want an entry for %s on branch feature", list, added.Path)
	}

	if err := p.WorktreeRemove(ctx, added.Path); err != nil {
		t.Fatalf("WorktreeRemove: %v", err)
	}
	if _, err := os.Stat(added.Path); !os.IsNotExist(err) {
		t.Fatalf("worktree path %s still exists after WorktreeRemove", added.Path)
	}

	// A second removal of the now-gone path is a well-formed not_found
	// answer against a REAL repo, not just the fakeRunner's canned
	// response (INV-ERR-2).
	if err := p.WorktreeRemove(ctx, added.Path); err == nil {
		t.Fatal("WorktreeRemove of an already-removed path = nil error, want ErrNotFound")
	}
}

// newRealBareGitFixture creates a throwaway BARE repo with one commit on
// branch "main", returning its absolute, symlink-resolved path. A bare repo
// cannot receive a commit directly, so this goes via a throwaway non-bare
// seed repo that pushes into it; only the bare repo is handed to the test.
func newRealBareGitFixture(t *testing.T) string {
	t.Helper()
	seed := gittest.New(t, gitfixture.RepoOptions{Suite: "bare-seed", InitialCommit: true})
	bare, err := seed.AddBareRemote(context.Background(), "origin")
	if err != nil {
		t.Fatalf("AddBareRemote: %v", err)
	}
	mustGit(t, seed, "push", "origin", "HEAD:refs/heads/main")
	sandboxAmbientHome(t, bare)
	return bare.Dir
}

// TestProvider_RealGit_BareRepo_WorktreeAddListBranchDetectRemove is the
// real-git regression test for repoRootFor's bare-repository fix
// [bug: pg2-jn22x, review 2026-09-05-pg-connector-deep-review.md finding
// 22/34]. Before the fix, every one of these ops resolved "root" as the
// bare repo's own PARENT directory (filepath.Dir of a bare repo's own
// --git-common-dir, which for a bare repo IS the repo itself already)
// instead of the bare repo itself.
func TestProvider_RealGit_BareRepo_WorktreeAddListBranchDetectRemove(t *testing.T) {
	bare := newRealBareGitFixture(t)

	t.Chdir(bare)
	p := New(NewExecRunner())
	ctx := context.Background()

	added, err := p.WorktreeAdd(ctx, "main")
	if err != nil {
		t.Fatalf("WorktreeAdd: %v", err)
	}
	wantPath := filepath.Join(bare, ".worktrees", scmGitWorktreeNamespace, "main")
	if added.Path != wantPath {
		t.Fatalf("WorktreeAdd result = %+v, want Path=%s (rooted at the bare repo itself, not its parent)", added, wantPath)
	}

	branchInfo, err := p.BranchDetect(ctx, added.Path)
	if err != nil {
		t.Fatalf("BranchDetect: %v", err)
	}
	if branchInfo.Repo != filepath.Base(bare) {
		t.Fatalf("BranchDetect result = %+v, want Repo=%s (the bare repo's own directory name)", branchInfo, filepath.Base(bare))
	}

	list, err := p.WorktreeList(ctx)
	if err != nil {
		t.Fatalf("WorktreeList: %v", err)
	}
	foundBareEntry, foundLinked := false, false
	for _, w := range list {
		if w.Path == bare {
			foundBareEntry = true
		}
		if w.Path == added.Path {
			foundLinked = true
		}
	}
	if !foundBareEntry || !foundLinked {
		t.Fatalf("WorktreeList = %+v, want both the bare main entry (%s) and the linked worktree (%s)", list, bare, added.Path)
	}

	if err := p.WorktreeRemove(ctx, added.Path); err != nil {
		t.Fatalf("WorktreeRemove: %v", err)
	}
}

// newRealSeparateGitDirFixture builds a repo at <root>/work whose git
// directory is relocated to <root>/actual-git-dir via `--separate-git-dir`
// (gitfixture's RepoOptions.SeparateGitDir), with one commit on branch
// "main". Returns the fixture repo; repo.Dir is the working-tree root.
func newRealSeparateGitDirFixture(t *testing.T) *gitfixture.Repo {
	t.Helper()
	repo := gittest.New(t, gitfixture.RepoOptions{
		Name:           "work",
		SeparateGitDir: "actual-git-dir",
		InitialCommit:  true,
	})
	sandboxAmbientHome(t, repo)
	return repo
}

// TestProvider_RealGit_SeparateGitDir_MainWorktree_RootIsWorkingTree is
// the real-git regression test for repoRootFor's `--separate-git-dir`
// fix [bug: pg2-jn22x, review finding 22/34]. Before the fix, root was
// computed as filepath.Dir of the RELOCATED git directory — an unrelated
// path with no connection to the working tree at all — instead of the
// working tree's own root. Calling from the newly added LINKED worktree's
// own vantage point is verified to fail loud rather than silently
// resolving to some other, wrong directory: real git 2.54 genuinely has
// no command that recovers the main worktree's real location from there
// (verified empirically during this bead's investigation — even `git
// worktree list --porcelain`'s own main-worktree entry misreports the
// relocated git directory's own path as if it were the worktree path in
// this exact configuration).
func TestProvider_RealGit_SeparateGitDir_MainWorktree_RootIsWorkingTree(t *testing.T) {
	fx := newRealSeparateGitDirFixture(t)
	work := fx.Dir
	if st, err := os.Stat(filepath.Join(work, ".git")); err != nil || !st.Mode().IsRegular() {
		t.Fatalf("%s/.git must be a gitdir: file for the --separate-git-dir scenario: %v", work, err)
	}
	mustGit(t, fx, "branch", "feature")

	t.Chdir(work)
	p := New(NewExecRunner())
	ctx := context.Background()

	added, err := p.WorktreeAdd(ctx, "feature")
	if err != nil {
		t.Fatalf("WorktreeAdd: %v", err)
	}
	wantPath := filepath.Join(work, ".worktrees", scmGitWorktreeNamespace, "feature")
	if added.Path != wantPath {
		t.Fatalf("WorktreeAdd result = %+v, want Path=%s (rooted at the working tree, not the relocated git directory)", added, wantPath)
	}

	branchInfo, err := p.BranchDetect(ctx, work)
	if err != nil {
		t.Fatalf("BranchDetect: %v", err)
	}
	if branchInfo.Repo != filepath.Base(work) {
		t.Fatalf("BranchDetect result = %+v, want Repo=%s", branchInfo, filepath.Base(work))
	}

	if _, err := p.BranchDetect(ctx, added.Path); !errors.Is(err, scriptout.ErrUnavailable) {
		t.Fatalf("BranchDetect(linked worktree) err = %v, want errors.Is(err, ErrUnavailable) — genuinely unresolvable, must fail loud", err)
	}
}

// TestProvider_RealGit_WorktreeRemove_RefusesWorktreeNotCreatedByThisBackend
// is the real-git regression test for WorktreeRemove's new scoping check
// [bug: pg2-jn22x, review finding 22]. otherPath simulates a worktree some
// OTHER tool created directly under `.worktrees/` — e.g. this very
// workspace's own drain/workforest convention, `.worktrees/<bead-id>` —
// never through this backend's own WorktreeAdd. This test creates and
// operates entirely on its own throwaway temp-dir repo/worktree; it never
// touches this workspace's real `.worktrees/*`.
func TestProvider_RealGit_WorktreeRemove_RefusesWorktreeNotCreatedByThisBackend(t *testing.T) {
	fx := newRealGitFixture(t)
	repo := fx.Dir
	mustGit(t, fx, "branch", "other-tool-worktree")
	otherPath := filepath.Join(repo, ".worktrees", "some-bead-id")
	mustGit(t, fx, "worktree", "add", "--", otherPath, "other-tool-worktree")

	t.Chdir(repo)
	p := New(NewExecRunner())
	ctx := context.Background()

	if err := p.WorktreeRemove(ctx, otherPath); !errors.Is(err, scriptout.ErrNotFound) {
		t.Fatalf("WorktreeRemove(%s) err = %v, want errors.Is(err, ErrNotFound) — this backend must refuse to remove a worktree it did not create", otherPath, err)
	}
	if _, err := os.Stat(otherPath); err != nil {
		t.Fatalf("worktree at %s should still exist (removal must have been refused before any real `git worktree remove` ran): %v", otherPath, err)
	}
}

// TestProvider_RealGit_WorktreeRemove_SymlinkedPathVariant_StillMatches is
// the real-git regression test for WorktreeRemove's new symlink
// normalization [bug: pg2-jn22x, review finding 22]. Unlike
// newRealGitFixture (which deliberately pre-resolves its OWN symlinks by
// hand before ever calling this Provider, masking this exact gap per the
// review), this test builds a genuinely unresolved symlinked alias of a
// real worktree path and passes THAT to WorktreeRemove.
func TestProvider_RealGit_WorktreeRemove_SymlinkedPathVariant_StillMatches(t *testing.T) {
	fx := newRealGitFixture(t)
	repo := fx.Dir
	mustGit(t, fx, "branch", "feature")

	t.Chdir(repo)
	p := New(NewExecRunner())
	ctx := context.Background()

	added, err := p.WorktreeAdd(ctx, "feature")
	if err != nil {
		t.Fatalf("WorktreeAdd: %v", err)
	}

	aliasParent := t.TempDir()
	alias := filepath.Join(aliasParent, "alias")
	if err := os.Symlink(filepath.Dir(added.Path), alias); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	aliasedPath := filepath.Join(alias, filepath.Base(added.Path))

	if err := p.WorktreeRemove(ctx, aliasedPath); err != nil {
		t.Fatalf("WorktreeRemove(%s) (a symlinked alias of the real worktree path %s): %v, want success", aliasedPath, added.Path, err)
	}
	if _, err := os.Stat(added.Path); !os.IsNotExist(err) {
		t.Fatalf("worktree path %s still exists after WorktreeRemove via its symlinked alias", added.Path)
	}
}
