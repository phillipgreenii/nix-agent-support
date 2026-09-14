package pathspec

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/patheval"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/temproot"
)

// repoFixture builds a throwaway git working tree (a `.git` directory is
// enough for patheval.GitRoot) with a .gitignore naming `*.log`, `build/`
// and `.env`, plus a tracked README.md, an ignored ignored.log, an ignored
// build/ directory, an un-ignored sub/ directory, and an ignored .env.
func repoFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.Mkdir(filepath.Join(root, ".git"), 0o755))
	must(os.WriteFile(filepath.Join(root, ".gitignore"), []byte("*.log\nbuild/\n.env\n"), 0o644))
	must(os.WriteFile(filepath.Join(root, "README.md"), []byte("hi\n"), 0o644))
	must(os.WriteFile(filepath.Join(root, "ignored.log"), []byte("x\n"), 0o644))
	must(os.WriteFile(filepath.Join(root, ".env"), []byte("S=1\n"), 0o600))
	must(os.Mkdir(filepath.Join(root, "build"), 0o755))
	must(os.WriteFile(filepath.Join(root, "build", "out"), []byte("x\n"), 0o644))
	must(os.Mkdir(filepath.Join(root, "sub"), 0o755))
	for _, v := range []string{"WORKSPACE_ROOT", "CETA_EXTRA_READWRITE_ROOTS", "CETA_EXTRA_READONLY_ROOTS", "CETA_DENIED_ROOTS", "MONOREPO_ROOT"} {
		t.Setenv(v, "")
	}
	r, err := filepath.EvalSymlinks(root)
	must(err)
	return r
}

// TestClassifyInRepo: inside a git working tree the gitignore source
// decides — ignored paths are Deletable, everything else writable-only.
func TestClassifyInRepo(t *testing.T) {
	root := repoFixture(t)
	pe := patheval.NewWithCWD(root, root)
	cases := []struct {
		path string
		want Class
	}{
		{"README.md", Writable},
		{"ignored.log", Deletable},
		{"build", Deletable},
		{"build/out", Deletable},
		{"sub", Writable},
		{"sub/new.log", Deletable}, // not on disk; basename pattern still matches
		{".env", Deletable},        // the gitignore source says yes; the POLICY's secret check must win above it
		{".git", Protected},
		{".git/HEAD", Protected},
		{".worktrees/x", Protected},
		{root, Writable},
		{"/nix/store/x", NotWritable},
	}
	for _, c := range cases {
		got, reason := Classify(pe, c.path)
		if got != c.want {
			t.Errorf("Classify(%q) = %s (%s), want %s", c.path, got, reason, c.want)
		}
	}
}

// TestClassifyTemp: outside any repository a path under a temp root is
// Deletable, provided the zone model already allows writing there.
func TestClassifyTemp(t *testing.T) {
	dir := t.TempDir()
	if !temproot.Under(dir) {
		t.Skipf("t.TempDir() %s is not under a temproot.Roots entry on this machine", dir)
	}
	if _, inRepo := patheval.GitRoot(dir); inRepo {
		t.Skipf("t.TempDir() %s sits inside a git working tree; the temp source cannot be isolated", dir)
	}
	for _, v := range []string{"WORKSPACE_ROOT", "CETA_EXTRA_READWRITE_ROOTS", "CETA_EXTRA_READONLY_ROOTS", "CETA_DENIED_ROOTS", "MONOREPO_ROOT"} {
		t.Setenv(v, "")
	}
	// Use dir as the project root so the zone is writable wherever the
	// machine's $TMPDIR happens to be (patheval's built-in /tmp zone is
	// literal /tmp only).
	pe := patheval.NewWithCWD(dir, dir)
	got, reason := Classify(pe, filepath.Join(dir, "scratch.txt"))
	if got != Deletable {
		t.Fatalf("Classify(temp file) = %s (%s), want deletable", got, reason)
	}
}

// TestClassifyRepoUnderTempPrefersRepo: the innermost workspace wins — a
// tracked file inside a repository that itself sits under a temp root is
// NOT deletable, and an ignored one is (via gitignore, not via temp).
func TestClassifyRepoUnderTempPrefersRepo(t *testing.T) {
	root := repoFixture(t)
	if !temproot.Under(root) {
		t.Skipf("fixture %s is not under a temp root; precedence cannot be exercised here", root)
	}
	pe := patheval.NewWithCWD(root, root)
	if got, reason := Classify(pe, "README.md"); got != Writable {
		t.Errorf("tracked file in a repo under a temp root: got %s (%s), want writable", got, reason)
	}
	if got, reason := Classify(pe, "ignored.log"); got != Deletable || reason != "deletable per git workspace at "+root {
		t.Errorf("ignored file: got %s (%s), want deletable via the git kind", got, reason)
	}
}

// TestClassString pins the names policies embed in reasons.
func TestClassStringProtected(t *testing.T) {
	if got := Protected.String(); got != "protected" {
		t.Fatalf("got %q", got)
	}
}

// TestClassifyNilEvaluator: a nil evaluator is NotWritable, never Deletable.
func TestClassifyNilEvaluator(t *testing.T) {
	if got, _ := Classify(nil, "/tmp/x"); got != NotWritable {
		t.Fatalf("got %s, want not-writable", got)
	}
}

// TestClassString pins the names policies embed in reasons.
func TestClassString(t *testing.T) {
	for c, want := range map[Class]string{NotWritable: "not-writable", Writable: "writable", Deletable: "deletable", Class(99): "class-invalid"} {
		if got := c.String(); got != want {
			t.Errorf("%d.String() = %q, want %q", int(c), got, want)
		}
	}
}
