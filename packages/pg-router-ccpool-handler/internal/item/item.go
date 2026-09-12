// Package item holds pg-router's generalized unit of work. A query yields Items;
// bead-backed queries map beads.Issue -> Item, command/future queries build it
// from their own source. Metadata carries source-specific fields exposed to prompt
// interpolation. Status/labels/created-by are NOT carried here — flows re-fetch
// them by ID (DoneSignal reads bd status; the created-marker diff reads bd list).
// This package is a leaf: it imports nothing in-repo (keeps the import DAG acyclic).
//
// Duplicated verbatim from packages/pg-router/internal/item (still needed
// there by its own query/discover packages, which stay in-core): a
// zero-dependency leaf, so a byte-for-byte copy costs nothing and neither
// side may import the other's internal/* (docket pg2-oju6w Task 5.2/5.3,
// folded per the operator's 2026-09-11 decision; docs/adr/0065's Addendum).
// This module's own dispatch handling (cmd/pg-router-ccpool-handler/dispatch.go)
// decodes an Item from the wire event's opaque payload using the SAME field
// names pg-router's own (now-deleted-here) discover.ItemFromPayload used —
// Task 5.6's "same shape, different side" framing.
package item

type Item struct {
	ID       string
	Type     string
	Title    string
	Metadata map[string]any
}
