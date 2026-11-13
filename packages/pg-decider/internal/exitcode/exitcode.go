// Package exitcode holds pg-decider's process exit codes (entity-change-flow
// design 9.12, row `pg-decider plan|apply`). The shared codes come from the
// workspace's exit-code package (github.com/phillipgreenii/x/exitcode); the two
// the shared set has no meaning for are app codes in its 20-63 band.
package exitcode

import (
	"os"

	xexit "github.com/phillipgreenii/x/exitcode"
)

const (
	// OK: all actions applied, or none were needed.
	OK = xexit.OK
	// Usage: bad arguments (an unknown entity type, a wrong argument count, an
	// unknown flag).
	Usage = xexit.Usage
	// Failure: an error that is not one of the scheme's codes (an unreadable
	// --from-item, an unusable configuration, no decider for the type, a plan
	// that cannot be printed).
	Failure = xexit.Error
	// Partial: some actions failed; they are captured and retried by the
	// next run or sweep (the shared Degraded code).
	Partial = xexit.Degraded
	// ViewUnreadable: the composite view could not be read; nothing was
	// applied (the shared Unavailable code).
	ViewUnreadable = xexit.Unavailable
)

// The app codes.
const (
	// LookupUntrusted: a pr run's work-item lookup could not be trusted (the
	// listing was truncated or unavailable, the per-PR answer was
	// inconsistent, or the lookup query is not configured); nothing was written. The composed
	// command maps it to the class decide_failed, or to the retryable class transient
	// when the reason is lookup-unavailable.
	LookupUntrusted = xexit.AppMin
	// PartialPermanent: some actions failed, and every failure is a refusal a
	// retry cannot clear (the connector answered invalid_argument, e.g. another
	// actor holds the item's claim). Nothing is rolled back. The composed
	// command reports it as a decide_partial failure the router does not retry.
	PartialPermanent = xexit.AppMin + 1
)

// osExit is os.Exit, swapped by tests.
var osExit = os.Exit

// Exit terminates the process with code.
func Exit(code int) { osExit(code) }
