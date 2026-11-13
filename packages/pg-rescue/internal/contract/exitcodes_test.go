package contract

import (
	"testing"

	"github.com/phillipgreenii/x/exitcode"
)

// The handler contract codes are app codes of x/exitcode (20-63), distinct from
// each other and from the shared codes, so a handler's usage error (2) or
// generic failure (1) is never read as an outcome.
func TestHandlerExitCodesAreAppCodes(t *testing.T) {
	if ExitResolved != exitcode.OK {
		t.Errorf("ExitResolved = %d; want %d", ExitResolved, exitcode.OK)
	}
	for name, code := range map[string]int{"declined": ExitDeclined, "deferred": ExitDeferred} {
		if !exitcode.IsAppSpecific(code) {
			t.Errorf("%s = %d; want an app code in %d-%d", name, code, exitcode.AppMin, exitcode.AppMax)
		}
	}
	if ExitDeclined == ExitDeferred {
		t.Error("declined and deferred share a code")
	}
}
