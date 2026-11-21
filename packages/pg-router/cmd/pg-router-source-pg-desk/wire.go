// wire.go: this adapter's wire shapes. Input side: the pg-desk.changes/v1
// envelope (spec section 9.3), decoded generically with this module's own
// structs — this is a SEPARATE Go module and never imports
// packages/pg-desk. Output side: pg-router's per-record command-query
// source shape, the published api/event.Item.
package main

import (
	"encoding/json"
	"io"

	apievent "github.com/phillipgreenii/pg-router/api/event"
)

// envelope is the subset of the change envelope this adapter consumes. The
// envelope's cursor and at fields are not used: the adapter is a stateless
// translator.
type envelope struct {
	Contract string      `json:"contract"`
	Cursor   envCursor   `json:"cursor"`
	Sources  []envSource `json:"sources"`
	Records  []envRecord `json:"records"`
}

// envCursor is the envelope's cursor; under --no-ack To is the position the
// caller acknowledges.
type envCursor struct {
	From json.Number `json:"from"`
	To   json.Number `json:"to"`
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

// writeItems encodes items as one JSON array to stdout. items MUST be a
// non-nil (possibly empty) slice so zero items marshals as [] not null.
func writeItems(stdout io.Writer, items []apievent.Item) error {
	b, err := apievent.EncodeItems(items)
	if err != nil {
		return err
	}
	_, err = stdout.Write(b)
	return err
}
