package prview

import "encoding/json"

// MarshalView renders v as its machine-readable JSON representation: one
// indented JSON document with every axis present as an explicit key (see the
// package doc's "Explicit markers, never omitted keys"). View's own field
// tags already carry no `omitempty` anywhere, so this is a thin, single
// entry point over the stdlib encoder rather than a bespoke encoding — its
// purpose is to give the `--json` contract exactly one call site to route
// through (mirroring cmd/pg-pr/worktree.go's writeJSON convention: 2-space
// indent), so a later CLI-wiring change (a separate sibling bead) has one
// function to call instead of re-deciding indent/marshal behavior itself.
//
// MarshalView performs no IO — it is a pure transform, consistent with
// Assemble's own "no IO calls of any kind" contract.
func MarshalView(v View) ([]byte, error) {
	return json.MarshalIndent(v, "", "  ")
}
