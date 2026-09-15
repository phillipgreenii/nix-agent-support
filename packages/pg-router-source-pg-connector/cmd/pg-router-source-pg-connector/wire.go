// wire.go: this adapter's own output-side wire shape — pg-router's
// rawItem, confirmed by reading
// packages/pg-router/internal/query/command.go's rawItem struct during
// this packet's own curation pass [design: section 6.1]. This adapter
// emits only id/type/title/metadata: at/expiresAt/emit stay absent
// (omitted by pg-router's own omitempty on decode) simply by never having
// a field for them here at all.
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
