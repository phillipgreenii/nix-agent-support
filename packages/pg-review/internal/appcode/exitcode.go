// Package appcode holds the pg-review specific exit codes, in the app range
// of the shared table (github.com/phillipgreenii/x/exitcode, AppMin..AppMax).
// Everything with a shared meaning (usage, not found, config, busy) uses the
// shared package directly. The tool returns these codes in every context;
// mapping them to router semantics is the router adapter's job, and it
// duplicates Skipped (the adapter is a separate module).
package appcode

import "github.com/phillipgreenii/x/exitcode"

const (
	// Skipped is a skipped run (pending cap, ccpool usage limit) or an
	// already-done one (the run key already has a Filed run; no row is
	// written). A busy run lock is the shared Busy, not Skipped.
	Skipped = exitcode.AppMin + iota
	// Validation is a rejected validation: findings, a batch or a broker
	// request that the tool refused.
	Validation
	// Ledger is a ledger error or state conflict, including a failed
	// integrity check and a stale view.
	Ledger
	// Provider is a provider failure or timeout.
	Provider
)
