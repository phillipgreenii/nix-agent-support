package main

import "github.com/phillipgreenii/x/exitcode"

// App-specific exit codes of the ccpool CLI, in the shared app range 20..63
// (see the exitcode package). Outcomes a shared code covers use it directly:
// exitcode.Timeout for a reply that timed out, exitcode.NotFound for an unknown
// session. A caller (pg-router-handlers, pg-review) branches on these numbers,
// so they are pinned by tests on both sides and documented in
// docs/behavior/interfaces.md. No refusal maps to the shared Busy code 9.
const (
	// exitUsageLimited: `new` and `reply` refused work because an account usage
	// window is at its limit.
	exitUsageLimited = exitcode.AppMin // 20
	// exitNeedsInput: reply's turn stopped waiting for human input.
	exitNeedsInput = 21
	// exitTurnFailed: reply's turn ended in the errored state.
	exitTurnFailed = 22
	// exitSessionBusy: reply refused because the session is working.
	exitSessionBusy = 23
	// exitCancelUnconfirmed: cancel (or reply --interrupt) could not confirm the
	// interrupt landed.
	exitCancelUnconfirmed = 24
	// exitPromptNotIngested: a confirmed-dropped prompt (--confirm-ingest window
	// elapsed with no model turn).
	exitPromptNotIngested = 25
	// exitIngestUnknown: the recorder could not say whether the prompt was
	// ingested; the prompt was delivered and the model may be working.
	exitIngestUnknown = 26
	// exitResultPending: result's turn has not completed yet.
	exitResultPending = 27
)
