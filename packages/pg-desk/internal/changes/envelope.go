// Package changes holds the wire types of contract pg-desk.changes/v1 — the
// change envelope pg-desk hands to a consumer (design 9.3) — and the small
// pure helpers shared by every verb that renders change records. The wire types
// are the published contract package's (pkg/deskapi); the aliases keep this
// package's names.
package changes

import "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/pkg/deskapi"

// Contract is the contract string every envelope carries.
const Contract = deskapi.ChangesContract

// Source status values [design 9.3].
const (
	StatusOK       = deskapi.StatusOK
	StatusDegraded = deskapi.StatusDegraded
	StatusFailed   = deskapi.StatusFailed
)

type (
	// Cursor is the consumer's change_log position around one call.
	Cursor = deskapi.Cursor
	// Source is one watched query the call consulted.
	Source = deskapi.Source
	// Record is one change record.
	Record = deskapi.Record
	// Envelope is the body of one `pg-desk <type> changes` call.
	Envelope = deskapi.Changes
)
