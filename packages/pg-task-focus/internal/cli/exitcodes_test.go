package cli

import (
	"testing"

	"github.com/phillipgreenii/x/exitcode"
)

// The numeric values are the contract with every script that branches on them,
// so they are pinned here as literals.
func TestExitCodesPinned(t *testing.T) {
	for _, c := range []struct {
		name string
		got  int
		want int
	}{
		{"ok", ExitOK, 0},
		{"error", ExitError, 1},
		{"usage", ExitUsage, 2},
		{"unreachable is Unavailable", ExitUnreachable, 3},
		{"not ready is Unavailable", ExitNotReady, 3},
		{"serve could not start is Config", ExitStartFailed, 7},
		{"refused", ExitRefused, 20},
		{"store", ExitStore, 21},
		{"check failed", ExitCheckFailed, 22},
	} {
		if c.got != c.want {
			t.Errorf("%s = %d, want %d", c.name, c.got, c.want)
		}
	}
	if ExitUnreachable != exitcode.Unavailable || ExitStartFailed != exitcode.Config {
		t.Error("shared exit codes drifted from x/exitcode")
	}
	for _, code := range []int{ExitRefused, ExitStore, ExitCheckFailed} {
		if !exitcode.IsAppSpecific(code) {
			t.Errorf("app code %d is outside the app-specific range 20 to 63", code)
		}
	}
}
