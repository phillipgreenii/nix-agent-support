package pathspec

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/patheval"
)

// mkRepo builds a minimal non-bare repo layout under a fresh temp dir:
// <root>/.git/{HEAD,objects,refs,hooks,config}.
func mkRepo(t *testing.T) string {
	t.Helper()
	root := patheval.ResolveRealPath(t.TempDir())
	for _, d := range []string{".git/objects", ".git/refs", ".git/hooks"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".git", "config"), []byte("[core]\n\tbare = false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

// asNonTemp makes every path classify as NON-temp for the test's duration:
// every directory a test creates sits under a temp root, so this is the
// only way to reach the Forbidden branch.
func asNonTemp(t *testing.T) {
	t.Helper()
	t.Cleanup(SetTempClassifierForTest(func(string) bool { return false }))
}

func TestGitDirProtection(t *testing.T) {
	t.Run("non-temp repo: .git writes are Forbidden", func(t *testing.T) {
		asNonTemp(t)
		root := mkRepo(t)
		for _, rel := range []string{".git/hooks/pre-commit", ".git/config", ".git", ".git/refs/heads/new", ".git/objects/ab/cdef"} {
			if v, reason := GitDirWriteVerdict(filepath.Join(root, rel)); v != Forbidden {
				t.Errorf("%s: %v (%s), want Forbidden", rel, v, reason)
			}
		}
		for _, rel := range []string{"README.md", "src/main.go", ".gitignore", ".github/workflows/x.yml"} {
			if v, _ := GitDirWriteVerdict(filepath.Join(root, rel)); v != Unknown {
				t.Errorf("%s: %v, want Unknown (ordinary path policy decides)", rel, v)
			}
		}
	})

	t.Run("temp repo (ADR 0059): same writes are Permitted", func(t *testing.T) {
		// default isTempPath: t.TempDir() is under a temp root.
		root := mkRepo(t)
		for _, rel := range []string{".git/hooks/pre-commit", ".git/config"} {
			if v, reason := GitDirWriteVerdict(filepath.Join(root, rel)); v != Permitted {
				t.Errorf("%s: %v (%s), want Permitted", rel, v, reason)
			}
		}
	})

	t.Run("symlink into .git is judged by the resolved path", func(t *testing.T) {
		asNonTemp(t)
		root := mkRepo(t)
		link := filepath.Join(root, "innocent")
		if err := os.Symlink(filepath.Join(root, ".git"), link); err != nil {
			t.Fatal(err)
		}
		// ResolveComponentwise is what the policy hands the verdict function.
		pe := patheval.NewWithCWD(root, root)
		abs := ResolveComponentwise(pe, "innocent/hooks/pre-commit")
		if abs == "" || !strings.HasSuffix(abs, ".git/hooks/pre-commit") {
			t.Fatalf("resolved %q, want the .git/hooks/pre-commit target", abs)
		}
		if v, reason := GitDirWriteVerdict(abs); v != Forbidden {
			t.Errorf("%v (%s), want Forbidden", v, reason)
		}
		// symlink-then-`..` hop: scratch/back -> <root>/.git/hooks, so
		// scratch/back/../config is <root>/.git/config.
		if err := os.MkdirAll(filepath.Join(root, "scratch"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(root, ".git", "hooks"), filepath.Join(root, "scratch", "back")); err != nil {
			t.Fatal(err)
		}
		abs = ResolveComponentwise(pe, "scratch/back/../config")
		if v, reason := GitDirWriteVerdict(abs); v != Forbidden {
			t.Errorf("%q: %v (%s), want Forbidden", abs, v, reason)
		}
	})

	t.Run("linked worktree: the .git file and the common dir are protected", func(t *testing.T) {
		asNonTemp(t)
		main := mkRepo(t)
		admin := filepath.Join(main, ".git", "worktrees", "wt")
		if err := os.MkdirAll(admin, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(admin, "commondir"), []byte("../..\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		wt := patheval.ResolveRealPath(t.TempDir())
		if err := os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: "+admin+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		for _, p := range []string{
			filepath.Join(wt, ".git"),                        // the pointer file
			filepath.Join(admin, "HEAD"),                     // the admin dir
			filepath.Join(main, ".git", "hooks", "x"),        // the COMMON dir in the main clone
			filepath.Join(main, ".git", "config"),            // shared config
			filepath.Join(wt, ".git", "hooks", "pre-commit"), // impossible path (a file, not dir) but still contained
		} {
			if v, reason := GitDirWriteVerdict(p); v != Forbidden {
				t.Errorf("%s: %v (%s), want Forbidden", p, v, reason)
			}
		}
		if v, _ := GitDirWriteVerdict(filepath.Join(wt, "file.txt")); v != Unknown {
			t.Errorf("ordinary worktree file: %v, want Unknown", v)
		}
	})

	t.Run("core.hooksPath target is protected", func(t *testing.T) {
		asNonTemp(t)
		root := mkRepo(t)
		hooks := filepath.Join(root, "ci-hooks")
		cfg := "[core]\n\thooksPath = ci-hooks\n"
		if err := os.WriteFile(filepath.Join(root, ".git", "config"), []byte(cfg), 0o644); err != nil {
			t.Fatal(err)
		}
		if v, reason := GitDirWriteVerdict(filepath.Join(hooks, "pre-push")); v != Forbidden {
			t.Errorf("%v (%s), want Forbidden", v, reason)
		}
		if v, _ := GitDirWriteVerdict(filepath.Join(root, "other", "pre-push")); v != Unknown {
			t.Errorf("a non-hooksPath dir: %v, want Unknown", v)
		}
	})

	t.Run("bare repo is protected as a git dir", func(t *testing.T) {
		asNonTemp(t)
		bare := patheval.ResolveRealPath(t.TempDir())
		for _, d := range []string{"objects", "refs", "hooks"} {
			if err := os.MkdirAll(filepath.Join(bare, d), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(bare, "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if v, reason := GitDirWriteVerdict(filepath.Join(bare, "hooks", "update")); v != Forbidden {
			t.Errorf("%v (%s), want Forbidden", v, reason)
		}
	})

	t.Run("a real git init agrees with the layout this models", func(t *testing.T) {
		asNonTemp(t)
		git, err := exec.LookPath("git")
		if err != nil {
			t.Skip("git not on PATH")
		}
		root := patheval.ResolveRealPath(t.TempDir())
		cmd := exec.Command(git, "-C", root, "init", "-q")
		cmd.Env = []string{"HOME=" + root, "PATH=" + os.Getenv("PATH"), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"}
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git init: %v: %s", err, out)
		}
		if v, reason := GitDirWriteVerdict(filepath.Join(root, ".git", "hooks", "pre-commit")); v != Forbidden {
			t.Errorf("%v (%s), want Forbidden", v, reason)
		}
	})
}
