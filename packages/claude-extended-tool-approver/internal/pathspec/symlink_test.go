package pathspec

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/patheval"
)

// TestSymlink covers this packet's Objective/Validation for symlink
// resolution: (1) a symlink whose target itself contains a further symlink
// resolves component-wise; (2) a resolved path that uses `..` to climb back
// inside a forbidden zone after passing through a symlink is still
// classified against its REAL (post-resolution) location, never the
// pre-resolution literal path.
func TestSymlink(t *testing.T) {
	t.Run("multi-hop symlink chain resolves component-wise", func(t *testing.T) {
		root := t.TempDir()
		must := func(err error) {
			t.Helper()
			if err != nil {
				t.Fatal(err)
			}
		}
		must(os.MkdirAll(filepath.Join(root, "real", "target"), 0o755))
		must(os.WriteFile(filepath.Join(root, "real", "target", "file.txt"), []byte("hi\n"), 0o644))
		// hop2 -> real/target, hop1 -> hop2 (a symlink whose OWN target is
		// itself a further symlink).
		must(os.Symlink(filepath.Join(root, "real", "target"), filepath.Join(root, "hop2")))
		must(os.Symlink(filepath.Join(root, "hop2"), filepath.Join(root, "hop1")))
		r, err := filepath.EvalSymlinks(root)
		must(err)
		root = r
		pe := patheval.NewWithCWD(root, root)

		got := ResolveComponentwise(pe, "hop1/file.txt")
		want := filepath.Join(root, "real", "target", "file.txt")
		if got != want {
			t.Errorf("ResolveComponentwise(%q) = %q, want %q (the fully-resolved real location, through BOTH symlink hops)", "hop1/file.txt", got, want)
		}
	})

	t.Run("a symlink pointing outside a zone followed by .. does not silently re-enter it", func(t *testing.T) {
		root := t.TempDir()
		must := func(err error) {
			t.Helper()
			if err != nil {
				t.Fatal(err)
			}
		}
		// protected/.git marks protected/ as a git workspace root, whose
		// `.git` entry is Delete: Protected (gitKind's Classify,
		// workspace.go) — the "forbidden zone" this case climbs back into.
		must(os.Mkdir(filepath.Join(root, "protected"), 0o755))
		must(os.Mkdir(filepath.Join(root, "protected", ".git"), 0o755))
		must(os.Mkdir(filepath.Join(root, "protected", "sub"), 0o755))
		must(os.Mkdir(filepath.Join(root, "scratch"), 0o755))
		// scratch/back is a symlink living in an UNPROTECTED tree, pointing
		// OUTSIDE it into protected/sub.
		must(os.Symlink(filepath.Join(root, "protected", "sub"), filepath.Join(root, "scratch", "back")))
		for _, v := range []string{"WORKSPACE_ROOT", "CETA_EXTRA_READWRITE_ROOTS", "CETA_EXTRA_READONLY_ROOTS", "CETA_DENIED_ROOTS", "MONOREPO_ROOT"} {
			t.Setenv(v, "")
		}
		r, err := filepath.EvalSymlinks(root)
		must(err)
		root = r
		pe := patheval.NewWithCWD(root, root)

		// literal is built by PLAIN STRING CONCATENATION, never
		// filepath.Join (which would itself lexically clean "back/.."
		// before this test even ran, defeating the point of the fixture).
		literal := "scratch/back/../.git"

		// Sanity: confirm this fixture actually exercises resolution — the
		// LEXICAL (symlink-unaware) reading of literal names a DIFFERENT,
		// non-special path (scratch/.git, which does not even exist) than
		// the REAL, physically-resolved one (protected/.git). If a future
		// refactor makes these equal, this fixture would silently stop
		// testing anything.
		lexical := filepath.Clean(filepath.Join(root, literal))
		realTarget := filepath.Join(root, "protected", ".git")
		if lexical == realTarget {
			t.Fatalf("fixture does not distinguish the literal path from the real one: both are %s", lexical)
		}

		class, reason := ClassifyWith(pe, DefaultKinds(), literal)
		if class != Protected {
			t.Errorf("Classify(%q) = %s (%q), want Protected — the resolved path (%s) lands inside the protected git workspace's .git, but a lexical (pre-resolution) reading would have named the ordinary, unprotected %s instead",
				literal, class, reason, realTarget, lexical)
		}
	})
}
