package internal

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// setupEnv returns a hermetic environment for the plain `git` invocations
// this file's own fixture setup makes directly — bypassing this package's
// Runner/gitenv seam entirely, since fixture setup is not the code under
// test. HOME is pointed at a fresh temp dir and both the global and system
// config files are pinned to /dev/null, so nothing this setup does can
// read or write the real developer/CI environment's own git config
// (mirrors packages/pg-pr/internal/gitfixture's already-established
// pattern in this repo — a different Go module, so not importable here).
func setupEnv(t *testing.T) []string {
	t.Helper()
	ambient := map[string]string{}
	for _, kv := range os.Environ() {
		if k, v, ok := strings.Cut(kv, "="); ok {
			ambient[k] = v
		}
	}
	env := []string{
		"HOME=" + t.TempDir(),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
	}
	for _, k := range []string{"PATH", "TMPDIR"} {
		if v, ok := ambient[k]; ok {
			env = append(env, k+"="+v)
		}
	}
	return env
}

func setupGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	full := append([]string{"-C", dir}, args...)
	cmd := exec.Command("git", full...)
	cmd.Env = setupEnv(t)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// newRealGitFixture creates a throwaway repo with one commit on branch
// "main", returning its absolute, symlink-resolved path (matching how
// git itself reports paths on macOS, where a t.TempDir() lives under a
// /var symlink to /private/var).
func newRealGitFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("resolve symlinks: %v", err)
	}
	setupGit(t, resolved, "init", "-b", "main")
	setupGit(t, resolved, "commit", "--allow-empty", "-m", "initial")
	return resolved
}

// TestProvider_RealGit_WorktreeAddListRemoveAndBranchDetect is the
// packet's required test verifying this Provider against a REAL git
// checkout, not just the fakeRunner-backed tests above — the packet's own
// AC: "backed by real local git state (verified against a real git
// checkout in tests, not just mocks, for at least one method)". It
// exercises all four scm.Provider methods, not just one.
func TestProvider_RealGit_WorktreeAddListRemoveAndBranchDetect(t *testing.T) {
	repo := newRealGitFixture(t)
	setupGit(t, repo, "branch", "feature")

	// WorktreeAdd/WorktreeRemove/WorktreeList carry no repo/cwd wire
	// argument of their own [design: §4.7] — they resolve "the current
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
	// response [design: §4.5, §4.7].
	if err := p.WorktreeRemove(ctx, added.Path); err == nil {
		t.Fatal("WorktreeRemove of an already-removed path = nil error, want ErrNotFound")
	}
}

// newRealBareGitFixture creates a throwaway BARE repo with one commit on
// branch "main", returning its absolute, symlink-resolved path. A bare
// repo cannot receive a commit directly, so this goes via a throwaway
// non-bare clone that pushes into it and is then discarded.
func newRealBareGitFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("resolve symlinks: %v", err)
	}
	bare := filepath.Join(resolved, "repo.git")
	setupGit(t, resolved, "init", "--bare", "-b", "main", bare)
	clone := filepath.Join(resolved, "seed-clone")
	setupGit(t, resolved, "clone", bare, clone)
	setupGit(t, clone, "commit", "--allow-empty", "-m", "initial")
	setupGit(t, clone, "push", "origin", "main")
	if err := os.RemoveAll(clone); err != nil {
		t.Fatalf("remove seed clone: %v", err)
	}
	return bare
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

// newRealSeparateGitDirFixture inits a repo at <root>/work whose git
// directory is relocated to <root>/actual-git-dir via
// `--separate-git-dir`, with one commit on branch "main". Returns the
// working-tree root.
func newRealSeparateGitDirFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("resolve symlinks: %v", err)
	}
	work := filepath.Join(resolved, "work")
	gitDir := filepath.Join(resolved, "actual-git-dir")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatalf("mkdir work: %v", err)
	}
	setupGit(t, work, "init", "-b", "main", "--separate-git-dir="+gitDir)
	setupGit(t, work, "commit", "--allow-empty", "-m", "initial")
	return work
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
	work := newRealSeparateGitDirFixture(t)
	setupGit(t, work, "branch", "feature")

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
	repo := newRealGitFixture(t)
	setupGit(t, repo, "branch", "other-tool-worktree")
	otherPath := filepath.Join(repo, ".worktrees", "some-bead-id")
	setupGit(t, repo, "worktree", "add", "--", otherPath, "other-tool-worktree")

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
	repo := newRealGitFixture(t)
	setupGit(t, repo, "branch", "feature")

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
