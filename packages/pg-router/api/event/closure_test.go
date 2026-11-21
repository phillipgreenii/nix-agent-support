package event_test

import (
	"bytes"
	"os/exec"
	"strings"
	"testing"
)

// allowedDeps lists the packages outside the standard library that this contract
// package may depend on. It is empty: the event contract is standard library
// only, so a consumer in another module gains no dependency by importing it.
var allowedDeps = map[string]bool{}

// TestClosure fails when go list -deps of this package contains anything
// outside the standard library and the allowed list.
func TestClosure(t *testing.T) {
	cmd := exec.CommandContext(t.Context(), "go", "list", "-deps", "-f", "{{if not .Standard}}{{.ImportPath}}{{end}}", ".")
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("go list: %v\n%s", err, stderr.String())
	}
	const self = "github.com/phillipgreenii/pg-router/api/event"
	for _, p := range strings.Fields(out.String()) {
		if p == self || allowedDeps[p] {
			continue
		}
		t.Errorf("api/event depends on %s, outside the standard library and the allowed list", p)
	}
}
