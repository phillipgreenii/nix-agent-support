package participant_test

import (
	"bytes"
	"os/exec"
	"strings"
	"testing"
)

// allowedDeps lists the packages outside the standard library that the
// participant contract may depend on: only the shared exit codes, which it
// re-exports.
var allowedDeps = map[string]bool{
	"github.com/phillipgreenii/x/exitcode": true,
}

// TestClosure fails when go list -deps of this package contains anything
// outside the standard library and the allowed list.
func TestClosure(t *testing.T) {
	assertClosure(t, ".", "github.com/phillipgreenii/pg-router/api/participant", allowedDeps)
}

// TestCoreclientClosure keeps the socket client inside the shared set: the
// standard library, the shared exit codes and the contract package itself.
func TestCoreclientClosure(t *testing.T) {
	allowed := map[string]bool{
		"github.com/phillipgreenii/x/exitcode":                true,
		"github.com/phillipgreenii/pg-router/api/participant": true,
	}
	assertClosure(t, "./coreclient", "github.com/phillipgreenii/pg-router/api/participant/coreclient", allowed)
}

func assertClosure(t *testing.T, pkg, self string, allowed map[string]bool) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "go", "list", "-deps", "-f", "{{if not .Standard}}{{.ImportPath}}{{end}}", pkg)
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("go list: %v\n%s", err, stderr.String())
	}
	for _, p := range strings.Fields(out.String()) {
		if p == self || allowed[p] {
			continue
		}
		t.Errorf("%s depends on %s, outside the standard library and the allowed list", self, p)
	}
}
