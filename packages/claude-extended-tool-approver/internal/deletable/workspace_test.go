package deletable

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/patheval"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/temproot"
)

// scratchOutsideTemp returns a fresh directory that is NOT under any temp
// root, NOT inside a git working tree, and NOT under $HOME/.cache — so a
// negative test ("the same relative path is not deletable without the
// marker") cannot be satisfied by an outer kind by accident. It lives under
// the real $HOME/.local/share (the same escape hatch patheval's TestMain
// uses to get out of literal /tmp) and is removed at cleanup. Skips when no
// such directory can be found.
func scratchOutsideTemp(t *testing.T) string {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no $HOME")
	}
	parent := filepath.Join(home, ".local", "share")
	if err := os.MkdirAll(parent, 0o700); err != nil {
		t.Skip(err)
	}
	dir, err := os.MkdirTemp(parent, "ceta-deletable-test-")
	if err != nil {
		t.Skip(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	dir = patheval.ResolveRealPath(dir)
	if temproot.Under(dir) {
		t.Skipf("%s is under a temp root", dir)
	}
	if _, in := patheval.GitRoot(dir); in {
		t.Skipf("%s is inside a git working tree", dir)
	}
	return dir
}

func clearZoneEnv(t *testing.T) {
	t.Helper()
	for _, v := range []string{"WORKSPACE_ROOT", "CETA_EXTRA_READWRITE_ROOTS", "CETA_EXTRA_READONLY_ROOTS", "CETA_DENIED_ROOTS", "MONOREPO_ROOT", "XDG_CACHE_HOME", "GOCACHE", "GOMODCACHE", "GOPATH"} {
		t.Setenv(v, "")
	}
}

func mkdirs(t *testing.T, root string, dirs ...string) {
	t.Helper()
	for _, d := range dirs {
		if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(d)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

func touch(t *testing.T, root string, files ...string) {
	t.Helper()
	for _, f := range files {
		p := filepath.Join(root, filepath.FromSlash(f))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// TestGradleKind: build/ and .gradle/ under a settings.gradle root are
// Deletable (and everything inside them); src/ is Silent; without the
// marker the same relative paths are Silent.
func TestGradleKind(t *testing.T) {
	clearZoneEnv(t)
	root := scratchOutsideTemp(t)
	mkdirs(t, root, "build/classes", ".gradle/caches", "src/main")
	touch(t, root, "settings.gradle", "build/classes/A.class", "src/main/A.java")
	for rel, want := range map[string]Category{
		"build": CatDeletable, "build/classes": CatDeletable, "build/classes/A.class": CatDeletable,
		".gradle": CatDeletable, ".gradle/caches": CatDeletable,
		"src": CatSilent, "src/main/A.java": CatSilent, "settings.gradle": CatSilent,
	} {
		res := Resolve([]Kind{gradleKind}, filepath.Join(root, filepath.FromSlash(rel)))
		if res.Category != want {
			t.Errorf("%s: got %s (kind %q), want %s", rel, res.Category, res.Kind, want)
		}
		if want != CatSilent && res.Kind != "gradle" {
			t.Errorf("%s: decided by %q, want gradle", rel, res.Kind)
		}
	}
	// Negative: remove the marker, same tree, nothing is deletable.
	if err := os.Remove(filepath.Join(root, "settings.gradle")); err != nil {
		t.Fatal(err)
	}
	if res := Resolve([]Kind{gradleKind}, filepath.Join(root, "build")); res.Category != CatSilent {
		t.Errorf("without marker: build is %s, want silent", res.Category)
	}
	// Kotlin DSL marker also identifies.
	touch(t, root, "settings.gradle.kts")
	if res := Resolve([]Kind{gradleKind}, filepath.Join(root, "build")); res.Category != CatDeletable {
		t.Errorf("settings.gradle.kts: build is %s, want deletable", res.Category)
	}
}

// TestGoKind: the module tree declares nothing; GOCACHE (env) and its
// XDG / $HOME defaults are Deletable roots; GOMODCACHE likewise.
func TestGoKind(t *testing.T) {
	clearZoneEnv(t)
	root := scratchOutsideTemp(t)
	mkdirs(t, root, "mod/internal", "gocache/aa", "modcache/x@v1", "xdg/go-build")
	touch(t, root, "mod/go.mod", "mod/internal/a.go")
	if res := Resolve([]Kind{goKind}, filepath.Join(root, "mod", "internal", "a.go")); res.Category != CatSilent {
		t.Errorf("module source: got %s, want silent", res.Category)
	}
	t.Setenv("GOCACHE", filepath.Join(root, "gocache"))
	t.Setenv("GOMODCACHE", filepath.Join(root, "modcache"))
	for _, rel := range []string{"gocache", "gocache/aa", "modcache/x@v1"} {
		res := Resolve([]Kind{goKind}, filepath.Join(root, rel))
		if res.Category != CatDeletable || res.Kind != "go" {
			t.Errorf("%s: got %s by %q, want deletable by go", rel, res.Category, res.Kind)
		}
	}
	// Default derivation without the env vars: $XDG_CACHE_HOME/go-build.
	t.Setenv("GOCACHE", "")
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "xdg"))
	if res := Resolve([]Kind{goKind}, filepath.Join(root, "xdg", "go-build")); res.Category != CatDeletable {
		t.Errorf("XDG default GOCACHE: got %s, want deletable", res.Category)
	}
	if res := Resolve([]Kind{goKind}, filepath.Join(root, "xdg")); res.Category != CatSilent {
		t.Errorf("XDG_CACHE_HOME itself is not go's: got %s, want silent", res.Category)
	}
}

// TestGoKindModCacheZoneConflict pins the documented interaction: the
// default GOMODCACHE (~/go/pkg/mod) is a Deletable declaration, but it sits
// in patheval's read-only `~/go/pkg` zone, and Classify (zone first) reports
// NotWritable — the declaration does not override the older zone decision.
func TestGoKindModCacheZoneConflict(t *testing.T) {
	clearZoneEnv(t)
	home := scratchOutsideTemp(t)
	t.Setenv("HOME", home)
	mkdirs(t, home, "go/pkg/mod/x@v1", "proj")
	touch(t, home, "proj/.git")
	target := filepath.Join(home, "go", "pkg", "mod", "x@v1")
	if res := Resolve([]Kind{goKind}, target); res.Category != CatDeletable {
		t.Fatalf("declaration: got %s, want deletable", res.Category)
	}
	pe := patheval.NewWithCWD(filepath.Join(home, "proj"), filepath.Join(home, "proj"))
	if got, reason := ClassifyWith(pe, DefaultKinds(), target); got != NotWritable {
		t.Errorf("Classify: got %s (%s), want not-writable (read-only zone wins)", got, reason)
	}
}

// TestHomeKind: ~/.cache is Deletable, ~/.ssh and ~/.gnupg Protected, the
// rest Silent; $XDG_CACHE_HOME elsewhere is a Deletable root.
func TestHomeKind(t *testing.T) {
	clearZoneEnv(t)
	scratch := scratchOutsideTemp(t)
	home := filepath.Join(scratch, "home")
	mkdirs(t, home, ".cache/x", ".ssh", ".gnupg", "docs")
	mkdirs(t, scratch, "xdgcache/y")
	t.Setenv("HOME", home)
	for rel, want := range map[string]Category{
		".cache": CatDeletable, ".cache/x": CatDeletable,
		".ssh": CatProtected, ".ssh/id_rsa": CatProtected, ".gnupg": CatProtected,
		"docs": CatSilent, ".": CatSilent,
	} {
		res := Resolve([]Kind{homeKind}, filepath.Join(home, filepath.FromSlash(rel)))
		if res.Category != want {
			t.Errorf("~/%s: got %s, want %s", rel, res.Category, want)
		}
	}
	t.Setenv("XDG_CACHE_HOME", filepath.Join(scratch, "xdgcache"))
	if res := Resolve([]Kind{homeKind}, filepath.Join(scratch, "xdgcache", "y")); res.Category != CatDeletable || res.Kind != "home" {
		t.Errorf("XDG_CACHE_HOME: got %s by %q, want deletable by home", res.Category, res.Kind)
	}
}

// TestPnKind: the workforests dir (default and configured) is Protected;
// everything else Silent; a `.worktrees` inside a member repo is Protected
// by the git kind.
func TestPnKind(t *testing.T) {
	clearZoneEnv(t)
	root := scratchOutsideTemp(t)
	mkdirs(t, root, ".workforests/feat/repo", "repo/.worktrees/x", "repo/src")
	touch(t, root, "repo/.git", "repo/src/a.go")
	if err := os.WriteFile(filepath.Join(root, "pn-workspace.toml"), []byte("[workspace]\nname = 'x'\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	kinds := []Kind{gitKind, pnKind}
	for rel, want := range map[string]struct {
		cat  Category
		kind string
	}{
		".workforests":           {CatProtected, "pn"},
		".workforests/feat":      {CatProtected, "pn"},
		".workforests/feat/repo": {CatProtected, "pn"},
		"repo/.worktrees":        {CatProtected, "git"},
		"repo/.worktrees/x":      {CatProtected, "git"},
		"repo/src/a.go":          {CatKeep, "git"},
		"pn-workspace.toml":      {CatSilent, ""},
	} {
		res := Resolve(kinds, filepath.Join(root, filepath.FromSlash(rel)))
		if res.Category != want.cat || res.Kind != want.kind {
			t.Errorf("%s: got %s by %q, want %s by %q", rel, res.Category, res.Kind, want.cat, want.kind)
		}
	}
	// Configured workforests_dir.
	if err := os.WriteFile(filepath.Join(root, "pn-workspace.toml"), []byte("[workspace]\nworkforests_dir = 'sets'\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mkdirs(t, root, "sets/feat")
	if res := Resolve(kinds, filepath.Join(root, "sets", "feat")); res.Category != CatProtected {
		t.Errorf("configured workforests_dir: got %s, want protected", res.Category)
	}
	if res := Resolve(kinds, filepath.Join(root, ".workforests", "feat")); res.Category != CatSilent {
		t.Errorf("default dir once reconfigured: got %s, want silent", res.Category)
	}
	if got := pnWorkforestsDir(filepath.Join(root, "nowhere")); got != ".workforests" {
		t.Errorf("missing toml default: %q", got)
	}
}

// TestPrecedence: gradle inside git inside temp — gradle decides build/,
// git decides the rest (Keep holds tracked files against temp; ignored
// paths are Deletable via git), an outer Protected wins over an inner
// Deletable, and a path under no kind is Silent.
func TestPrecedence(t *testing.T) {
	clearZoneEnv(t)
	root := t.TempDir() // under a temp root on this machine, by design of the test
	root = patheval.ResolveRealPath(root)
	if !temproot.Under(root) {
		t.Skipf("%s is not under a temp root", root)
	}
	mkdirs(t, root, "repo/gp/build", "repo/gp/src", "repo/.workforests/s", "repo/ignored-dir")
	touch(t, root, "repo/.git", "repo/gp/settings.gradle", "repo/gp/src/A.java", "repo/README.md", "repo/pn-workspace.toml")
	if err := os.WriteFile(filepath.Join(root, "repo", ".gitignore"), []byte("ignored-dir/\n.workforests/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	kinds := DefaultKinds()
	for rel, want := range map[string]struct {
		cat  Category
		kind string
	}{
		"repo/gp/build":       {CatDeletable, "gradle"},
		"repo/gp/src/A.java":  {CatKeep, "git"},
		"repo/README.md":      {CatKeep, "git"},
		"repo/ignored-dir":    {CatDeletable, "git"},
		"repo/.git":           {CatProtected, "git"},
		"repo/.workforests/s": {CatProtected, "pn"}, // gitignored (inner git says Deletable) but the pn kind protects: Protected wins
		"outside.txt":         {CatDeletable, "temp"},
	} {
		res := Resolve(kinds, filepath.Join(root, filepath.FromSlash(rel)))
		if res.Category != want.cat || res.Kind != want.kind {
			t.Errorf("%s: got %s by %q, want %s by %q", rel, res.Category, res.Kind, want.cat, want.kind)
		}
	}
	scratch := scratchOutsideTemp(t)
	if res := Resolve(kinds, filepath.Join(scratch, "x")); res.Category != CatSilent {
		t.Errorf("under no kind: got %s by %q, want silent", res.Category, res.Kind)
	}
}

// TestClassifyDeletableImpliesWritable: a zone-unknown path under a
// declared cache root is Deletable (the declaration vouches for it), while a
// zone-unknown path under no declaration is NotWritable.
func TestClassifyDeletableImpliesWritable(t *testing.T) {
	clearZoneEnv(t)
	scratch := scratchOutsideTemp(t)
	home := filepath.Join(scratch, "home")
	mkdirs(t, home, ".cache/tool", "docs", "proj")
	touch(t, home, "proj/.git")
	t.Setenv("HOME", home)
	pe := patheval.NewWithCWD(filepath.Join(home, "proj"), filepath.Join(home, "proj"))
	if pe.Evaluate(filepath.Join(home, ".cache", "tool")) != patheval.PathUnknown {
		t.Skip("fixture broken: ~/.cache is zoned on this machine")
	}
	if got, reason := ClassifyWith(pe, DefaultKinds(), filepath.Join(home, ".cache", "tool")); got != Deletable {
		t.Errorf("~/.cache/tool: got %s (%s), want deletable", got, reason)
	}
	if got, reason := ClassifyWith(pe, DefaultKinds(), filepath.Join(home, "docs")); got != NotWritable {
		t.Errorf("~/docs (unzoned, undeclared): got %s (%s), want not-writable", got, reason)
	}
	if got, reason := ClassifyWith(pe, DefaultKinds(), filepath.Join(home, ".ssh", "id_rsa")); got != Protected {
		t.Errorf("~/.ssh/id_rsa: got %s (%s), want protected", got, reason)
	}
}

// TestCategoryString pins the names embedded in reasons.
func TestCategoryString(t *testing.T) {
	for c, want := range map[Category]string{CatSilent: "silent", CatDeletable: "deletable", CatKeep: "keep", CatProtected: "protected", Category(9): "category-invalid"} {
		if got := c.String(); got != want {
			t.Errorf("%d: %q, want %q", int(c), got, want)
		}
	}
}
