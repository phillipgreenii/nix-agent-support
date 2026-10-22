package temproottest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/temproot"
)

// outsideEveryBuiltinRoot returns a fresh directory that is NOT under any
// temproot built-in root, to stand in for GOTMPDIR=/Volumes/gitrepos/build_tmp.
// It lives next to the package source (the module checkout), which is never
// under $TMPDIR, /private/var/folders, /private/tmp or /tmp on a developer
// machine. It skips, rather than silently weakening, when the checkout itself
// sits under a temp root (a build sandbox), because the premise cannot hold.
func outsideEveryBuiltinRoot(t *testing.T) string {
	t.Helper()
	t.Setenv(extraRootsEnvVar, "")
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp(wd, "gotmpdir-fixture-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if temproot.Under(dir) {
		t.Skipf("checkout %s is itself under a built-in temp root; cannot model an out-of-root GOTMPDIR here", wd)
	}
	return dir
}

// TestTempDir_IndependentOfGOTMPDIR is the pg2-gg2bf regression guard (V-3):
// with GOTMPDIR pointing outside every built-in root, a plain t.TempDir()
// fixture is NOT a temp root (the failure the three CETA packages had), while
// the helper's fixture is, and its sibling and a real path are not.
func TestTempDir_IndependentOfGOTMPDIR(t *testing.T) {
	out := outsideEveryBuiltinRoot(t)
	t.Setenv("GOTMPDIR", out)

	plain := t.TempDir()
	if !strings.HasPrefix(plain, out) {
		t.Skipf("t.TempDir() %q does not honour GOTMPDIR %q on this Go version", plain, out)
	}
	if temproot.Under(plain) {
		t.Fatalf("premise broken: plain t.TempDir() %q is already under a temp root", plain)
	}

	fx := TempDir(t)
	if !strings.HasPrefix(fx, out) {
		t.Fatalf("helper fixture %q is not under GOTMPDIR %q", fx, out)
	}
	if !temproot.Under(filepath.Join(fx, ".git", "config")) {
		t.Errorf("helper fixture %q must count as a temp root", fx)
	}
	if temproot.Under(plain) {
		t.Errorf("a sibling t.TempDir() %q must not become a temp root", plain)
	}
	if temproot.Under("/etc/pg2-gg2bf-real-canonical/.git") {
		t.Error("a real, non-temp path must stay outside every temp root")
	}
}

// TestDeclareRoot_MergesWithExisting pins the merge (never replace) contract.
func TestDeclareRoot_MergesWithExisting(t *testing.T) {
	t.Setenv(extraRootsEnvVar, "/etc/pg2-gg2bf-existing")
	dir := TempDir(t)
	got := os.Getenv(extraRootsEnvVar)
	if got != "/etc/pg2-gg2bf-existing:"+dir {
		t.Errorf("%s = %q, want existing value kept and %q appended", extraRootsEnvVar, got, dir)
	}
	if !temproot.Under("/etc/pg2-gg2bf-existing/x") {
		t.Error("the pre-existing extra root must still be honoured")
	}
}
