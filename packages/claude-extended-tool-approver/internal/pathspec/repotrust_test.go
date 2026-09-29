package pathspec

import (
	"os"
	"path/filepath"
	"testing"
)

// r2Fixture builds a project root (a plain `.git` directory is enough — the
// same convention repoFixture/fixture() in this package and
// internal/effectpolicy use, since worktree.go/gitKind only ever check for
// the marker's presence), a git-worktree root at
// `<root>/.worktrees/<branch>` (a `.git` FILE, matching what `git worktree
// add` writes — see worktree.go's IsWorktreeRoot), a nested git repo at
// `<root>/vendored` that is NEITHER a worktree nor a recorded submodule, and
// a nested git repo at `<root>/vendored-submodule` that IS recorded as a
// submodule in the root's own .gitmodules. Untracked content lives at
// `<root>/scratch/note.txt`.
func r2Fixture(t *testing.T) (root string) {
	t.Helper()
	root = t.TempDir()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.Mkdir(filepath.Join(root, ".git"), 0o755))
	must(os.MkdirAll(filepath.Join(root, ".worktrees", "feature-x"), 0o755))
	must(os.WriteFile(filepath.Join(root, ".worktrees", "feature-x", ".git"),
		[]byte("gitdir: "+filepath.Join(root, ".git", "worktrees", "feature-x")+"\n"), 0o644))
	must(os.MkdirAll(filepath.Join(root, "vendored"), 0o755))
	must(os.Mkdir(filepath.Join(root, "vendored", ".git"), 0o755))
	must(os.MkdirAll(filepath.Join(root, "vendored-submodule"), 0o755))
	must(os.Mkdir(filepath.Join(root, "vendored-submodule", ".git"), 0o755))
	must(os.WriteFile(filepath.Join(root, ".gitmodules"),
		[]byte("[submodule \"vendored-submodule\"]\n\tpath = vendored-submodule\n\turl = https://example.invalid/x\n"), 0o644))
	must(os.MkdirAll(filepath.Join(root, "scratch"), 0o755))
	must(os.WriteFile(filepath.Join(root, "scratch", "note.txt"), []byte("hi\n"), 0o644))
	r, err := filepath.EvalSymlinks(root)
	must(err)
	return r
}

// TestR2Boundary: project root ⇒ inside; a git worktree of the project ⇒
// inside; a nested git repo that is neither a worktree nor a recorded
// submodule gitlink ⇒ outside; a recorded submodule ⇒ inside; untracked
// non-git content under the root ⇒ inside.
func TestR2Boundary(t *testing.T) {
	root := r2Fixture(t)
	cases := []struct {
		name       string
		path       string
		wantInside bool
	}{
		{"project root itself", root, true},
		{"a git worktree of the project", filepath.Join(root, ".worktrees", "feature-x"), true},
		{"content inside a worktree", filepath.Join(root, ".worktrees", "feature-x", "file.go"), true},
		{"nested repo, neither worktree nor submodule", filepath.Join(root, "vendored"), false},
		{"content inside that nested repo", filepath.Join(root, "vendored", "lib.go"), false},
		{"a recorded submodule", filepath.Join(root, "vendored-submodule"), true},
		{"content inside a recorded submodule", filepath.Join(root, "vendored-submodule", "lib.go"), true},
		{"untracked non-git content under the root", filepath.Join(root, "scratch", "note.txt"), true},
		{"a not-yet-created path under the root", filepath.Join(root, "scratch", "new.txt"), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cache := NewR2Cache()
			inside, reason := InsideRepoTrust(cache, root, c.path)
			if inside != c.wantInside {
				t.Errorf("InsideRepoTrust(%q) = %v (%q), want inside=%v", c.path, inside, reason, c.wantInside)
			}
		})
	}
}

// TestR2BoundaryWorkforestSibling: a sibling repo in the same pn-workspace
// workforest SET (a coordinated set's own per-repo worktree, one level
// deeper than a single repo's `.worktrees/<branch>` convention — see
// worktree.go's IsWorkforestSetContainer doc comment) is INSIDE the R2
// boundary; an unrelated repo elsewhere on disk is not.
func TestR2BoundaryWorkforestSibling(t *testing.T) {
	wsRoot := t.TempDir()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.WriteFile(filepath.Join(wsRoot, "pn-workspace.toml"), []byte("[workspace]\nname = 'x'\n"), 0o644))
	setDir := filepath.Join(wsRoot, ".workforests", "my-feature")
	repoA := filepath.Join(setDir, "repo-a")
	repoB := filepath.Join(setDir, "repo-b")
	must(os.MkdirAll(repoA, 0o755))
	must(os.Mkdir(filepath.Join(repoA, ".git"), 0o755))
	must(os.MkdirAll(repoB, 0o755))
	must(os.Mkdir(filepath.Join(repoB, ".git"), 0o755))

	unrelated := t.TempDir()
	must(os.Mkdir(filepath.Join(unrelated, ".git"), 0o755))

	r, err := filepath.EvalSymlinks(repoA)
	must(err)
	repoA = r
	r, err = filepath.EvalSymlinks(repoB)
	must(err)
	repoB = r
	r, err = filepath.EvalSymlinks(unrelated)
	must(err)
	unrelated = r

	cache := NewR2Cache()
	if inside, reason := InsideRepoTrust(cache, repoA, filepath.Join(repoB, "file.go")); !inside {
		t.Errorf("sibling workforest repo should be inside: %v (%q)", inside, reason)
	}
	if inside, reason := InsideRepoTrust(cache, repoA, filepath.Join(unrelated, "file.go")); inside {
		t.Errorf("an unrelated repo elsewhere on disk should be outside: %v (%q)", inside, reason)
	}
}

// TestR2CacheHit: repeated evaluation within one hook invocation (one
// R2Cache instance) reuses the cached pn-workspace.toml lookup rather than
// re-reading it per effect — proven by deleting pn-workspace.toml itself
// after the first lookup and confirming the SECOND lookup for the SAME
// project root still returns the memoized (non-empty) result, which a fresh
// (uncached) lookup against the now-deleted file could not produce.
func TestR2CacheHit(t *testing.T) {
	wsRoot := t.TempDir()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	tomlPath := filepath.Join(wsRoot, "pn-workspace.toml")
	must(os.WriteFile(tomlPath, []byte("[workspace]\nname = 'x'\n"), 0o644))
	setDir := filepath.Join(wsRoot, ".workforests", "my-feature")
	repoA := filepath.Join(setDir, "repo-a")
	repoB := filepath.Join(setDir, "repo-b")
	must(os.MkdirAll(repoA, 0o755))
	must(os.MkdirAll(repoB, 0o755))
	r, err := filepath.EvalSymlinks(repoA)
	must(err)
	repoA = r

	cache := NewR2Cache()
	first := cache.WorkforestMembers(repoA)
	if len(first) == 0 {
		t.Fatalf("expected a non-empty workforest membership set before the cache-hit probe, got %v", first)
	}

	must(os.Remove(tomlPath))

	second := cache.WorkforestMembers(repoA)
	if len(second) != len(first) {
		t.Errorf("second WorkforestMembers call did not reuse the cache (pn-workspace.toml was removed, so an uncached lookup would return nil): first=%v second=%v", first, second)
	}
}

// TestR2BoundaryNilCache: InsideRepoTrust still answers correctly (a slower,
// uncached lookup) when cache is nil — never a wrong answer or a panic.
func TestR2BoundaryNilCache(t *testing.T) {
	root := r2Fixture(t)
	if inside, _ := InsideRepoTrust(nil, root, filepath.Join(root, "scratch", "note.txt")); !inside {
		t.Error("untracked content under the root should be inside even with a nil cache")
	}
}
