package main

import (
	"bytes"
	"strings"
	"testing"
)

// TestValidateLastN pins the exact boundary of the CLI-level guard: only a
// strictly-negative --last-n is rejected. Zero (the sentinel `internal/gate.Check`
// still defaults to 100) and any positive value MUST pass through unvalidated here
// — this function's whole job is rejecting the meaningless input, not replicating
// Check's own defaulting.
func TestValidateLastN(t *testing.T) {
	for _, n := range []int{0, 1, 100} {
		if err := validateLastN(n); err != nil {
			t.Errorf("validateLastN(%d) = %v, want nil", n, err)
		}
	}
	for _, n := range []int{-1, -5, -100} {
		err := validateLastN(n)
		if err == nil {
			t.Fatalf("validateLastN(%d) = nil, want an error", n)
		}
		if !strings.Contains(err.Error(), "--last-n must be >= 0") {
			t.Errorf("validateLastN(%d) error = %q, want a clear --last-n message", n, err.Error())
		}
	}
}

// TestGateCheckCmd_rejectsNegativeLastN pins the CLI-boundary rejection added for
// bead pg2-w70x1: `git log -n -5` silently ignores the negative bound and scans the
// FULL history instead of erroring (verified directly), so a negative --last-n must
// fail fast here rather than ever reaching internal/gate.Check. The validation runs
// before os.Getwd() or any pn/bd/git client is constructed, so this test never
// touches a real pn/bd/git binary or workspace despite exercising the real cobra
// command end to end.
func TestGateCheckCmd_rejectsNegativeLastN(t *testing.T) {
	cmd := newGateCheckCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--last-n", "-5"})

	err := cmd.Execute()
	if err == nil {
		t.Fatalf("expected an error for --last-n -5, got none (output: %s)", out.String())
	}
	if !strings.Contains(err.Error(), "--last-n must be >= 0") {
		t.Fatalf("error = %q, want a clear --last-n message", err.Error())
	}
	if !strings.Contains(err.Error(), "-5") {
		t.Fatalf("error = %q, want the rejected value echoed back", err.Error())
	}
}
