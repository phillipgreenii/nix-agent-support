package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/executor"
	"github.com/phillipgreenii/pg-router/conformance"
)

// TestRunDispatch_poolCapacityUnknownExitsBusyWithReason proves runDispatch
// maps the admission gate's capacity-unknown decline
// (executor.ErrPoolCapacityUnknown, internal/executor/ccpool.go's run()) to
// the wire's pre-accept busy decline — conformance.ExitBusy — carrying the
// OPTIONAL reply body bead pg2-j4uwg adds: {"schemaVersion":"1","reason":
// "capacity-unknown"}. Before this bead the busy decline always wrote NO
// body at all (INV-CONC-1, DEC-WIRE-1 exit 9 permits, but never required,
// one — "no body required"); this proves the now-written body carries the
// correct reason for THIS branch specifically, distinct from the
// at-capacity branch TestRunDispatch_poolAtCapacityExitsBusyWithReason below
// (ADR 0072's Decision item 3).
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
func TestRunDispatch_poolCapacityUnknownExitsBusyWithReason(t *testing.T) {
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
	var reply struct {
		SchemaVersion string `json:"schemaVersion"`
		Reason        string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(got), &reply); err != nil {
		t.Fatalf("stdout = %q, not valid JSON: %v", got, err)
	}
	if reply.Reason != busyReasonCapacityUnknown {
		t.Fatalf("reason = %q, want %q", reply.Reason, busyReasonCapacityUnknown)
	}
}

// TestRunDispatch_busyDeclineReasonAtCapacity is dispatch.go's own
// busyDeclineReason mapping tested directly (unit-level, no process/CLI
// faking needed): the at-capacity branch — the OTHER admission-gate
// sentinel from the capacity-unknown case above — maps to
// busyReasonAtCapacity, and neither sentinel matches the other's tag.
func TestRunDispatch_busyDeclineReasonAtCapacity(t *testing.T) {
	reason, busy := busyDeclineReason(executor.ErrPoolAtCapacity)
	if !busy {
		t.Fatalf("ErrPoolAtCapacity must be recognized as a busy decline")
	}
	if reason != busyReasonAtCapacity {
		t.Fatalf("reason = %q, want %q", reason, busyReasonAtCapacity)
	}
}

// TestRunDispatch_busyDeclineReasonLowDisk is dispatch.go's own
// busyDeclineReason mapping tested directly for the low-disk sentinel (bead
// pg2-8vn8t, widening ADR 0072's Decision item 3's busy-decline set): an
// isolation failure caused by a full filesystem (executor.ErrLowDisk) maps to
// busyReasonLowDisk, distinct from either capacity reason above.
func TestRunDispatch_busyDeclineReasonLowDisk(t *testing.T) {
	reason, busy := busyDeclineReason(executor.ErrLowDisk)
	if !busy {
		t.Fatalf("ErrLowDisk must be recognized as a busy decline")
	}
	if reason != busyReasonLowDisk {
		t.Fatalf("reason = %q, want %q", reason, busyReasonLowDisk)
	}
}

// TestRunDispatch_busyDeclineReasonNilAndOtherErrors proves busyDeclineReason
// reports ok=false for nil and for an unrelated error — it must not
// misclassify a genuine dispatch failure as a busy decline.
func TestRunDispatch_busyDeclineReasonNilAndOtherErrors(t *testing.T) {
	if _, busy := busyDeclineReason(nil); busy {
		t.Fatal("nil err must not be classified as a busy decline")
	}
	if _, busy := busyDeclineReason(errors.New("some other failure")); busy {
		t.Fatal("an unrelated error must not be classified as a busy decline")
	}
}

// TestRunDispatch_busyDeclineReasonCommandBusy is dispatch.go's own
// busyDeclineReason mapping tested directly for the command role's exit-9
// sentinel (bead pg2-358u3): executor.ErrBusy, wrapped exactly as
// internal/executor/command.go wraps it, maps to busyReasonCommandBusy,
// distinct from the capacity and low-disk reasons.
func TestRunDispatch_busyDeclineReasonCommandBusy(t *testing.T) {
	wrapped := fmt.Errorf("command role %q item %s: %w: %w", "r", "b1", executor.ErrBusy, errors.New("exit status 9"))
	reason, busy := busyDeclineReason(wrapped)
	if !busy {
		t.Fatalf("a wrapped executor.ErrBusy must be recognized as a busy decline")
	}
	if reason != busyReasonCommandBusy {
		t.Fatalf("reason = %q, want %q", reason, busyReasonCommandBusy)
	}
}

// TestRunDispatch_commandRoleExit9ExitsBusyWithReason proves the whole
// command-role path end to end (bead pg2-358u3): a command role whose real
// argv exits 9 makes runDispatch exit conformance.ExitBusy with the OPTIONAL
// reply body {"schemaVersion":"1","reason":"command-busy"} — a pre-accept
// busy decline the core re-offers, NOT an accepted-dispatch failure
// (ExitError + an {"error": ...} body). The body must also carry none of the
// command's stderr (the failure-only tail must not leak onto the busy path).
func TestRunDispatch_commandRoleExit9ExitsBusyWithReason(t *testing.T) {
	t.Setenv("PATH", "/usr/bin")

	dir := t.TempDir()
	rolePath := filepath.Join(dir, "role.json")
	roleJSON := `{"name":"cmd-busy","type":"command","command":{"argv":["/bin/sh","-c","echo secret-stderr-text >&2; exit 9"]}}`
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
	var reply struct {
		SchemaVersion string `json:"schemaVersion"`
		Reason        string `json:"reason"`
		Error         string `json:"error"`
	}
	if err := json.Unmarshal([]byte(got), &reply); err != nil {
		t.Fatalf("stdout = %q, not valid JSON: %v", got, err)
	}
	if reply.Reason != busyReasonCommandBusy {
		t.Fatalf("reason = %q, want %q", reply.Reason, busyReasonCommandBusy)
	}
	if reply.Error != "" {
		t.Fatalf("busy reply must carry no error text; got %q", reply.Error)
	}
	for _, leak := range []string{"stderr tail", "secret-stderr-text"} {
		if strings.Contains(got, leak) {
			t.Fatalf("busy reply must not carry %q; stdout=%q", leak, got)
		}
	}
}
