// wire.go: this adapter's wire shapes. Input side: the pg-desk.changes/v1
// envelope (spec section 9.3), decoded generically with this module's own
// structs — this is a SEPARATE Go module and never imports
// packages/pg-desk. Output side: pg-router's per-record command-query
// source shape (rawItem in packages/pg-router/internal/query/command.go),
// mirrored from packages/pg-router-source-pg-connector's wire.go.
package main

import (
	"encoding/json"
	"io"
)

// envelope is the subset of the change envelope this adapter consumes. The
// envelope's cursor and at fields are not used: the adapter is a stateless
// translator.
type envelope struct {
	Contract string      `json:"contract"`
	Sources  []envSource `json:"sources"`
	Records  []envRecord `json:"records"`
}

type envSource struct {
	Query  string `json:"query"`
	Status string `json:"status"`
}

type envRecord struct {
	Seq     json.Number `json:"seq"`
	Type    string      `json:"type"`
	ID      string      `json:"id"`
	Title   string      `json:"title"`
	Version json.Number `json:"version"`
	Kinds   []string    `json:"kinds"`
	Origin  string      `json:"origin"`
}

// rawItem is pg-router's own per-record command-query source shape.
type rawItem struct {
	ID       string         `json:"id"`
	Type     string         `json:"type"`
	Title    string         `json:"title"`
	Metadata map[string]any `json:"metadata"`
}

// writeItems encodes items as one JSON array to stdout. items MUST be a
// non-nil (possibly empty) slice so zero items marshals as [] not null.
func writeItems(stdout io.Writer, items []rawItem) error {
	enc := json.NewEncoder(stdout)
	enc.SetEscapeHTML(false)
	return enc.Encode(items)
}
