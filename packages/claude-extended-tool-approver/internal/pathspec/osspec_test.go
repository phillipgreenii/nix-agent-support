package pathspec

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/patheval"
)

// wantFacets asserts path's Read/Write/Delete AccessResult against kinds via
// ResolveAccess — unlike workspace_test.go's wantAccess (Delete only, the
// pre-ADR-0068 single-question shape), this packet's zones state opinions
// on multiple facets at once, so tests need to check all three.
func wantFacets(t *testing.T, kinds []Kind, path string, wantRead, wantWrite, wantDelete AccessResult) {
	t.Helper()
	pa := ResolveAccess(kinds, path)
	if pa.Read.Result != wantRead {
		t.Errorf("%s: Read = %s (%q), want %s", path, pa.Read.Result, pa.Read.Reason, wantRead)
	}
	if pa.Write.Result != wantWrite {
		t.Errorf("%s: Write = %s (%q), want %s", path, pa.Write.Result, pa.Write.Reason, wantWrite)
	}
	if pa.Delete.Result != wantDelete {
		t.Errorf("%s: Delete = %s (%q), want %s", path, pa.Delete.Result, pa.Delete.Reason, wantDelete)
	}
}

// homeScratch returns a fresh, resolved scratch directory suitable for
// t.Setenv("HOME", ...) — mirrors workspace_test.go's scratchOutsideTemp,
// minus the temp/git exclusion (irrelevant here: these tests pass an
// explicit []Kind that never includes tempKind/gitKind, so there is nothing
// for an outer kind to accidentally satisfy the assertion with).
func homeScratch(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "ceta-osspec-home-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	dir = patheval.ResolveRealPath(dir)
	if dir == "" {
		t.Skip("cannot resolve scratch home")
	}
	return dir
}

// TestOSKinds: OSKinds() returns exactly the five Kind values this file
// declares.
func TestOSKinds(t *testing.T) {
	got := OSKinds()
	if len(got) != 5 {
		t.Fatalf("OSKinds() returned %d kinds, want 5", len(got))
	}
}

// TestOSTmpKind: zone 1 — /tmp is Read/Write per osTmpKind; combined with
// workspace.go's tempKind (already landed by P1), it ALSO states its
// existing Delete: Permitted opinion — both facets are checked, not just
// the new one (this packet's acceptance criteria).
func TestOSTmpKind(t *testing.T) {
	clearZoneEnv(t)
	dir, err := os.MkdirTemp("/tmp", "ceta-osspec-tmp-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	dir = patheval.ResolveRealPath(dir)
	if dir == "" {
		t.Skip("cannot resolve /tmp scratch dir")
	}
	wantFacets(t, []Kind{osTmpKind}, dir, Permitted, Permitted, Unknown)
	wantFacets(t, []Kind{osTmpKind, tempKind}, dir, Permitted, Permitted, Permitted)
}

// TestOSNixKind: zone 2 — /nix is Read: Permitted, Write: Forbidden,
// Delete: Forbidden ("immutable path owned by nix-daemon").
func TestOSNixKind(t *testing.T) {
	clearZoneEnv(t)
	if _, err := os.Stat("/nix"); err != nil {
		t.Skip("no /nix on this machine")
	}
	wantFacets(t, []Kind{osNixKind}, "/nix/store", Permitted, Forbidden, Forbidden)
}

// TestOSHomeKindClaude: zones 3-4 — ~/.claude is ReadOnly except its
// plans/projects carve-out (Write: Permitted); ~/.claude.json is ReadOnly.
// Every facet's Delete stays Unknown (osHomeKind never declares an opinion
// on Delete — that is workspace.go's homeKind's job, unchanged by this
// packet).
func TestOSHomeKindClaude(t *testing.T) {
	clearZoneEnv(t)
	home := homeScratch(t)
	mkdirs(t, home, ".claude/plans", ".claude/projects", ".claude/settings")
	touch(t, home, ".claude.json", ".claude/settings/x.json", ".claude/plans/p.md", ".claude/projects/proj/notes.md")
	t.Setenv("HOME", home)
	kinds := []Kind{osHomeKind}
	cases := []struct {
		rel                 string
		read, write, delete AccessResult
	}{
		{".claude", Permitted, Forbidden, Unknown},
		{".claude/settings/x.json", Permitted, Forbidden, Unknown},
		{".claude/plans", Permitted, Permitted, Unknown},
		{".claude/plans/p.md", Permitted, Permitted, Unknown},
		{".claude/projects", Permitted, Permitted, Unknown},
		{".claude/projects/proj/notes.md", Permitted, Permitted, Unknown},
		{".claude.json", Permitted, Forbidden, Unknown},
		{"docs", Unknown, Unknown, Unknown},
	}
	for _, c := range cases {
		wantFacets(t, kinds, filepath.Join(home, filepath.FromSlash(c.rel)), c.read, c.write, c.delete)
	}
}

// TestOSHomeKindGoPkgException: zone 6 — regression test for the named
// ~/go/pkg exception (osspec.go's "The ~/go/pkg exception" doc comment).
// ~/go/pkg itself (and a sibling not under GOMODCACHE) must have Delete
// EXPLICITLY NOT Forbidden — asserted both as "!= Forbidden" and as the
// expected "== Unknown" — and combined with goKind's deeper GOMODCACHE
// RootDecl (Delete: Permitted), the deeper opinion must win: if osHomeKind
// declared Delete: Forbidden here instead, updateFacet's "Forbidden wins
// outright regardless of depth" rule would override GOMODCACHE's Permitted,
// silently reintroducing the exact bug ADR 0068 exists to fix.
func TestOSHomeKindGoPkgException(t *testing.T) {
	clearZoneEnv(t)
	home := homeScratch(t)
	mkdirs(t, home, "go/pkg/mod/example.com/pkg", "go/pkg/other")
	t.Setenv("HOME", home)
	kinds := []Kind{osHomeKind, goKind}

	for _, rel := range []string{"go/pkg", "go/pkg/other"} {
		pa := ResolveAccess(kinds, filepath.Join(home, filepath.FromSlash(rel)))
		if pa.Delete.Result == Forbidden {
			t.Errorf("%s: Delete = Forbidden, want anything but Forbidden (the named ~/go/pkg exception)", rel)
		}
		if pa.Delete.Result != Unknown {
			t.Errorf("%s: Delete = %s, want Unknown", rel, pa.Delete.Result)
		}
		if pa.Read.Result != Permitted {
			t.Errorf("%s: Read = %s, want Permitted", rel, pa.Read.Result)
		}
		if pa.Write.Result != Forbidden {
			t.Errorf("%s: Write = %s, want Forbidden", rel, pa.Write.Result)
		}
	}

	// GOMODCACHE's own Delete: Permitted (goKind, deeper root) wins over the
	// outer ~/go/pkg's Delete: Unknown — the exact fix this ADR delivers.
	wantFacets(t, kinds, filepath.Join(home, "go", "pkg", "mod", "example.com", "pkg"), Permitted, Forbidden, Permitted)
}

// TestOSGradleCacheKind: zone 5 — the Gradle user cache (GRADLE_USER_HOME
// or ~/.gradle) is ReadOnly. Distinct from workspace.go's gradleKind (a
// per-project settings.gradle marker Kind, unaffected by this packet).
func TestOSGradleCacheKind(t *testing.T) {
	clearZoneEnv(t)
	t.Setenv("GRADLE_USER_HOME", "")
	home := homeScratch(t)
	mkdirs(t, home, ".gradle/caches")
	t.Setenv("HOME", home)
	wantFacets(t, []Kind{osGradleCacheKind}, filepath.Join(home, ".gradle", "caches"), Permitted, Forbidden, Unknown)

	// Explicit GRADLE_USER_HOME overrides the ~/.gradle default.
	custom := homeScratch(t)
	t.Setenv("GRADLE_USER_HOME", custom)
	wantFacets(t, []Kind{osGradleCacheKind}, custom, Permitted, Forbidden, Unknown)
}

// TestOSXDGDataKind: zones 7-10 — the four <xdgDataHome> subpaths:
// nix-support-local-plugins/ (ReadOnly), contained-claude/ (ReadOnly — the
// correction osspec.go's header documents), claude-extended-tool-approver/
// (ReadWrite, the tool's own asks.db), claude-pretool-hook/ (ReadOnly).
func TestOSXDGDataKind(t *testing.T) {
	clearZoneEnv(t)
	xdg := homeScratch(t)
	mkdirs(t, xdg, "nix-support-local-plugins", "contained-claude", "claude-extended-tool-approver", "claude-pretool-hook")
	t.Setenv("XDG_DATA_HOME", xdg)
	kinds := []Kind{osXDGDataKind}
	cases := []struct {
		sub                 string
		read, write, delete AccessResult
	}{
		{"nix-support-local-plugins", Permitted, Forbidden, Unknown},
		{"contained-claude", Permitted, Forbidden, Unknown},
		{"claude-extended-tool-approver", Permitted, Permitted, Unknown},
		{"claude-pretool-hook", Permitted, Forbidden, Unknown},
	}
	for _, c := range cases {
		wantFacets(t, kinds, filepath.Join(xdg, c.sub), c.read, c.write, c.delete)
	}
}

// TestOSXDGDataKindDefaultFallback: XDG_DATA_HOME unset falls back to
// ~/.local/share, matching patheval.New's own derivation.
func TestOSXDGDataKindDefaultFallback(t *testing.T) {
	clearZoneEnv(t)
	t.Setenv("XDG_DATA_HOME", "")
	home := homeScratch(t)
	mkdirs(t, home, ".local/share/claude-extended-tool-approver")
	t.Setenv("HOME", home)
	wantFacets(t, []Kind{osXDGDataKind}, filepath.Join(home, ".local", "share", "claude-extended-tool-approver"), Permitted, Permitted, Unknown)
}
