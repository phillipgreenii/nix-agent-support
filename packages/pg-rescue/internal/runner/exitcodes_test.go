package runner_test

import (
	"testing"

	"github.com/phillipgreenii/pg-rescue/internal/app"
	"github.com/phillipgreenii/pg-rescue/internal/contract"
	"github.com/phillipgreenii/pg-rescue/internal/runner"
	"github.com/phillipgreenii/x/exitcode"
)

// The wrapper's own exit codes are app codes (20-63), pairwise distinct, never
// a sysexits code, and disjoint from the handler contract's codes.
func TestWrapperExitCodesAreDistinctAppCodes(t *testing.T) {
	codes := map[string]int{
		"wrapper error":   runner.ExitWrapperError,
		"deferred":        runner.ExitDeferred,
		"stdin unhandled": runner.ExitStdinUnhandled,
		"handler decline": contract.ExitDeclined,
		"handler defer":   contract.ExitDeferred,
	}
	seen := map[int]string{}
	for name, c := range codes {
		if !exitcode.IsAppSpecific(c) {
			t.Errorf("%s = %d; want an app code", name, c)
		}
		if prev, dup := seen[c]; dup {
			t.Errorf("%s and %s share code %d", name, prev, c)
		}
		seen[c] = name
	}
	if app.ExitWrapperError != runner.ExitWrapperError || app.ExitDeferred != runner.ExitDeferred {
		t.Error("package app must mirror the runner's wrapper codes")
	}
}
