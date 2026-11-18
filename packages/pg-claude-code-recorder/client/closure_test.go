package client

import (
	"os/exec"
	"strings"
	"testing"
)

const modulePath = "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder"

// deps lists the import paths in the dependency closure of pkg (a path relative
// to the module root), run from the module root.
func deps(t *testing.T, pkg string) []string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "go", "list", "-deps", "-f", "{{.ImportPath}}", pkg)
	cmd.Dir = ".."
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list -deps %s: %v", pkg, err)
	}
	return strings.Fields(string(out))
}

// TestClientClosureHasNoEngine: the public client must not pull the engine into
// a consumer: no store, lifecycle, feed or query package and no SQLite driver.
func TestClientClosureHasNoEngine(t *testing.T) {
	forbidden := []string{
		modulePath + "/internal/store",
		modulePath + "/internal/lifecycle",
		modulePath + "/internal/feed",
		modulePath + "/internal/query",
		"modernc.org/sqlite",
	}
	list := deps(t, "./client")
	if len(list) < 10 {
		t.Fatalf("closure of ./client looks empty: %v", list)
	}
	for _, d := range list {
		for _, f := range forbidden {
			if d == f || strings.HasPrefix(d, f+"/") {
				t.Errorf("./client depends on %s (forbidden prefix %s)", d, f)
			}
		}
	}
}

// TestWireClosureIsStdlibOnly: the wire contract package depends on the
// standard library and x only.
func TestWireClosureIsStdlibOnly(t *testing.T) {
	list := deps(t, "./wire")
	for _, d := range list {
		first, _, _ := strings.Cut(d, "/")
		if !strings.Contains(first, ".") { // standard library, vendored std included
			continue
		}
		if d == modulePath+"/wire" || strings.HasPrefix(d, "github.com/phillipgreenii/x/") {
			continue
		}
		t.Errorf("./wire depends on %s, outside the standard library and x", d)
	}
}
