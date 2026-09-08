package deletable

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
// `.worktrees` (git kind, depth 1), or a direct child of a pn workforest SET
// CONTAINER (pn kind, depth 2 under the workspace's declared, default or
// configured, workforests_dir — tc-8og1 item 1) — independent of any `.git`
// file.
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
		t.Error("nested two levels deep under .worktrees: want false")
	}
	if IsDeclaredWorktreeSlot(filepath.Join(root, "plain", "sub")) {
		t.Error("unrelated path: want false")
	}

	pnroot := t.TempDir()
	if err := os.WriteFile(filepath.Join(pnroot, "pn-workspace.toml"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	mkdirs(t, pnroot, ".workforests/set1/repoa", ".workforests/set1/repoa/sub")
	if IsDeclaredWorktreeSlot(filepath.Join(pnroot, ".workforests", "set1")) {
		t.Error("pn set CONTAINER itself (depth 1 under default workforests_dir): want false — it is judged as a set, not a slot (see TestIsWorkforestSetContainer)")
	}
	if !IsDeclaredWorktreeSlot(filepath.Join(pnroot, ".workforests", "set1", "repoa")) {
		t.Error("pn per-repo slot nested under a set (depth 2 under default workforests_dir): want true")
	}
	if IsDeclaredWorktreeSlot(filepath.Join(pnroot, ".workforests", "set1", "repoa", "sub")) {
		t.Error("nested three levels deep under workforests_dir: want false")
	}

	if err := os.WriteFile(filepath.Join(pnroot, "pn-workspace.toml"), []byte("[workspace]\nworkforests_dir = 'sets'\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mkdirs(t, pnroot, "sets/set1/repoa")
	if !IsDeclaredWorktreeSlot(filepath.Join(pnroot, "sets", "set1", "repoa")) {
		t.Error("pn per-repo slot under a configured workforests_dir: want true")
	}
	if IsDeclaredWorktreeSlot(filepath.Join(pnroot, "sets", "set1")) {
		t.Error("pn set container under a configured workforests_dir: want false")
	}
	if IsDeclaredWorktreeSlot(filepath.Join(pnroot, ".workforests", "set1", "repoa")) {
		t.Error("stale default dir once reconfigured to 'sets': want false")
	}
}

// TestIsWorkforestSetContainer: the pn workforest SET's own container
// directory — `<workforests_dir>/<set>` — is recognized independent of
// whether it currently has any member subdirectories, while a per-repo slot
// nested one level deeper is not itself a container.
func TestIsWorkforestSetContainer(t *testing.T) {
	pnroot := t.TempDir()
	if err := os.WriteFile(filepath.Join(pnroot, "pn-workspace.toml"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	mkdirs(t, pnroot, ".workforests/set1/repoa", ".workforests/empty-set")
	if !IsWorkforestSetContainer(filepath.Join(pnroot, ".workforests", "set1")) {
		t.Error("set container with a member: want true")
	}
	if !IsWorkforestSetContainer(filepath.Join(pnroot, ".workforests", "empty-set")) {
		t.Error("set container with no members yet: want true (location alone decides)")
	}
	if IsWorkforestSetContainer(filepath.Join(pnroot, ".workforests", "set1", "repoa")) {
		t.Error("a per-repo slot nested under a set: want false — it is a slot, not a container")
	}
	if IsWorkforestSetContainer(filepath.Join(pnroot, ".workforests")) {
		t.Error("workforests_dir itself is not a CHILD of workforests_dir: want false")
	}
	if IsWorkforestSetContainer(pnroot) {
		t.Error("the pn workspace root itself: want false")
	}

	plain := t.TempDir()
	mkdirs(t, plain, "notpn/set1")
	if IsWorkforestSetContainer(filepath.Join(plain, "notpn", "set1")) {
		t.Error("no pn-workspace.toml anywhere above: want false")
	}
}

// TestProbeWorkforestSetState exercises the worst-of-members combining rule
// (tc-8og1 item 1: any dirty -> Dirty, all clean -> Clean, else -> Unknown),
// via the SAME SetWorktreeStateProbe injection seam a single slot uses. The
// fake keys on a basename PREFIX (not exact match, unlike fixture()'s in
// internal/effectpolicy) so more than one differently-named "clean" member
// can be exercised in the all-clean case.
func TestProbeWorkforestSetState(t *testing.T) {
	restore := SetWorktreeStateProbe(func(slotRoot string) (WorktreeState, error) {
		base := filepath.Base(slotRoot)
		switch {
		case strings.HasPrefix(base, "clean") && !strings.Contains(base, "ignored"):
			return WorktreeClean, nil
		case strings.HasPrefix(base, "dirty"):
			return WorktreeDirty, nil
		case strings.Contains(base, "ignored"):
			return WorktreeCleanIgnored, nil
		default:
			return WorktreeUnknown, fmt.Errorf("fixture fake: %s is not a real git repository", slotRoot)
		}
	})
	defer restore()

	allClean := t.TempDir()
	mkdirs(t, allClean, "clean-repoa", "clean-repob")
	state, err := ProbeWorkforestSetState(allClean)
	if err != nil {
		t.Fatalf("all-clean set: unexpected error: %v", err)
	}
	if state != WorktreeClean {
		t.Fatalf("all-clean set: got %s, want clean", state)
	}

	anyDirty := t.TempDir()
	mkdirs(t, anyDirty, "clean", "dirty")
	state, err = ProbeWorkforestSetState(anyDirty)
	if err != nil {
		t.Fatalf("mixed-with-dirty set: unexpected error: %v", err)
	}
	if state != WorktreeDirty {
		t.Fatalf("mixed-with-dirty set: got %s, want dirty", state)
	}

	mixedNoDirty := t.TempDir()
	mkdirs(t, mixedNoDirty, "clean", "ignored-only")
	state, err = ProbeWorkforestSetState(mixedNoDirty)
	if err != nil {
		t.Fatalf("mixed clean/ignored-only set: unexpected error: %v", err)
	}
	if state != WorktreeUnknown {
		t.Fatalf("mixed clean/ignored-only set: got %s, want unknown (abstain)", state)
	}

	empty := t.TempDir()
	state, err = ProbeWorkforestSetState(empty)
	if state != WorktreeUnknown {
		t.Fatalf("empty set container: got %s, want unknown", state)
	}
	if err == nil {
		t.Fatal("empty set container: expected a non-nil error")
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
