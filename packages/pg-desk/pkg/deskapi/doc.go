// Package deskapi is the published contract of pg-desk's two machine-readable
// outputs: the composite view (contract pg-desk.view/v1, printed by
// `pg-desk <type> show --json`) and the change envelope (contract
// pg-desk.changes/v1, printed by `pg-desk <type> changes --json`).
//
// It is a contract package: it holds the message types, the constants and the
// codec and nothing else. It imports the standard library only; the closure
// test fails when that stops being true, so a consumer in another module gains
// no dependency by importing it. The JSON Schemas under schemas/ stay
// hand-written; the both-ways test keeps the types, the schemas and the golden
// files under testdata/golden/ in step. Consumers read the golden files in
// place by relative path and never copy them.
//
// Evolution is additive only: a new member is optional, and an absent value
// reads as the earlier behavior.
package deskapi
