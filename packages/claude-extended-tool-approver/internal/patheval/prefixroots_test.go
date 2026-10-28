package patheval

import (
	"os"
	"path/filepath"
	"testing"
)

// prefixFixture builds a real home directory (a hermetic temp dir) holding
// .cache, sets HOME and the prefix-roots env, and returns the evaluator and the
// resolved ~/.cache path.
func prefixFixture(t *testing.T, entries string) (*PathEvaluator, string) {
	t.Helper()
	home := resolveRefPath(t.TempDir())
	cache := filepath.Join(home, ".cache")
	if err := os.MkdirAll(cache, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("CETA_EXTRA_READONLY_ROOTS", "")
	t.Setenv("CETA_EXTRA_READWRITE_ROOTS", "")
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv(ExtraReadOnlyPrefixRootsEnv, entries)
	return NewWithCWD("/nonexistent-ceta-project", "/nonexistent-ceta-project"), cache
}

func mustMkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
}

func cacheEntries(home string) string {
	c := home + "/.cache/"
	return c + "p2-decomp-*:" + c + "pdec-*:" + c + "pd-*"
}

// TestPrefixRoots_GrantsNamedChildrenReadOnly: the three ruled groups are readable
// (never writable), for the directory, a nested file and a not-yet-existing file.
func TestPrefixRoots_GrantsNamedChildrenReadOnly(t *testing.T) {
	_, cache := prefixFixture(t, "")
	t.Setenv(ExtraReadOnlyPrefixRootsEnv, cacheEntries(filepath.Dir(cache)))
	pe := NewWithCWD("/nonexistent-ceta-project", "/nonexistent-ceta-project")

	for _, child := range []string{"p2-decomp-abc", "pdec-1", "pd-xyz.2_x"} {
		mustMkdir(t, filepath.Join(cache, child, "sub"))
		for _, p := range []string{
			filepath.Join(cache, child),
			filepath.Join(cache, child, "sub"),
			filepath.Join(cache, child, "sub", "not-yet-created.txt"),
		} {
			got := pe.Evaluate(p)
			if !got.CanRead() || got.CanWrite() {
				t.Errorf("%s: read=%v write=%v, want read-only", p, got.CanRead(), got.CanWrite())
			}
		}
	}
}

// TestPrefixRoots_NoWholeParentAndNoLookalikes pins the acceptance criterion that
// the bare parent is NOT granted, and the shape rule's refusals.
func TestPrefixRoots_NoWholeParentAndNoLookalikes(t *testing.T) {
	_, cache := prefixFixture(t, "")
	home := filepath.Dir(cache)
	t.Setenv(ExtraReadOnlyPrefixRootsEnv, cacheEntries(home))
	pe := NewWithCWD("/nonexistent-ceta-project", "/nonexistent-ceta-project")

	for _, p := range []string{
		cache,                                      // the bare parent: no whole-~/.cache grant
		filepath.Join(cache, "other", "f"),         // an unnamed sibling
		filepath.Join(cache, "pd"),                 // prefix without the dash
		filepath.Join(cache, "pd", "f"),            //
		filepath.Join(cache, "pd-"),                // the bare prefix is not a child
		filepath.Join(cache, "pd-", "f"),           //
		filepath.Join(cache, "pdx-foo", "f"),       // lookalike prefix
		filepath.Join(cache, "xpd-foo", "f"),       // prefix not at the start
		filepath.Join(cache, "pd-a b", "f"),        // outside the safe name charset
		filepath.Join(cache, "pd-a:b", "f"),        //
		filepath.Join(cache, "pd-ok", "..", "sec"), // `..` cleans to a sibling
		filepath.Join(cache, "pd-ok", "..", "..", ".ssh", "id"),
		filepath.Join(home, ".cache2", "pd-x", "f"), // a sibling of the parent
		filepath.Join(home, "pd-x", "f"),            // a different parent
		filepath.Join(cache, "nested", "pd-x", "f"), // not a DIRECT child
	} {
		if got := pe.Evaluate(p); got.CanRead() {
			t.Errorf("%s was granted (%v), want unknown", p, got)
		}
	}
}

// TestPrefixRoots_SymlinkEscapeNotMatched: a name that matches but resolves
// elsewhere is judged by where it lands, not by its name, in both directions.
func TestPrefixRoots_SymlinkEscapeNotMatched(t *testing.T) {
	_, cache := prefixFixture(t, "")
	home := filepath.Dir(cache)
	t.Setenv(ExtraReadOnlyPrefixRootsEnv, cacheEntries(home))
	pe := NewWithCWD("/nonexistent-ceta-project", "/nonexistent-ceta-project")

	outside := filepath.Join(home, "outside")
	mustMkdir(t, outside)
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	// pd-evil -> outside: the matching NAME must not grant what it points at.
	if err := os.Symlink(outside, filepath.Join(cache, "pd-evil")); err != nil {
		t.Fatal(err)
	}
	// pd-ok/link -> outside: a link INSIDE a granted child must not extend the grant.
	mustMkdir(t, filepath.Join(cache, "pd-ok"))
	if err := os.Symlink(outside, filepath.Join(cache, "pd-ok", "link")); err != nil {
		t.Fatal(err)
	}
	// plain -> pd-ok: a non-matching name pointing INTO a granted child resolves to
	// the granted child, which is the correct (granted) answer.
	if err := os.Symlink(filepath.Join(cache, "pd-ok"), filepath.Join(cache, "plain")); err != nil {
		t.Fatal(err)
	}

	for _, p := range []string{
		filepath.Join(cache, "pd-evil", "secret"),
		filepath.Join(cache, "pd-ok", "link", "secret"),
	} {
		if got := pe.Evaluate(p); got.CanRead() {
			t.Errorf("%s was granted through a symlink escape (%v)", p, got)
		}
	}
	if got := pe.Evaluate(filepath.Join(cache, "plain", "f")); !got.CanRead() {
		t.Errorf("a symlink resolving into a granted child must be readable, got %v", got)
	}
}

// TestPrefixRoots_SymlinkedParentIsResolved: when the parent itself is a symlink
// the grant follows its real location, so the resolved path still matches.
func TestPrefixRoots_SymlinkedParentIsResolved(t *testing.T) {
	home := resolveRefPath(t.TempDir())
	real := filepath.Join(home, "real-cache")
	mustMkdir(t, filepath.Join(real, "pd-1"))
	if err := os.Symlink(real, filepath.Join(home, ".cache")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("CETA_EXTRA_READONLY_ROOTS", "")
	t.Setenv(ExtraReadOnlyPrefixRootsEnv, home+"/.cache/pd-*")
	pe := NewWithCWD("/nonexistent-ceta-project", "/nonexistent-ceta-project")

	if got := pe.Evaluate(home + "/.cache/pd-1/f"); !got.CanRead() {
		t.Errorf("a path through the symlinked parent must be readable, got %v", got)
	}
	if got := pe.Evaluate(home + "/.cache/other/f"); got.CanRead() {
		t.Errorf("an unnamed sibling must stay unknown, got %v", got)
	}
}

// TestPrefixRoots_TmpdirResolvedAtRuntime: `$TMPDIR/pd-*` follows the process's own
// TMPDIR when the evaluator is built, and grants none of the bare TMPDIR.
func TestPrefixRoots_TmpdirResolvedAtRuntime(t *testing.T) {
	build := func(tmpdir string) *PathEvaluator {
		t.Setenv("HOME", "/home/testuser")
		t.Setenv("CETA_EXTRA_READONLY_ROOTS", "")
		t.Setenv("TMPDIR", tmpdir)
		t.Setenv(ExtraReadOnlyPrefixRootsEnv, "$TMPDIR/pd-*")
		return NewWithCWD("/home/testuser/project", "/home/testuser/project")
	}
	const a = "/private/var/folders/zz/user-a/T"
	pe := build(a + "/")
	if got := pe.Evaluate(a + "/pd-123/log"); !got.CanRead() || got.CanWrite() {
		t.Errorf("TMPDIR pd-* child: read=%v write=%v, want read-only", got.CanRead(), got.CanWrite())
	}
	for _, p := range []string{a, a + "/other/f", a + "/pd", a + "/pdx-1/f", a + "/pd-1/../x"} {
		if got := pe.Evaluate(p); got.CanRead() {
			t.Errorf("%s was granted (%v), want unknown", p, got)
		}
	}
	// The same configuration under a different TMPDIR grants THAT directory only.
	const b = "/private/var/folders/zz/user-b/T"
	pe = build(b)
	if !pe.Evaluate(b+"/pd-1/f").CanRead() || pe.Evaluate(a+"/pd-123/log").CanRead() {
		t.Errorf("the grant must follow the process's TMPDIR, not a baked-in path")
	}
	// An unusable TMPDIR grants nothing.
	for _, bad := range []string{"", "/", "/etc", "/home/testuser", "/private/var/folders", "relative/tmp"} {
		if roots := build(bad).ExtraReadOnlyPrefixRoots(); len(roots) != 0 {
			t.Errorf("TMPDIR=%q yielded roots %v, want none", bad, roots)
		}
	}
}

// TestParsePrefixRoot_ShapeRule is the table for the entry shape rule.
func TestParsePrefixRoot_ShapeRule(t *testing.T) {
	t.Setenv("TMPDIR", "/private/var/folders/zz/user/T/")
	home := resolveRefPath(t.TempDir())
	under := filepath.Join(home, ".cache")
	mustMkdir(t, under)
	accept := map[string]PrefixRoot{
		under + "/pd-*":         {under, "pd-"},
		under + "/p2-decomp-*":  {under, "p2-decomp-"},
		under + "/a.b_c-*":      {under, "a.b_c-"},
		under + "/deep/er/pd-*": {under + "/deep/er", "pd-"},
		"$TMPDIR/pd-*":          {"/private/var/folders/zz/user/T", "pd-"},
		"/srv/data/Run-1-*":     {resolveRefPath("/srv/data"), "Run-1-"},
	}
	for entry, want := range accept {
		got, ok := parsePrefixRoot(entry, home)
		if !ok || got != want {
			t.Errorf("%q: got %+v ok=%v, want %+v", entry, got, ok, want)
		}
	}
	reject := []string{
		"",
		"pd-*",                       // no parent
		"/pd-*",                      // parent is /
		under + "/pd-",               // no star
		under + "/pd*",               // no dash before the star
		under + "/*",                 // whole parent
		under + "/-*",                // prefix must start alphanumeric
		under + "/.pd-*",             // dot-leading prefix
		under + "/pd-*x",             // star not trailing
		under + "/p*d-*",             // two stars
		under + "/pd-?*",             // metacharacter
		under + "/pd-[a]*",           // metacharacter
		under + "/pd-a b-*",          // space
		home + "/*/pd-*",             // glob in parent
		under + "/../pd-*",           // dot-dot
		home + "/./.cache/pd-*",      // dot segment
		home + "//.cache/pd-*",       // not clean
		"relative/.cache/pd-*",       // not absolute
		"~/.cache/pd-*",              // tilde is not expanded
		"$HOME/.cache/pd-*",          // only $TMPDIR is a token
		"$TMPDIR/sub/pd-*",           // the token is the whole parent
		home + "/pd-*",               // parent is $HOME itself
		filepath.Dir(home) + "/pd-*", // parent above $HOME
		home + "/.claude/pd-*",       // secret-adjacent
		home + "/.claude/projects/pd-*",
	}
	for _, entry := range reject {
		if got, ok := parsePrefixRoot(entry, home); ok {
			t.Errorf("%q was accepted as %+v, want rejected", entry, got)
		}
	}
	t.Setenv("TMPDIR", "")
	if got, ok := parsePrefixRoot("$TMPDIR/pd-*", home); ok {
		t.Errorf("$TMPDIR with TMPDIR unset was accepted as %+v", got)
	}
}

// TestPrefixRoots_ExactRootsUnchanged: the exact-path env var still behaves as before
// and a prefix entry does not leak into the exact roots.
func TestPrefixRoots_ExactRootsUnchanged(t *testing.T) {
	_, cache := prefixFixture(t, "")
	home := filepath.Dir(cache)
	exact := filepath.Join(home, "exact-root")
	mustMkdir(t, exact)
	t.Setenv("CETA_EXTRA_READONLY_ROOTS", exact)
	t.Setenv(ExtraReadOnlyPrefixRootsEnv, home+"/.cache/pd-*")
	pe := NewWithCWD("/nonexistent-ceta-project", "/nonexistent-ceta-project")

	if got := pe.Evaluate(filepath.Join(exact, "f")); !got.CanRead() || got.CanWrite() {
		t.Errorf("exact root: read=%v write=%v, want read-only", got.CanRead(), got.CanWrite())
	}
	if roots := pe.ExtraReadOnlyRoots(); len(roots) != 1 || roots[0] != exact {
		t.Errorf("ExtraReadOnlyRoots() = %v, want only %s", roots, exact)
	}
}
