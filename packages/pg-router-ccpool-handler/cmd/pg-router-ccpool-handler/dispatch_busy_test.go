package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-router/conformance"
)

// TestRunDispatch_poolFullExitsBusyNoBody proves runDispatch maps the
// admission gate's decline (executor.ErrPoolAtCapacity, added to
// internal/executor/ccpool.go's run()) to the wire's pre-accept busy decline
// — conformance.ExitBusy, with NO reply written to stdout at all (INV-CONC-1,
// DEC-WIRE-1 exit 9; ADR 0072's Decision item 3).
//
// runDispatch has no seam to inject a fake executor (it builds Deps itself
// from loadRole/loadConfig/buildDeps), so this goes through the REAL
// buildDeps wiring — a real ccpool.CLIRunner shelling out to the `ccpool`
// binary. PATH is overridden to a directory that cannot contain that binary
// (mirroring internal/beads/runner_test.go's own `t.Setenv("PATH",
// "/usr/bin")` isolation trick), so every `ccpool` subcommand this process
// would invoke — starting with `ccpool capacity --json`, the very first one
// run() calls after the (list-based) duplicate-absorb check — fails with
// "executable file not found in $PATH". That is exactly an unreadable pool:
// the admission gate's fail-closed branch, reached deterministically and
// without ever touching the real ccpool pool/tmux server/store.db (this
// module's own unit-test isolation invariant).
func TestRunDispatch_poolFullExitsBusyNoBody(t *testing.T) {
	t.Setenv("PATH", "/usr/bin")

	dir := t.TempDir()
	rolePath := filepath.Join(dir, "role.json")
	roleJSON := `{"name":"worker","type":"ccpool","ccpool":{"actor":"test-actor","completion":"close-only","onFailure":"unclaim","onDispatchFail":"unclaim","promptBody":"hello"}}`
	if err := os.WriteFile(rolePath, []byte(roleJSON), 0o644); err != nil {
		t.Fatalf("write role config: %v", err)
	}

	restoreIn := redirectStdin(t, `{"schemaVersion":"1","id":"d-1","event":{"id":"e-1","type":"dispatch","payload":{"id":"zr-w"}}}`)
	defer restoreIn()

	var code int
	got := captureStdout(t, func() {
		code = runDispatch([]string{"--role-config", rolePath})
	})
	if code != conformance.ExitBusy {
		t.Fatalf("exit = %d, want ExitBusy (%d); stdout=%q", code, conformance.ExitBusy, got)
	}
	if strings.TrimSpace(got) != "" {
		t.Fatalf("stdout = %q, want an empty body on a busy decline (no reply written at all)", got)
	}
}
