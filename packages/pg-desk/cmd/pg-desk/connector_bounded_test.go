package main

import (
	"testing"

	"github.com/phillipgreenii/x/cmdruntest"
)

// Every cmdrun.Call this module builds MUST carry a Timeout (or an explicit
// //cmdrun:unbounded reason), so a wedged connector cannot hold a verb open.
// The guard parses the non-test sources; it starts no process.
func TestEveryConnectorCallIsBounded(t *testing.T) {
	cmdruntest.AssertCallsBounded(t, "../..")
}
