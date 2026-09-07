package deletable

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// git runs a git subcommand `-C dir <args...>` hermetically (hermeticGitEnviron
// — the SAME helper realWorktreeState uses in production, per this slice's
// "every git call in tests goes through the hermetic helper"), failing the
// test on a non-zero exit.
func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = hermeticGitEnviron()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git -C %s %v: %v\n%s", dir, args, err, out)
	}
}

// gitTestRepo builds a real, throwaway git repository (t.TempDir()) with an
// initial commit on branch "main", entirely via the hermetic git() helper
// above. HOME is pointed at a second, separate t.TempDir() as an extra
// isolation layer on top of hermeticGitEnviron's GIT_CONFIG_GLOBAL=/dev/null
// (mirrors internal/engine/primarycommit_worktree_test.go's
// nestedWorktreeFixture) — no invocation in this file ever touches the real
// user's home directory or an ambient repository.
func gitTestRepo(t *testing.T) string {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	git(t, root, "init", "-q", "-b", "main")
	git(t, root, "config", "user.email", "t@example.com")
	git(t, root, "config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, root, "add", "README.md")
	git(t, root, "commit", "-q", "-m", "init")
	return root
}

// TestProbeWorktreeStateClean: a freshly added linked worktree, nothing
// touched, is clean.
func TestProbeWorktreeStateClean(t *testing.T) {
	root := gitTestRepo(t)
	worktree := filepath.Join(root, "wt")
	git(t, root, "worktree", "add", "-q", "-b", "feat", worktree)
	state, err := ProbeWorktreeState(worktree)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state != WorktreeClean {
		t.Fatalf("got %s, want clean", state)
	}
}

// TestProbeWorktreeStateDirtyModified: an edited tracked file is dirty.
func TestProbeWorktreeStateDirtyModified(t *testing.T) {
	root := gitTestRepo(t)
	worktree := filepath.Join(root, "wt")
	git(t, root, "worktree", "add", "-q", "-b", "feat", worktree)
	if err := os.WriteFile(filepath.Join(worktree, "README.md"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	state, err := ProbeWorktreeState(worktree)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state != WorktreeDirty {
		t.Fatalf("got %s, want dirty", state)
	}
}

// TestProbeWorktreeStateDirtyUntracked: an untracked, non-ignored file is
// dirty (never merely "ignored").
func TestProbeWorktreeStateDirtyUntracked(t *testing.T) {
	root := gitTestRepo(t)
	worktree := filepath.Join(root, "wt")
	git(t, root, "worktree", "add", "-q", "-b", "feat", worktree)
	if err := os.WriteFile(filepath.Join(worktree, "new.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	state, err := ProbeWorktreeState(worktree)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state != WorktreeDirty {
		t.Fatalf("got %s, want dirty (untracked)", state)
	}
}

// TestProbeWorktreeStateDirtyStaged: a staged (added, not committed) file is
// dirty.
func TestProbeWorktreeStateDirtyStaged(t *testing.T) {
	root := gitTestRepo(t)
	worktree := filepath.Join(root, "wt")
	git(t, root, "worktree", "add", "-q", "-b", "feat", worktree)
	if err := os.WriteFile(filepath.Join(worktree, "new.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, worktree, "add", "new.txt")
	state, err := ProbeWorktreeState(worktree)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state != WorktreeDirty {
		t.Fatalf("got %s, want dirty (staged)", state)
	}
}

// TestProbeWorktreeStateCleanIgnored: no tracked modification and no
// untracked-non-ignored file, but a `.gitignore`d file is present.
func TestProbeWorktreeStateCleanIgnored(t *testing.T) {
	root := gitTestRepo(t)
	worktree := filepath.Join(root, "wt")
	git(t, root, "worktree", "add", "-q", "-b", "feat", worktree)
	if err := os.WriteFile(filepath.Join(worktree, ".gitignore"), []byte("ignored.log\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, worktree, "add", ".gitignore")
	git(t, worktree, "commit", "-q", "-m", "add gitignore")
	if err := os.WriteFile(filepath.Join(worktree, "ignored.log"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	state, err := ProbeWorktreeState(worktree)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state != WorktreeCleanIgnored {
		t.Fatalf("got %s, want clean-ignored", state)
	}
}

// TestProbeWorktreeStateUnknownNotARepo: an ordinary directory (no `.git` at
// all) never resolves to clean.
func TestProbeWorktreeStateUnknownNotARepo(t *testing.T) {
	dir := t.TempDir()
	state, err := ProbeWorktreeState(dir)
	if state != WorktreeUnknown {
		t.Fatalf("got %s, want unknown", state)
	}
	if err == nil {
		t.Fatal("expected a non-nil error")
	}
}

// TestProbeWorktreeStateUnknownMissingPath: a path that does not exist on
// disk at all — the corpus harness's documented case — reports Unknown, not
// a panic or a hung process.
func TestProbeWorktreeStateUnknownMissingPath(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "does-not-exist")
	state, err := ProbeWorktreeState(dir)
	if state != WorktreeUnknown {
		t.Fatalf("got %s, want unknown", state)
	}
	if err == nil {
		t.Fatal("expected a non-nil error")
	}
}

// TestIsWorktreeRoot: the FILE-marker signal, positive for a linked
// worktree, negative for the canonical clone (`.git` is a directory there)
// and for a nonexistent path.
func TestIsWorktreeRoot(t *testing.T) {
	root := gitTestRepo(t)
	worktree := filepath.Join(root, "wt")
	git(t, root, "worktree", "add", "-q", "-b", "feat", worktree)
	if !IsWorktreeRoot(worktree) {
		t.Error("linked worktree: want IsWorktreeRoot true")
	}
	if IsWorktreeRoot(root) {
		t.Error("canonical clone (.git is a directory): want IsWorktreeRoot false")
	}
	if IsWorktreeRoot(filepath.Join(root, "does-not-exist")) {
		t.Error("nonexistent path: want IsWorktreeRoot false")
	}
}

// TestIsDeclaredWorktreeSlot: the LOCATION signal — a direct child of
// `.worktrees`, or of a pn workspace's declared (default or configured)
// workforests_dir — independent of any `.git` file.
func TestIsDeclaredWorktreeSlot(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, ".worktrees/feat", ".worktrees/feat/sub", "plain/sub")
	if !IsDeclaredWorktreeSlot(filepath.Join(root, ".worktrees", "feat")) {
		t.Error("direct child of .worktrees: want true")
	}
	if IsDeclaredWorktreeSlot(filepath.Join(root, ".worktrees")) {
		t.Error(".worktrees itself is not a CHILD of .worktrees: want false")
	}
	if IsDeclaredWorktreeSlot(filepath.Join(root, ".worktrees", "feat", "sub")) {
		t.Error("nested two levels deep: want false")
	}
	if IsDeclaredWorktreeSlot(filepath.Join(root, "plain", "sub")) {
		t.Error("unrelated path: want false")
	}

	pnroot := t.TempDir()
	if err := os.WriteFile(filepath.Join(pnroot, "pn-workspace.toml"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	mkdirs(t, pnroot, ".workforests/set1")
	if !IsDeclaredWorktreeSlot(filepath.Join(pnroot, ".workforests", "set1")) {
		t.Error("pn default workforests_dir child: want true")
	}

	if err := os.WriteFile(filepath.Join(pnroot, "pn-workspace.toml"), []byte("[workspace]\nworkforests_dir = 'sets'\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mkdirs(t, pnroot, "sets/set1")
	if !IsDeclaredWorktreeSlot(filepath.Join(pnroot, "sets", "set1")) {
		t.Error("pn configured workforests_dir child: want true")
	}
	if IsDeclaredWorktreeSlot(filepath.Join(pnroot, ".workforests", "set1")) {
		t.Error("stale default dir once reconfigured to 'sets': want false")
	}
}

// TestAtWorktreeRoot exercises each signal independently and confirms
// neither fires for an ordinary path.
func TestAtWorktreeRoot(t *testing.T) {
	root := gitTestRepo(t)

	adhoc := filepath.Join(filepath.Dir(root), "ceta-adhoc-worktree")
	git(t, root, "worktree", "add", "-q", "-b", "adhoc", adhoc)
	t.Cleanup(func() { _ = os.RemoveAll(adhoc) })
	if !AtWorktreeRoot(adhoc) {
		t.Error("ad hoc worktree outside .worktrees, by marker alone: want true")
	}

	slot := filepath.Join(root, ".worktrees", "notaworktree")
	if err := os.MkdirAll(slot, 0o755); err != nil {
		t.Fatal(err)
	}
	if !AtWorktreeRoot(slot) {
		t.Error("declared slot without a .git file, by location alone: want true")
	}

	if AtWorktreeRoot(filepath.Join(root, "README.md")) {
		t.Error("an ordinary tracked file: want false")
	}
	if AtWorktreeRoot(root) {
		t.Error("the canonical clone itself: want false")
	}
}

// TestSetWorktreeStateProbe: the injection seam substitutes and restores
// cleanly.
func TestSetWorktreeStateProbe(t *testing.T) {
	restore := SetWorktreeStateProbe(func(root string) (WorktreeState, error) {
		return WorktreeDirty, nil
	})
	state, err := ProbeWorktreeState("/anything")
	if err != nil || state != WorktreeDirty {
		restore()
		t.Fatalf("got %s, %v; want dirty, nil", state, err)
	}
	restore()
	if worktreeStateProbe == nil {
		t.Fatal("restore left the probe nil")
	}
}

// TestWorktreeStateString pins the names DeleteAccess embeds in reasons.
func TestWorktreeStateString(t *testing.T) {
	cases := map[WorktreeState]string{
		WorktreeClean:        "clean",
		WorktreeDirty:        "dirty",
		WorktreeCleanIgnored: "clean-ignored",
		WorktreeUnknown:      "unknown",
		WorktreeState(99):    "unknown",
	}
	for state, want := range cases {
		if got := state.String(); got != want {
			t.Errorf("%d: got %q, want %q", int(state), got, want)
		}
	}
}
