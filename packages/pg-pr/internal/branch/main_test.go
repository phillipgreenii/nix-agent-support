package branch

import (
	"os"
	"testing"
)

// TestMain unsets every git-location env var BEFORE any test runs. A git
// hook (pre-commit/prek, invoking `go test` for this very package as its
// run-unit-tests hook) exports GIT_DIR/GIT_WORK_TREE for the commit in
// progress, and this test binary inherits that. `-C <dir>` and even an
// explicit repo path argument passed to Detect do NOT override these — git's
// own repo discovery consults them FIRST — so a test that builds an isolated
// fixture under t.TempDir() and passes its path to Detect would otherwise
// silently operate on the AMBIENT repo (this checkout) instead of the
// fixture: confirmed 2026-08-27 (pg2-5ek6b/pg2-12795) — TestDetectBasicWithPR
// and friends reported this checkout's own repo/branch instead of the
// fixture's "owner/repo"/"main" under a real commit-time hook run, though a
// plain `go test` from an uncontaminated shell never reproduces it.
// Unsetting these here, once, for the whole process is sufficient: nothing
// in this package's tests or the code under test re-sets them.
func TestMain(m *testing.M) {
	for _, k := range []string{
		"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_CEILING_DIRECTORIES",
		"GIT_COMMON_DIR", "GIT_PREFIX", "GIT_OBJECT_DIRECTORY",
	} {
		_ = os.Unsetenv(k)
	}
	os.Exit(m.Run())
}
