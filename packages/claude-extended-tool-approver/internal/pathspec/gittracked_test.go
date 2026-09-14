package pathspec

import (
	"os"
	"path/filepath"
	"testing"
)

// TestGitTrackedNonSecret exercises the git kind's Secrecy declaration
// (tc-lc8f item 3z; deletable.go's "# NON-SECRET declarations" doc comment
// carries the two operator rulings) against a REAL throwaway repository —
// gitTestRepo/git, worktree_test.go's helpers, entirely hermetic — never
// resolving into the real checkout this session runs from. Every case the
// brief named:
//
//   - tracked (in the index, not gitignored) => non-secret
//   - ignored, EVEN WHEN force-tracked despite the .gitignore match => no
//     opinion (the "not gitignored" half of ruling 2, distinct from a plain
//     untracked file — see the force-add case below)
//   - untracked, non-ignored => no opinion
//   - a directory with tracked children => non-secret
//   - a nonexistent path => no opinion, never an error or a panic
func TestGitTrackedNonSecret(t *testing.T) {
	root := gitTestRepo(t)
	if err := os.MkdirAll(filepath.Join(root, "secrets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "secrets", "committed.yaml"), []byte("k: v\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("secrets/.env\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, root, "add", "secrets/committed.yaml", ".gitignore")
	git(t, root, "commit", "-q", "-m", "add secrets dir")

	// secrets/.env: matches .gitignore AND is force-added into the index
	// anyway (git add -f) — a tracked file that is ALSO gitignored, the
	// one case that distinguishes "not gitignored" from "merely untracked".
	if err := os.WriteFile(filepath.Join(root, "secrets", ".env"), []byte("S=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, root, "add", "-f", "secrets/.env")
	git(t, root, "commit", "-q", "-m", "force-add ignored env")

	// secrets/token: plain untracked, non-ignored file.
	if err := os.WriteFile(filepath.Join(root, "secrets", "token"), []byte("t\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		rel  string
		want bool // want NonSecretWith's ok
	}{
		{"tracked file is non-secret", "secrets/committed.yaml", true},
		{"tracked-but-ignored file: no opinion", "secrets/.env", false},
		{"untracked non-ignored file: no opinion", "secrets/token", false},
		{"directory with tracked children is non-secret", "secrets", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			abs := filepath.Join(root, filepath.FromSlash(c.rel))
			ok, reason := NonSecretWith(DefaultKinds(), abs)
			if ok != c.want {
				t.Errorf("NonSecretWith(%q) = (%v, %q), want ok=%v", c.rel, ok, reason, c.want)
			}
			if ok && reason == "" {
				t.Error("non-secret opinion carries no reason")
			}
		})
	}

	// Nonexistent path: no opinion, never an error/panic — the corpus
	// harness's documented historical-CWD case (ProbeWorktreeState's own
	// TestProbeWorktreeStateUnknownMissingPath is the sibling test for the
	// worktree-state probe).
	t.Run("nonexistent path: no opinion", func(t *testing.T) {
		abs := filepath.Join(root, "secrets", "does-not-exist")
		ok, reason := NonSecretWith(DefaultKinds(), abs)
		if ok {
			t.Errorf("nonexistent path: got non-secret (%q), want no opinion", reason)
		}
	})
}

// TestGitTrackedNonSecretOutsideGitRepo: a plain directory (no `.git` at
// all) never gets a git-kind opinion — NonSecretWith must not start a git
// process, let alone crash, when no workspace declares a candidate at all.
func TestGitTrackedNonSecretOutsideGitRepo(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "secrets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "secrets", "x"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ok, reason := NonSecretWith(DefaultKinds(), filepath.Join(dir, "secrets", "x"))
	if ok {
		t.Errorf("outside any git repository: got non-secret (%q), want no opinion", reason)
	}
}

// TestSetGitTrackedProbe: the injection seam substitutes and restores
// cleanly, mirroring TestSetWorktreeStateProbe.
func TestSetGitTrackedProbe(t *testing.T) {
	restore := SetGitTrackedProbe(func(root, rel string, isDir bool) (bool, error) {
		return true, nil
	})
	tracked, err := gitTrackedProbe("/anything", "anything", false)
	if err != nil || !tracked {
		restore()
		t.Fatalf("got %v, %v; want true, nil", tracked, err)
	}
	restore()
	if gitTrackedProbe == nil {
		t.Fatal("restore left the probe nil")
	}
}

// TestRealGitTracked exercises realGitTracked directly (rather than through
// the Kind/workspace layer above) so a file/directory-operand distinction
// and the genuine-probe-error path (root not a git repository at all) are
// each pinned independent of gitKind's own Ignored()/rel=="." guards.
func TestRealGitTracked(t *testing.T) {
	root := gitTestRepo(t)
	if err := os.MkdirAll(filepath.Join(root, "d"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "d", "f.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, root, "add", "d/f.txt")
	git(t, root, "commit", "-q", "-m", "add d/f.txt")
	if err := os.WriteFile(filepath.Join(root, "untracked.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if tracked, err := realGitTracked(root, "d/f.txt", false); err != nil || !tracked {
		t.Errorf("tracked file: got %v, %v; want true, nil", tracked, err)
	}
	if tracked, err := realGitTracked(root, "untracked.txt", false); err != nil || tracked {
		t.Errorf("untracked file: got %v, %v; want false, nil", tracked, err)
	}
	if tracked, err := realGitTracked(root, "d", true); err != nil || !tracked {
		t.Errorf("directory with a tracked child: got %v, %v; want true, nil", tracked, err)
	}
	emptyDir := filepath.Join(root, "empty")
	if err := os.MkdirAll(emptyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if tracked, err := realGitTracked(root, "empty", true); err != nil || tracked {
		t.Errorf("directory with no tracked children: got %v, %v; want false, nil", tracked, err)
	}

	// A genuine probe failure (not a git repository at all) is a real
	// error, never silently "not tracked".
	notARepo := t.TempDir()
	if _, err := realGitTracked(notARepo, "x", false); err == nil {
		t.Error("root is not a git repository: want a non-nil error")
	}
}
