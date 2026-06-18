// Package item holds pr-pool's generalized unit of work. A query yields Items;
// bead-backed queries map beads.Issue -> Item, command/future queries build it
// from their own source. Metadata carries source-specific fields exposed to prompt
// interpolation. Status/labels/created-by are NOT carried here — flows re-fetch
// them by ID (DoneSignal reads bd status; the created-marker diff reads bd list).
// This package is a leaf: it imports nothing in-repo (keeps the import DAG acyclic).
package item

type Item struct {
	ID       string
	Type     string
	Title    string
	Metadata map[string]any
}
