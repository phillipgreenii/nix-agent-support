package pgassistants_test

import (
	"testing"

	"github.com/phillipgreenii/x/cmdruntest"
)

// Every cmdrun.Call in the module's non-test code sets a Timeout (or carries a
// stated //cmdrun:unbounded reason), so no external call can run unbounded.
func TestCallsBounded(t *testing.T) {
	cmdruntest.AssertCallsBounded(t, ".")
}
