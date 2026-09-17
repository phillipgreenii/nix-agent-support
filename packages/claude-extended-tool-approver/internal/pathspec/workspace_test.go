package pathspec

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/patheval"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/temproot"
)

// wantAccess is the ADR 0068 rewrite of this file's pre-existing
// Category/Resolution-based table-driven assertions (tc-mkpaz.1's packet
// text: "update these assertions to the new per-facet shape your resolution
// entry point returns, preserving what each test actually verifies"): it
// resolves path's DELETE facet via ResolveAccess — every original assertion
// in this file was a deletability question, since the pre-rename package
// only ever answered one — and checks both the exported AccessResult and,
// when wantReasonHas is non-empty, that the Reason names the deciding kind
// (Verdict carries no separate Kind/Root field, only Result and Reason; see
// workspace.go's Verdict doc comment). Pass wantReasonHas == "" for a
// genuinely undecided path (no candidate had an opinion at all — the old
// CatSilent/kind=="" case).
func wantAccess(t *testing.T, kinds []Kind, path string, want AccessResult, wantReasonHas string) {
	t.Helper()
	pa := ResolveAccess(kinds, path)
	if pa.Delete.Result != want {
		t.Errorf("%s: Delete = %s (%q), want %s", path, pa.Delete.Result, pa.Delete.Reason, want)
	}
	if wantReasonHas != "" && !strings.Contains(pa.Delete.Reason, wantReasonHas) {
		t.Errorf("%s: Delete reason %q does not name kind %q", path, pa.Delete.Reason, wantReasonHas)
	}
}

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
	for rel, want := range map[string]AccessResult{
		"build": Permitted, "build/classes": Permitted, "build/classes/A.class": Permitted,
		".gradle": Permitted, ".gradle/caches": Permitted,
		"src": Unknown, "src/main/A.java": Unknown, "settings.gradle": Unknown,
	} {
		wantKind := ""
		if want != Unknown {
			wantKind = "gradle"
		}
		wantAccess(t, []Kind{gradleKind}, filepath.Join(root, filepath.FromSlash(rel)), want, wantKind)
	}
	// Negative: remove the marker, same tree, nothing is deletable.
	if err := os.Remove(filepath.Join(root, "settings.gradle")); err != nil {
		t.Fatal(err)
	}
	wantAccess(t, []Kind{gradleKind}, filepath.Join(root, "build"), Unknown, "")
	// Kotlin DSL marker also identifies.
	touch(t, root, "settings.gradle.kts")
	wantAccess(t, []Kind{gradleKind}, filepath.Join(root, "build"), Permitted, "gradle")
}

// TestInsideMarkerWorkspace (slice 3x, tc-lc8f item 4e): a plain existence
// check over a kind's Markers, independent of what Resolve/Classify's
// CATEGORY machinery would say about the SAME path — the go module tree
// itself is Silent under goKind's own Rules-based categorize (TestGoKind
// above proves this), so Resolve alone could never confirm "this is a go
// workspace" for an ordinary source file; InsideMarkerWorkspace answers a
// different, narrower question (does a go.mod/.git ancestor exist at all).
func TestInsideMarkerWorkspace(t *testing.T) {
	root := scratchOutsideTemp(t)
	mkdirs(t, root, "mod/internal")
	touch(t, root, "mod/go.mod", "mod/internal/a.go")

	if !InsideMarkerWorkspace(DefaultKinds(), []string{"git", "go"}, filepath.Join(root, "mod", "internal", "a.go")) {
		t.Error("a source file under go.mod should be inside a go workspace")
	}
	if InsideMarkerWorkspace(DefaultKinds(), []string{"git", "go"}, root) {
		t.Error("root itself (no go.mod/.git ancestor) should NOT be inside a workspace")
	}
	// Naming only "git" excludes the go.mod match.
	if InsideMarkerWorkspace(DefaultKinds(), []string{"git"}, filepath.Join(root, "mod", "internal", "a.go")) {
		t.Error("naming only \"git\" must not match a go.mod-only tree")
	}
	// A .git sibling is found the same way.
	touch(t, root, "repo/.git")
	if !InsideMarkerWorkspace(DefaultKinds(), []string{"git", "go"}, filepath.Join(root, "repo")) {
		t.Error("a directory holding .git should be inside a git workspace")
	}
}

// TestGoKind: the module tree declares nothing; GOCACHE (env) and its
// XDG / $HOME defaults are Deletable roots; GOMODCACHE likewise.
func TestGoKind(t *testing.T) {
	clearZoneEnv(t)
	root := scratchOutsideTemp(t)
	mkdirs(t, root, "mod/internal", "gocache/aa", "modcache/x@v1", "xdg/go-build")
	touch(t, root, "mod/go.mod", "mod/internal/a.go")
	wantAccess(t, []Kind{goKind}, filepath.Join(root, "mod", "internal", "a.go"), Unknown, "")
	t.Setenv("GOCACHE", filepath.Join(root, "gocache"))
	t.Setenv("GOMODCACHE", filepath.Join(root, "modcache"))
	for _, rel := range []string{"gocache", "gocache/aa", "modcache/x@v1"} {
		wantAccess(t, []Kind{goKind}, filepath.Join(root, rel), Permitted, "go")
	}
	// Default derivation without GOCACHE: goKind.Roots' XDG_CACHE_HOME
	// branch is deliberately gated to non-darwin (runtime.GOOS !=
	// "darwin") because real `go env GOCACHE` on darwin ignores
	// XDG_CACHE_HOME entirely and always derives $HOME/Library/Caches/
	// go-build (verified empirically: `XDG_CACHE_HOME=/tmp/x go env
	// GOCACHE` on darwin still prints .../Library/Caches/go-build) — so
	// this assertion must be OS-aware rather than assuming XDG_CACHE_HOME
	// is honored everywhere.
	t.Setenv("GOCACHE", "")
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "xdg"))
	if runtime.GOOS == "darwin" {
		home := filepath.Join(root, "home")
		mkdirs(t, home, "Library/Caches/go-build")
		t.Setenv("HOME", home)
		wantAccess(t, []Kind{goKind}, filepath.Join(home, "Library", "Caches", "go-build"), Permitted, "go")
		wantAccess(t, []Kind{goKind}, filepath.Join(root, "xdg", "go-build"), Unknown, "")
	} else {
		wantAccess(t, []Kind{goKind}, filepath.Join(root, "xdg", "go-build"), Permitted, "go")
		wantAccess(t, []Kind{goKind}, filepath.Join(root, "xdg"), Unknown, "")
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
	wantAccess(t, []Kind{goKind}, target, Permitted, "go")
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
	for rel, want := range map[string]AccessResult{
		".cache": Permitted, ".cache/x": Permitted,
		".ssh": Forbidden, ".ssh/id_rsa": Forbidden, ".gnupg": Forbidden,
		"docs": Unknown, ".": Unknown,
	} {
		wantKind := ""
		if want != Unknown {
			wantKind = "home"
		}
		wantAccess(t, []Kind{homeKind}, filepath.Join(home, filepath.FromSlash(rel)), want, wantKind)
	}
	t.Setenv("XDG_CACHE_HOME", filepath.Join(scratch, "xdgcache"))
	wantAccess(t, []Kind{homeKind}, filepath.Join(scratch, "xdgcache", "y"), Permitted, "home")
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
		result AccessResult
		kind   string
	}{
		".workforests":           {Forbidden, "pn"},
		".workforests/feat":      {Forbidden, "pn"},
		".workforests/feat/repo": {Forbidden, "pn"},
		"repo/.worktrees":        {Forbidden, "git"},
		"repo/.worktrees/x":      {Forbidden, "git"},
		"repo/src/a.go":          {Unknown, "git"}, // holdKeep: needs consent, held against a hypothetical outer source
		"pn-workspace.toml":      {Unknown, ""},
	} {
		wantAccess(t, kinds, filepath.Join(root, filepath.FromSlash(rel)), want.result, want.kind)
	}
	// Configured workforests_dir.
	if err := os.WriteFile(filepath.Join(root, "pn-workspace.toml"), []byte("[workspace]\nworkforests_dir = 'sets'\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mkdirs(t, root, "sets/feat")
	wantAccess(t, kinds, filepath.Join(root, "sets", "feat"), Forbidden, "pn")
	wantAccess(t, kinds, filepath.Join(root, ".workforests", "feat"), Unknown, "")
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
		result AccessResult
		kind   string
	}{
		"repo/gp/build":       {Permitted, "gradle"},
		"repo/gp/src/A.java":  {Unknown, "git"}, // holdKeep: needs consent, held against the outer temp root
		"repo/README.md":      {Unknown, "git"}, // holdKeep: needs consent, held against the outer temp root
		"repo/ignored-dir":    {Permitted, "git"},
		"repo/.git":           {Forbidden, "git"},
		"repo/.workforests/s": {Forbidden, "pn"}, // gitignored (inner git says Permitted) but the pn kind forbids: Forbidden wins
		"outside.txt":         {Permitted, "temp"},
	} {
		wantAccess(t, kinds, filepath.Join(root, filepath.FromSlash(rel)), want.result, want.kind)
	}
	scratch := scratchOutsideTemp(t)
	wantAccess(t, kinds, filepath.Join(scratch, "x"), Unknown, "")
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

// TestAccessResultString pins the names embedded in reasons (ADR 0068
// rewrite of the pre-rename TestCategoryString — this package's own
// unexported holdKeep is included here since a package-internal test can
// spell it, unlike any caller outside this package: see AccessResult's doc
// comment).
func TestAccessResultString(t *testing.T) {
	for a, want := range map[AccessResult]string{Unknown: "unknown", Permitted: "permitted", Forbidden: "forbidden", holdKeep: "keep", AccessResult(99): "access-result-invalid"} {
		if got := a.String(); got != want {
			t.Errorf("%d: %q, want %q", int(a), got, want)
		}
	}
}
