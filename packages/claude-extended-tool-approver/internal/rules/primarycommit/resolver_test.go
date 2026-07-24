package primarycommit

import (
	"os/exec"
	"path/filepath"
	"testing"
)

// Contract test: pins our assumptions about the on-disk git file formats the resolver
// reads (.git dir-vs-file, .git/HEAD ref line, .git/config section). Uses real `git`
// only to BUILD fixtures; the resolver itself never shells out. Requires git on PATH.
func TestFileResolver_Contract(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	dir := t.TempDir()
	git := func(d string, args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"-C", d}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git -C %s %v: %v\n%s", d, args, err, out)
		}
	}
	git(dir, "init", "-q", "-b", "trunk")
	git(dir, "config", "user.email", "t@example.com")
	git(dir, "config", "user.name", "t")

	r := NewFileResolver()

	// Main working tree is canonical (.git is a directory).
	if c, err := r.IsCanonical(dir); err != nil || !c {
		t.Fatalf("IsCanonical(main) = %v, %v; want true", c, err)
	}
	// CurrentBranch reads .git/HEAD — works on an UNBORN branch.
	if b, err := r.CurrentBranch(dir); err != nil || b != "trunk" {
		t.Fatalf("CurrentBranch = %q, %v; want trunk", b, err)
	}
	// No config key -> default main.
	if p, err := r.PrimaryBranch(dir); err != nil || p != "main" {
		t.Fatalf("PrimaryBranch(default) = %q, %v; want main", p, err)
	}
	// Config override wins.
	git(dir, "config", "pgii-integrate-branch.primaryBranch", "mainline")
	if p, _ := r.PrimaryBranch(dir); p != "mainline" {
		t.Fatalf("PrimaryBranch(config) = %q; want mainline", p)
	}
	// A linked worktree is NOT canonical (.git is a gitdir: file).
	git(dir, "commit", "--allow-empty", "-q", "-m", "init")
	wt := filepath.Join(t.TempDir(), "wt")
	git(dir, "worktree", "add", "-q", "-b", "feature", wt)
	if c, err := r.IsCanonical(wt); err != nil || c {
		t.Fatalf("IsCanonical(worktree) = %v, %v; want false", c, err)
	}
}
