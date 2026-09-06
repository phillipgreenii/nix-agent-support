package patheval

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMain — tc-xoqj.
//
// New/NewWithCWD hardcode tmpRoot to literal, resolved "/tmp" (see the
// "/tmp/**" comment in classify): that zone is deliberately NOT keyed on
// $TMPDIR, because /tmp itself is the tool's own well-known agent-scratch
// convention, independent of whatever a given process's temp dir happens to
// be configured to. That is real, intended product behavior and this file
// does not change it (see TestPathEvaluator_Tmp_ReadWrite in
// evaluator_test.go, which pins exactly that literal path).
//
// Go's t.TempDir(), however, is NOT pinned to literal /tmp — it builds under
// os.TempDir(), which reads $TMPDIR and falls back to "/tmp" only when
// $TMPDIR is unset. macOS sets a per-user $TMPDIR outside /tmp by default, so
// this collision is normally invisible; on a plain Linux box with $TMPDIR
// unset (this machine, tc-xoqj) os.TempDir() IS "/tmp", so every HOME/
// XDG_DATA_HOME/GRADLE_USER_HOME/extra-root/"unrelated" fixture built with
// t.TempDir() across this package's tests (escape_zone_ladder_test.go,
// fabricated_root_zone_test.go, ...) lands INSIDE the tool's own hardcoded
// read-write /tmp zone by pure accident of fixture placement — before the
// narrower zone rule the fixture exists to exercise is ever reached. Every
// such test failed with "fixture broken: classify(...) = read-write, want
// read-only/unknown", which is the test-authoring version of exactly the
// nix-build-sandbox /nix collision escape_zone_ladder_test.go's pg2-lw19e
// note and evaluator_test.go's pinnedHome both already document and guard
// against for a different ambient directory (there it was $TMPDIR landing
// under /nix; here it is $TMPDIR being literally unset and defaulting to
// /tmp). Confirmed unrelated to classify() itself: TestPathEvaluator_
// Tmp_ReadWrite (literal "/tmp/foo", no t.TempDir() involved) passes
// unchanged, and pe.projectRootGrantsZone-backed project-root fixtures
// (mount_test.go et al.) are unaffected because the project-root check in
// classify() runs before the tmpRoot check regardless of where the project
// directory happens to sit.
//
// The fix belongs on the TEST side, not classify(): redirect $TMPDIR (and
// therefore every subsequent t.TempDir() in this test binary) to a real,
// writable directory that does not resolve under literal /tmp, so fixtures
// stop colliding with the tool's own /tmp zone. This only ever ACTIVATES when
// the effective temp dir would otherwise be literal /tmp — an environment
// that already places $TMPDIR elsewhere (macOS's default, or a nix build
// sandbox's $NIX_BUILD_TOP under /nix) is left exactly as before, including
// the existing, deliberate /nix-sandbox skip logic in
// escape_zone_ladder_test.go and fabricated_root_zone_test.go.
func TestMain(m *testing.M) {
	restore, err := redirectTempDirAwayFromLiteralTmp()
	if err != nil {
		fmt.Fprintln(os.Stderr, "patheval TestMain: "+err.Error())
		os.Exit(1)
	}
	code := m.Run()
	restore()
	os.Exit(code)
}

// redirectTempDirAwayFromLiteralTmp points $TMPDIR at a fresh, non-/tmp
// directory when the effective OS temp dir would otherwise resolve under
// literal "/tmp", and returns a restore func that removes the directory and
// puts $TMPDIR back exactly as found. When the effective temp dir is already
// outside /tmp, it does nothing and returns a no-op restore.
func redirectTempDirAwayFromLiteralTmp() (restore func(), err error) {
	effective := evalSymlinksWithFallback(os.TempDir())
	if effective == "" {
		effective = filepath.Clean(os.TempDir())
	}
	if effective != "/tmp" && !strings.HasPrefix(effective+"/", "/tmp/") {
		return func() {}, nil
	}

	// Any directory nested under literal /tmp — no matter how deep — still
	// matches classify()'s tmpRoot prefix check, so the replacement MUST live
	// entirely outside it. The ambient $HOME (read here, before any test in
	// this package overrides it for its own PathEvaluator) is a portable,
	// always-available choice: every test in this package sets its OWN
	// synthetic HOME/XDG_DATA_HOME/etc. before constructing an evaluator, so
	// this ambient value is never itself read as a zone by the code under
	// test.
	homeDir, homeErr := os.UserHomeDir()
	if homeErr != nil || homeDir == "" {
		return nil, fmt.Errorf("no usable $HOME to place a non-/tmp scratch directory outside literal /tmp (effective temp dir %q collides with the tool's own tmpRoot zone): %w", effective, homeErr)
	}
	cacheParent := filepath.Join(homeDir, ".cache")
	if err := os.MkdirAll(cacheParent, 0o700); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", cacheParent, err)
	}
	dir, err := os.MkdirTemp(cacheParent, "ceta-patheval-test-tmp-")
	if err != nil {
		return nil, fmt.Errorf("create non-/tmp scratch dir under %s: %w", cacheParent, err)
	}

	original, hadOriginal := os.LookupEnv("TMPDIR")
	if err := os.Setenv("TMPDIR", dir); err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("set TMPDIR=%s: %w", dir, err)
	}
	return func() {
		_ = os.RemoveAll(dir)
		if hadOriginal {
			_ = os.Setenv("TMPDIR", original)
		} else {
			_ = os.Unsetenv("TMPDIR")
		}
	}, nil
}
