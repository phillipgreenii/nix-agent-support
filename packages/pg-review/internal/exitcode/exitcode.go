// Package exitcode is the single exit-code table of pg-review. The tool
// returns these codes in every context; mapping them to router semantics is
// the router adapter's job.
package exitcode

const (
	// OK is success.
	OK = 0
	// Failure is an unexpected failure with no more specific code. Stub
	// commands that are not implemented yet also exit with it.
	Failure = 1
	// Usage is a usage error (a bad flag, argument or value).
	Usage = 2
	// Skipped is a skipped run (pending cap, ccpool usage limit or ccpool
	// busy) or an already-done one (the run key already has a Filed run; no
	// row is written).
	Skipped = 3
	// ValidationRejected is a rejected validation, or not found (an unknown or
	// expired show-run id).
	ValidationRejected = 4
	// Busy means the run lock is busy, and nothing else.
	Busy = 5
	// Ledger is a ledger error or state conflict, including a failed
	// integrity check and a stale view.
	Ledger = 6
	// Provider is a provider failure or timeout.
	Provider = 7
	// Reserved is reserved and never returned until a design assigns it.
	Reserved = 8
	// Unused is never used, by design.
	Unused = 9
)
