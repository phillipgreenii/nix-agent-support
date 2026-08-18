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
//
// pg2-lw19e: inside a `nix build .#checks.<system>.claude-extended-tool-approver-
// integration-tests` sandbox, $TMPDIR (and therefore every t.TempDir()) IS
// NIX_BUILD_TOP, which on this system nix places under /nix/var/nix/builds/<id> —
// so HOME/XDG_DATA_HOME/extra-root fixtures built on t.TempDir() land under /nix
// and trip classify()'s coarse "/nix/**" ReadOnly guard before the more specific
// zone under test is ever reached (5 subtests: claude-readwrite-plans,
// claude-readwrite-projects, xdg-claude-extended-tool-approver,
// extra-read-write-root, unknown-zone). This was root-caused as a TEST-ENVIRONMENT
// artifact, not a classify() defect: a live user session's HOME/XDG_DATA_HOME/
// extra roots never resolve under /nix (only a build user's transient scratch
// space does), and classify()'s ladder ordering — explicit config grants
// (projectRoot/workspaceRoot/tmpRoot/sandboxConfig) before the broad /nix guard,
// before the narrower home/xdg/extra-root zones — is intentional and correct for
// real use. See the skip added in assertEscapeIntoZone's fixture sanity check for
// the full reasoning; classify() itself is deliberately left unchanged.
package patheval

import (
	"os"
	"path/filepath"
	"strings"
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
		// A t.TempDir()-rooted fixture ordinarily lands outside every zone
		// classify recognizes: a normal OS tmp dir (macOS's per-user
		// /var/folders/.../T, or /tmp when $TMPDIR is unset) is not itself
		// HOME, XDG_DATA_HOME, an extra root, or /nix. Inside a nix BUILD
		// SANDBOX, though, $TMPDIR *is* NIX_BUILD_TOP, which on this system
		// nix places under /nix/var/nix/builds/<build-id> — a real,
		// writable-during-the-build directory that nonetheless satisfies
		// classify()'s coarse "/nix/**" guard (intended only to protect the
		// immutable /nix/store from writes). That guard runs BEFORE the
		// HOME/XDG_DATA_HOME/extra-root zone rules this test exercises, so
		// a fixture nested under HOME/XDG_DATA_HOME/an extra root inherits
		// the guard's coarser ReadOnly/blocked answer purely because of
		// where THIS BUILD happened to put its scratch space — not because
		// classify()'s zone ordering is wrong for real use. A live user
		// session's HOME, XDG_DATA_HOME, and configured extra roots never
		// resolve under /nix: that directory exists only transiently,
		// owned by the sandboxed build user, for the duration of one nix
		// build (verified for pg2-lw19e: in the failing build, TMPDIR ==
		// HOME == NIX_BUILD_TOP == /nix/var/nix/builds/<id>, while literal
		// /tmp independently resolves to /private/tmp and IS writable —
		// see the "tmp-root" subtest above, which already hedges the
		// analogous /tmp-placement case the same way). So this is squarely
		// a test-environment placement artifact, not a classify() defect,
		// and classify()'s ladder is deliberately left untouched. Skip
		// rather than fail — mirroring "tmp-root"'s existing precedent —
		// but ONLY when the mismatch is actually explained by this: any
		// other fixture mismatch (not rooted under /nix) still hard-fails,
		// so a genuine classify() regression on a normal machine is still
		// caught.
		if strings.HasPrefix(resolvedTarget, "/nix/") {
			t.Skipf("fixture %s resolves under /nix (this build's $TMPDIR is NIX_BUILD_TOP); "+
				"classify()'s /nix/** guard produced %v before the %v zone under test was ever reached — "+
				"nix-sandbox TMPDIR-placement artifact, not a defect (see comment above)",
				resolvedTarget, got, wantZone)
		}
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
