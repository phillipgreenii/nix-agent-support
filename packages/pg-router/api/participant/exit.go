// Package participant is the published contract between the router core and its
// participants (handlers and sources): the request and reply messages of the
// dispatch and lifecycle subcommands, the report a handler returns for one
// dispatch with the outcome codec that carries it, the decline kinds of a
// pre-accept decline, the participant lifecycle states and the shared exit
// codes.
//
// It is a contract package: it holds the message types, the constants and the
// codec and nothing else. It imports the standard library and the shared exit
// codes of x/exitcode only; the closure test fails when that stops being true.
// The JSON Schemas under schemas/ stay hand-written; the both-ways test in this
// package keeps the types and the schemas in step. The schema-version constant
// exists once, in api/status; a message type here carries it as a string field
// the sender fills.
package participant

import "github.com/phillipgreenii/x/exitcode"

// Coarse exit codes of a participant call. They re-export the workspace's shared
// codes (package exitcode in the x module) and define no new one, so a caller
// reads the same meaning from the router as from every other tool (ADR 0127).
// ExitBusy is a participant's pre-accept capacity decline and the core's own read
// refusal; ExitUnavailable is a caller that found no running core; ExitConfig is
// a config or startup precheck failing before any work begins. A participant is
// free to emit any of the shared codes: none is reserved to the core.
const (
	ExitOK          = exitcode.OK          // ok; rich outcome in the JSON reply
	ExitError       = exitcode.Error       // unexpected error: NOT a usage error, which is ExitUsage
	ExitUsage       = exitcode.Usage       // usage error: a bad flag, or a missing/unexpected argument
	ExitUnavailable = exitcode.Unavailable // no running core, or its socket does not answer
	ExitConfig      = exitcode.Config      // a config or startup precheck failed before any work
	ExitBusy        = exitcode.Busy        // at capacity: pre-accept busy decline (no body required)
)
