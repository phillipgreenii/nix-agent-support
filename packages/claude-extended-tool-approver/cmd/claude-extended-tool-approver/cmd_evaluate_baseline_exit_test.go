package main

import (
	"testing"

	"github.com/phillipgreenii/x/exitcode"
)

// The less-restrictive exit of `evaluate --baseline` MUST NOT collide with the
// usage code, and is pinned to the shared Degraded code.
func TestEvaluateBaselineExit_LessRestrictiveIsDegradedNotUsage(t *testing.T) {
	if evaluateBaselineExitLessRestrictive != exitcode.Degraded {
		t.Errorf("less-restrictive exit = %d, want exitcode.Degraded (%d)", evaluateBaselineExitLessRestrictive, exitcode.Degraded)
	}
	if evaluateBaselineExitLessRestrictive == exitcode.Usage {
		t.Errorf("less-restrictive exit collides with exitcode.Usage (%d)", exitcode.Usage)
	}
}
