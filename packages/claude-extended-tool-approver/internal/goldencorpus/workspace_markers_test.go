package goldencorpus

import (
	"os"
	"path/filepath"
	"testing"
)

// TestPathStateFixtureMaterialize pins how a corpus row whose verdict depends
// on the working directory being a trusted exec location
// (effectpolicy.TrustedCheckoutExec) is given a cwd: a row naming
// workspace_markers gets a real, throwaway directory carrying those marker
// files in place of its synthetic cwd, and a row without them keeps the
// synthetic cwd untouched.
func TestPathStateFixtureMaterialize(t *testing.T) {
	plain := PathStateFixture{CWD: "/repo", ProjectRoot: "/repo"}
	cwd, root, cleanup, err := plain.Materialize()
	if err != nil {
		t.Fatal(err)
	}
	cleanup()
	if cwd != "/repo" || root != "/repo" {
		t.Errorf("no markers: got cwd=%q root=%q, want the synthetic /repo untouched", cwd, root)
	}

	marked := PathStateFixture{CWD: "/repo", ProjectRoot: "/repo", WorkspaceMarkers: []string{"pn-workspace.toml"}}
	cwd, root, cleanup, err = marked.Materialize()
	if err != nil {
		t.Fatal(err)
	}
	if cwd != root {
		t.Errorf("cwd %q and project root %q must agree", cwd, root)
	}
	info, statErr := os.Stat(cwd)
	if statErr != nil || !info.IsDir() {
		t.Fatalf("materialized cwd %q is not a directory: %v", cwd, statErr)
	}
	// The marker sits ABOVE the cwd (a workforest set directory), not in it.
	if _, err := os.Stat(filepath.Join(cwd, "pn-workspace.toml")); err == nil {
		t.Errorf("marker is in the cwd itself; it must sit above it, like a real workforest set")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(filepath.Dir(cwd)), "pn-workspace.toml")); err != nil {
		t.Errorf("marker file missing above the cwd: %v", err)
	}
	cleanup()
	if _, err := os.Stat(cwd); !os.IsNotExist(err) {
		t.Errorf("cleanup left %q behind", cwd)
	}

	for _, bad := range []string{"", "../escape", "a/b", "/abs"} {
		fx := PathStateFixture{CWD: "/repo", WorkspaceMarkers: []string{bad}}
		if _, _, _, err := fx.Materialize(); err == nil {
			t.Errorf("marker name %q: want a refusal, got nil", bad)
		}
	}
}
