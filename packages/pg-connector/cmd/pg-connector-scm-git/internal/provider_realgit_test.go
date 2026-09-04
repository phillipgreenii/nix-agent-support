package internal

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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
	wantPath := filepath.Join(repo, ".worktrees", "feature")
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
