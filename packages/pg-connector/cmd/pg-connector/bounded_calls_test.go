package main

import (
	"testing"

	"github.com/phillipgreenii/x/cmdruntest"
)

// TestEveryCmdrunCallIsBounded is the bounded-call guard: no non-test
// cmdrun.Call literal anywhere in this module may lack a Timeout (or a
// //cmdrun:unbounded <reason> annotation), so a hung child cannot hold a call
// open forever.
func TestEveryCmdrunCallIsBounded(t *testing.T) {
	cmdruntest.AssertCallsBounded(t, "../..")
}
