package pathspec

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/x/gitclient"
	"github.com/phillipgreenii/x/gitfixture"
	"github.com/phillipgreenii/x/gittest"
)

// ignoreFixture is the one fixture both TestIgnoredTable and
// TestGitignoreAgainstGit use: it exercises every construct the matcher's
// documented subset (gitignore.go's header) claims — root and nested
// .gitignore files, .git/info/exclude, negation, dir-only, anchored,
// basename-at-any-depth, `**` in all three positions, character classes,
// escaped `#`/`!`, trailing-space trimming, and the excluded-parent rule.
//
// files lists the files to create (directories are created as needed); a
// trailing "/" creates an empty directory instead.
var ignoreFixture = struct {
	root, exclude string
	nested        map[string]string
	files         []string
}{
	root: strings.Join([]string{
		"# comment",
		"",
		"*.log",
		"!keep.log",
		"build/",
		"/top-only.txt",
		"docs/*.tmp",
		"**/generated",
		"cache/**",
		"a/**/b.txt",
		"\\#literal-hash",
		"\\!literal-bang",
		"trailing-space   ",
		"[cd]lass.o",
		"escaped\\ space.txt",
	}, "\n") + "\n",
	exclude: "local-only.txt\n",
	nested: map[string]string{
		"sub/.gitignore":        "*.nested\n!important.nested\n",
		"sub/deeper/.gitignore": "important.nested\n",
	},
	files: []string{
		"x.log", "keep.log", "sub/y.log",
		"build/", "build/out.o", "src/build/", "src/build/x", "build.txt",
		"top-only.txt", "sub/top-only.txt",
		"docs/a.tmp", "docs/inner/b.tmp",
		"generated/", "generated/g.go", "pkg/generated/", "pkg/generated/g.go",
		"cache/", "cache/c1", "cache/deep/c2",
		"a/b.txt", "a/x/b.txt", "a/x/y/b.txt", "a/b/c.txt",
		"#literal-hash", "!literal-bang", "trailing-space",
		"class.o", "dlass.o", "xlass.o",
		"escaped space.txt",
		"local-only.txt",
		"sub/f.nested", "sub/important.nested", "sub/deeper/f.nested", "sub/deeper/important.nested",
		"README.md", "sub/README.md",
		"missing-build", // a file (not dir) named like a dir-only pattern
	},
}

// wantIgnored is the expected verdict for each fixture path, hand-derived
// from gitignore(5) and confirmed by TestGitignoreAgainstGit against git.
var wantIgnored = map[string]bool{
	"x.log": true, "keep.log": false, "sub/y.log": true,
	"build": true, "build/out.o": true, "src/build": true, "src/build/x": true, "build.txt": false,
	"top-only.txt": true, "sub/top-only.txt": false,
	"docs/a.tmp": true, "docs/inner/b.tmp": false,
	"generated": true, "generated/g.go": true, "pkg/generated": true, "pkg/generated/g.go": true,
	"cache": false, "cache/c1": true, "cache/deep/c2": true,
	"a/b.txt": true, "a/x/b.txt": true, "a/x/y/b.txt": true, "a/b/c.txt": false,
	"#literal-hash": true, "!literal-bang": true, "trailing-space": true,
	"class.o": true, "dlass.o": true, "xlass.o": false,
	"escaped space.txt": true,
	"local-only.txt":    true,
	"sub/f.nested":      true, "sub/important.nested": false,
	"sub/deeper/f.nested": true, "sub/deeper/important.nested": true,
	"README.md": false, "sub/README.md": false,
	"missing-build": false,
	// Not created on disk: a dir-only pattern must not match a missing path
	// (the leading component is still treated as a directory, as git does).
	"src2/build": false,
	// Not created on disk, but a basename pattern needs no stat.
	"src2/z.log": true,
}

// buildIgnoreFixture populates root with the ignoreFixture tree (including
// .git/info/exclude, created if absent) and returns its symlink-resolved path.
func buildIgnoreFixture(t *testing.T, root string) string {
	t.Helper()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(root, ".git", "info"), 0o755))
	must(os.WriteFile(filepath.Join(root, ".git", "info", "exclude"), []byte(ignoreFixture.exclude), 0o644))
	must(os.WriteFile(filepath.Join(root, ".gitignore"), []byte(ignoreFixture.root), 0o644))
	for _, f := range ignoreFixture.files {
		p := filepath.Join(root, filepath.FromSlash(f))
		if strings.HasSuffix(f, "/") {
			must(os.MkdirAll(p, 0o755))
			continue
		}
		must(os.MkdirAll(filepath.Dir(p), 0o755))
		must(os.WriteFile(p, []byte("x\n"), 0o644))
	}
	for rel, content := range ignoreFixture.nested {
		p := filepath.Join(root, filepath.FromSlash(rel))
		must(os.MkdirAll(filepath.Dir(p), 0o755))
		must(os.WriteFile(p, []byte(content), 0o644))
	}
	r, err := filepath.EvalSymlinks(root)
	must(err)
	return r
}

// TestIgnoredTable pins the matcher's own verdicts on the fixture.
func TestIgnoredTable(t *testing.T) {
	root := buildIgnoreFixture(t, t.TempDir())
	for rel, want := range wantIgnored {
		if got := Ignored(root, filepath.Join(root, filepath.FromSlash(rel))); got != want {
			t.Errorf("Ignored(%q) = %v, want %v", rel, got, want)
		}
	}
	if Ignored(root, root) {
		t.Error("the root itself must never be ignored")
	}
	if Ignored(root, filepath.Dir(root)) {
		t.Error("a path outside the root must never be ignored")
	}
}

// TestGitignoreAgainstGit verifies the matcher's verdict for every fixture
// path against the real `git check-ignore` on an x/gittest repository holding
// the fixture. Because git's `.git/info/exclude` is created by `git init`, the
// fixture (which writes it) is populated AFTER the repository exists.
func TestGitignoreAgainstGit(t *testing.T) {
	repo := gittest.New(t, gitfixture.RepoOptions{})
	root := buildIgnoreFixture(t, repo.Dir)
	git := func(args ...string) (bool, string) {
		out, err := repo.Client.Run(t.Context(), append([]string{"-C", root, "-c", "core.excludesFile=/dev/null"}, args...)...)
		if err == nil {
			return true, string(out)
		}
		var ge *gitclient.GitError
		if errors.As(err, &ge) && ge.ExitCode == 1 {
			return false, string(out)
		}
		t.Fatalf("git %v: %v\n%s", args, err, out)
		return false, ""
	}
	for rel := range wantIgnored {
		gitSays, out := git("check-ignore", "-q", "--no-index", rel)
		ours := Ignored(root, filepath.Join(root, filepath.FromSlash(rel)))
		if gitSays != ours {
			t.Errorf("%q: git check-ignore says %v, matcher says %v (%s)", rel, gitSays, ours, strings.TrimSpace(out))
		}
	}
}

// TestParseIgnoreLine pins the per-line parse for the escape and
// trailing-space corners that are easy to get subtly wrong.
func TestParseIgnoreLine(t *testing.T) {
	cases := []struct {
		line    string
		ok      bool
		pattern string
		negate  bool
		dirOnly bool
		anchor  bool
	}{
		{"", false, "", false, false, false},
		{"# c", false, "", false, false, false},
		{"   ", false, "", false, false, false},
		{"*.log", true, "*.log", false, false, false},
		{"!keep", true, "keep", true, false, false},
		{"build/", true, "build", false, true, false},
		{"/top", true, "top", false, false, true},
		{"a/b", true, "a/b", false, false, true},
		{"\\#x", true, "#x", false, false, false},
		{"\\!x", true, "!x", false, false, false},
		{"x   ", true, "x", false, false, false},
		{"x\\ ", true, "x ", false, false, false},
		{"/", false, "", false, false, false},
	}
	for _, c := range cases {
		r, ok := parseIgnoreLine(c.line, "")
		if ok != c.ok {
			t.Errorf("%q: ok = %v, want %v", c.line, ok, c.ok)
			continue
		}
		if !ok {
			continue
		}
		if r.pattern != c.pattern || r.negate != c.negate || r.dirOnly != c.dirOnly || r.anchored != c.anchor {
			t.Errorf("%q: got %+v", c.line, r)
		}
	}
}
