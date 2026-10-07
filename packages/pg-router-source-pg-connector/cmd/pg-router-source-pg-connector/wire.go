// wire.go: this adapter's own output-side wire shape — pg-router's
// rawItem, confirmed by reading
// packages/pg-router/internal/query/command.go's rawItem struct during
// this packet's own curation pass [design: section 6.1]. This adapter
// emits id/type/title/metadata always. at/expiresAt (bead pg2-1ldvy) are
// emitted ONLY by "changes --retry-window <d>" with d > 0 and are omitted
// (omitempty) otherwise, so every other output stays byte-identical to what
// it was before they existed; emit stays absent (this adapter has no field
// for it).
package main

import (
	"encoding/json"
	"io"
)

// rawItem is pg-router's own per-record command-query source shape.
type rawItem struct {
	ID       string         `json:"id"`
	Type     string         `json:"type"`
	Title    string         `json:"title"`
	Metadata map[string]any `json:"metadata"`
	// At and ExpiresAt are RFC3339 strings; set only by changes with a
	// positive --retry-window (changes.go).
	At        string `json:"at,omitempty"`
	ExpiresAt string `json:"expiresAt,omitempty"`
}

// writeItems encodes items as one JSON array to stdout — every
// subcommand's own success path writes exactly one such array, never
// pg-connector's own wire envelope passed through unmodified [design:
// section 6.1]. items MUST be a non-nil (possibly empty) slice so a
// zero-match result still marshals as [] rather than null.
func writeItems(stdout io.Writer, items []rawItem) error {
	enc := json.NewEncoder(stdout)
	enc.SetEscapeHTML(false)
	return enc.Encode(items)
}
