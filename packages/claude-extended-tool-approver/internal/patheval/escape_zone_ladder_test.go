// escape_zone_ladder_test.go — pg2-l8esk.
//
// Generalizes TestBothZoneLaddersHonourTheGuard (fabricated_root_zone_test.go), which
// pinned agreement between Evaluate's two zone-ladder copies for ONLY the project-root
// zone, to EVERY zone the ladder recognizes.
//
// pg2-l8esk collapsed Evaluate's two independent ladder copies (one inline, one in a
// since-removed classifyWithoutEscapeCheck) into a single shared classify method, so
// there is no second implementation left to compare classify against — that specific
// drift is now structurally impossible. What this file instead pins is the ESCAPE
// BRANCH'S USE of that one ladder: Evaluate documents an asymmetric rule for a symlink
// that appears to be inside the project but resolves outside it —
//
//	escape to a zone AT LEAST as permissive as the project (ReadWrite), or to no zone
//	at all (Unknown), is BLOCKED (Evaluate returns PathUnknown);
//	escape to a LESS permissive zone (ReadOnly / Reject) is ALLOWED, using that zone's
//	own classification.
//
// pg2-byh62's regression was invisible to every existing unit test because an ordinary
// read never reaches that branch — only a symlink escaping the project does. This file
// drives an ACTUAL symlink escape into every zone classify recognizes (not just the
// project-root zone) and asserts the rule above holds for each. It also caught a real,
// pre-existing instance of the exact drift this bead is about: before the collapse, the
// two ladder copies disagreed on the <xdgDataHome>/claude-extended-tool-approver zone
// (ReadWrite vs ReadOnly — see "xdg-claude-extended-tool-approver" below), which meant
// an escape into that zone was wrongly ALLOWED instead of blocked.
//
// Every subtest is fully hermetic: isolateEnv clears every env var New/NewWithCWD
// reads (including XDG_DATA_HOME) so no subtest ever consults the real one, HOME is
// pinned to a fresh t.TempDir() every time, and no subtest writes anywhere outside a
// t.TempDir() except "tmp-root", which uses a self-removing os.MkdirTemp("/tmp", ...)
// marker (required because the escape check needs the symlink target to FULLY resolve
// on disk — see TestPathEvaluator_BrokenSymlink: a dangling target never reaches the
// escape check at all, it is treated as a broken symlink and returns PathUnknown before
// the branch is even considered).
//
// The project-root zone itself is deliberately ABSENT from this corpus: escaping INTO
// the project root is a contradiction (the escape check triggers precisely when the
// resolved target is NOT under the project root), so there is no case to construct.
package patheval

import (
	"os"
	"path/filepath"
	"testing"
)

// isolateEnv clears every env var New/NewWithCWD read, so each subtest below starts
// from a known-empty baseline and only the var(s) a case explicitly sets are non-empty.
// Without this, an ambient WORKSPACE_ROOT, GRADLE_USER_HOME, or (most importantly for
// this bead's acceptance criteria) XDG_DATA_HOME from the developer's own shell could
// make a case pass or fail for a reason unrelated to the zone it names, and could read
// state under the real XDG_DATA_HOME rather than a per-test synthetic one.
func isolateEnv(t *testing.T) {
	t.Helper()
	for _, v := range []string{
		"WORKSPACE_ROOT", "GRADLE_USER_HOME", "XDG_DATA_HOME",
		"CETA_EXTRA_READWRITE_ROOTS", "CETA_EXTRA_READONLY_ROOTS",
		"MONOREPO_ROOT",
	} {
		t.Setenv(v, "")
	}
}

// wantAfterEscape derives the expected Evaluate() verdict for a symlink escaping into a
// zone classified as wantZone, from Evaluate's own documented escape rule — not
// restated by hand per case, so a case cannot silently encode the wrong expectation.
func wantAfterEscape(wantZone PathAccess) PathAccess {
	if wantZone == PathReadWrite || wantZone == PathUnknown {
		return PathUnknown
	}
	return wantZone
}

// assertEscapeIntoZone builds a symlink inside projectDir pointing at target (which
// MUST already exist and fully resolve on disk), confirms target's OWN classification
// matches wantZone (a fixture sanity check — failure here means the case is set up
// wrong, not that the escape rule is broken), then asserts Evaluate's verdict on the
// SYMLINK matches wantAfterEscape(wantZone).
func assertEscapeIntoZone(t *testing.T, pe *PathEvaluator, projectDir, target string, wantZone PathAccess) {
	t.Helper()
	resolvedTarget := evalSymlinksWithFallback(target)
	if resolvedTarget == "" {
		t.Fatalf("target %s does not fully resolve on disk; escape check cannot be exercised (see TestPathEvaluator_BrokenSymlink)", target)
	}
	if got := pe.classify(resolvedTarget); got != wantZone {
		t.Fatalf("fixture broken: classify(%s) = %v, want %v (this zone's own classification, unrelated to the escape rule)", resolvedTarget, got, wantZone)
	}

	symlinkPath := filepath.Join(projectDir, "escape-link")
	if err := os.Symlink(target, symlinkPath); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}

	want := wantAfterEscape(wantZone)
	if got := pe.Evaluate(symlinkPath); got != want {
		verb := "BLOCKED"
		if want != PathUnknown {
			verb = "ALLOWED"
		}
		t.Errorf("Evaluate(symlink escaping project into a %v zone) = %v, want %v (escape into a %v zone must be %s)", wantZone, got, want, wantZone, verb)
	}
}

func TestEscapeRuleAgreesAcrossEveryZone(t *testing.T) {
	t.Run("workspace-root", func(t *testing.T) {
		isolateEnv(t)
		home := t.TempDir()
		t.Setenv("HOME", home)
		projectDir := t.TempDir()
		wsRoot := t.TempDir()
		t.Setenv("WORKSPACE_ROOT", wsRoot)
		target := filepath.Join(wsRoot, "sibling-repo")
		if err := os.MkdirAll(target, 0o755); err != nil {
			t.Fatalf("mkdir target: %v", err)
		}
		pe := New(projectDir)
		assertEscapeIntoZone(t, pe, projectDir, target, PathReadWrite)
	})

	t.Run("tmp-root", func(t *testing.T) {
		isolateEnv(t)
		home := t.TempDir()
		t.Setenv("HOME", home)
		projectDir := t.TempDir()
		target, err := os.MkdirTemp("/tmp", "ceta-escape-test-")
		if err != nil {
			t.Skipf("cannot create marker dir under /tmp: %v", err)
		}
		defer func() { _ = os.RemoveAll(target) }()
		pe := New(projectDir)
		// tmpRoot resolution is environment-dependent (e.g. inside a nix build
		// sandbox TMPDIR may not track literal /tmp — see the analogous caveat in
		// fabricated_root_zone_test.go). Skip rather than fail if this environment's
		// /tmp doesn't actually classify as PathReadWrite; that is an environment
		// property, not a claim about the escape rule under test.
		if got := pe.classify(evalSymlinksWithFallback(target)); got != PathReadWrite {
			t.Skipf("this environment's /tmp does not classify PathReadWrite (got %v); skipping, not a defect in the escape rule", got)
		}
		assertEscapeIntoZone(t, pe, projectDir, target, PathReadWrite)
	})

	t.Run("sandbox-allow-write", func(t *testing.T) {
		isolateEnv(t)
		home := t.TempDir()
		t.Setenv("HOME", home)
		projectDir := t.TempDir()
		target := t.TempDir()
		pe := New(projectDir)
		pe.SetSandboxConfig(&SandboxFilesystemConfig{AllowWrite: []string{target}})
		assertEscapeIntoZone(t, pe, projectDir, target, PathReadWrite)
	})

	t.Run("nix-store", func(t *testing.T) {
		isolateEnv(t)
		home := t.TempDir()
		t.Setenv("HOME", home)
		projectDir := t.TempDir()
		target := "/nix"
		if _, err := os.Stat(target); err != nil {
			t.Skipf("/nix does not exist in this environment: %v", err)
		}
		pe := New(projectDir)
		assertEscapeIntoZone(t, pe, projectDir, target, PathReadOnly)
	})

	t.Run("claude-readonly", func(t *testing.T) {
		isolateEnv(t)
		home := t.TempDir()
		t.Setenv("HOME", home)
		projectDir := t.TempDir()
		target := filepath.Join(home, ".claude", "settings")
		if err := os.MkdirAll(target, 0o755); err != nil {
			t.Fatalf("mkdir target: %v", err)
		}
		pe := New(projectDir)
		assertEscapeIntoZone(t, pe, projectDir, target, PathReadOnly)
	})

	t.Run("claude-readwrite-plans", func(t *testing.T) {
		isolateEnv(t)
		home := t.TempDir()
		t.Setenv("HOME", home)
		projectDir := t.TempDir()
		target := filepath.Join(home, ".claude", "plans")
		if err := os.MkdirAll(target, 0o755); err != nil {
			t.Fatalf("mkdir target: %v", err)
		}
		pe := New(projectDir)
		assertEscapeIntoZone(t, pe, projectDir, target, PathReadWrite)
	})

	t.Run("claude-readwrite-projects", func(t *testing.T) {
		isolateEnv(t)
		home := t.TempDir()
		t.Setenv("HOME", home)
		projectDir := t.TempDir()
		target := filepath.Join(home, ".claude", "projects")
		if err := os.MkdirAll(target, 0o755); err != nil {
			t.Fatalf("mkdir target: %v", err)
		}
		pe := New(projectDir)
		assertEscapeIntoZone(t, pe, projectDir, target, PathReadWrite)
	})

	t.Run("claude-json", func(t *testing.T) {
		isolateEnv(t)
		home := t.TempDir()
		t.Setenv("HOME", home)
		projectDir := t.TempDir()
		target := filepath.Join(home, ".claude.json")
		if err := os.WriteFile(target, []byte("{}"), 0o600); err != nil {
			t.Fatalf("write target: %v", err)
		}
		pe := New(projectDir)
		assertEscapeIntoZone(t, pe, projectDir, target, PathReadOnly)
	})

	t.Run("go-pkg", func(t *testing.T) {
		isolateEnv(t)
		home := t.TempDir()
		t.Setenv("HOME", home)
		projectDir := t.TempDir()
		target := filepath.Join(home, "go", "pkg", "mod")
		if err := os.MkdirAll(target, 0o755); err != nil {
			t.Fatalf("mkdir target: %v", err)
		}
		pe := New(projectDir)
		assertEscapeIntoZone(t, pe, projectDir, target, PathReadOnly)
	})

	t.Run("gradle-home", func(t *testing.T) {
		isolateEnv(t)
		home := t.TempDir()
		t.Setenv("HOME", home)
		projectDir := t.TempDir()
		gradleDir := t.TempDir()
		t.Setenv("GRADLE_USER_HOME", gradleDir)
		target := filepath.Join(gradleDir, "caches")
		if err := os.MkdirAll(target, 0o755); err != nil {
			t.Fatalf("mkdir target: %v", err)
		}
		pe := New(projectDir)
		assertEscapeIntoZone(t, pe, projectDir, target, PathReadOnly)
	})

	t.Run("xdg-nix-support-local-plugins", func(t *testing.T) {
		isolateEnv(t)
		home := t.TempDir()
		t.Setenv("HOME", home)
		projectDir := t.TempDir()
		xdg := t.TempDir()
		t.Setenv("XDG_DATA_HOME", xdg)
		target := filepath.Join(xdg, "nix-support-local-plugins", "plugins")
		if err := os.MkdirAll(target, 0o755); err != nil {
			t.Fatalf("mkdir target: %v", err)
		}
		pe := New(projectDir)
		assertEscapeIntoZone(t, pe, projectDir, target, PathReadOnly)
	})

	t.Run("xdg-contained-claude", func(t *testing.T) {
		isolateEnv(t)
		home := t.TempDir()
		t.Setenv("HOME", home)
		projectDir := t.TempDir()
		xdg := t.TempDir()
		t.Setenv("XDG_DATA_HOME", xdg)
		target := filepath.Join(xdg, "contained-claude")
		if err := os.MkdirAll(target, 0o755); err != nil {
			t.Fatalf("mkdir target: %v", err)
		}
		pe := New(projectDir)
		assertEscapeIntoZone(t, pe, projectDir, target, PathReadOnly)
	})

	// This is the zone the two pre-collapse ladder copies actually disagreed on
	// (Evaluate: PathReadWrite: the tool's own asks.db is a legitimate write target;
	// classifyWithoutEscapeCheck: PathReadOnly). Before the collapse this subtest
	// would have failed the fixture sanity check with whichever copy backed classify,
	// and — had classify still returned PathReadOnly here — would have gone on to
	// prove the escape was wrongly ALLOWED instead of BLOCKED.
	t.Run("xdg-claude-extended-tool-approver", func(t *testing.T) {
		isolateEnv(t)
		home := t.TempDir()
		t.Setenv("HOME", home)
		projectDir := t.TempDir()
		xdg := t.TempDir()
		t.Setenv("XDG_DATA_HOME", xdg)
		target := filepath.Join(xdg, "claude-extended-tool-approver")
		if err := os.MkdirAll(target, 0o755); err != nil {
			t.Fatalf("mkdir target: %v", err)
		}
		pe := New(projectDir)
		assertEscapeIntoZone(t, pe, projectDir, target, PathReadWrite)
	})

	t.Run("xdg-claude-pretool-hook", func(t *testing.T) {
		isolateEnv(t)
		home := t.TempDir()
		t.Setenv("HOME", home)
		projectDir := t.TempDir()
		xdg := t.TempDir()
		t.Setenv("XDG_DATA_HOME", xdg)
		target := filepath.Join(xdg, "claude-pretool-hook")
		if err := os.MkdirAll(target, 0o755); err != nil {
			t.Fatalf("mkdir target: %v", err)
		}
		pe := New(projectDir)
		assertEscapeIntoZone(t, pe, projectDir, target, PathReadOnly)
	})

	t.Run("extra-read-write-root", func(t *testing.T) {
		isolateEnv(t)
		home := t.TempDir()
		t.Setenv("HOME", home)
		projectDir := t.TempDir()
		rwRoot := t.TempDir()
		t.Setenv("CETA_EXTRA_READWRITE_ROOTS", rwRoot)
		target := filepath.Join(rwRoot, "sub")
		if err := os.MkdirAll(target, 0o755); err != nil {
			t.Fatalf("mkdir target: %v", err)
		}
		pe := New(projectDir)
		assertEscapeIntoZone(t, pe, projectDir, target, PathReadWrite)
	})

	t.Run("extra-read-only-root", func(t *testing.T) {
		isolateEnv(t)
		home := t.TempDir()
		t.Setenv("HOME", home)
		projectDir := t.TempDir()
		roRoot := t.TempDir()
		t.Setenv("CETA_EXTRA_READONLY_ROOTS", roRoot)
		target := filepath.Join(roRoot, "sub")
		if err := os.MkdirAll(target, 0o755); err != nil {
			t.Fatalf("mkdir target: %v", err)
		}
		pe := New(projectDir)
		assertEscapeIntoZone(t, pe, projectDir, target, PathReadOnly)
	})

	t.Run("unknown-zone", func(t *testing.T) {
		isolateEnv(t)
		home := t.TempDir()
		t.Setenv("HOME", home)
		projectDir := t.TempDir()
		target := t.TempDir() // a real dir under no configured zone at all
		pe := New(projectDir)
		assertEscapeIntoZone(t, pe, projectDir, target, PathUnknown)
	})
}
