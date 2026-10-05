// Package exitcode holds pg-decider's process exit codes (entity-change-flow
// design 9.12, row `pg-decider plan|apply`).
package exitcode

import "os"

const (
	// OK: all actions applied, or none were needed.
	OK = 0
	// Failure: a usage or other error that is not one of the scheme's
	// codes (bad arguments, unreadable --from-item, a seam not yet
	// implemented).
	Failure = 1
	// Partial: some actions failed; they are captured and retried by the
	// next run or sweep.
	Partial = 2
	// ViewUnreadable: the composite view could not be read; nothing was
	// applied.
	ViewUnreadable = 3
)

// osExit is os.Exit, swapped by tests.
var osExit = os.Exit

// Exit terminates the process with code.
func Exit(code int) { osExit(code) }
