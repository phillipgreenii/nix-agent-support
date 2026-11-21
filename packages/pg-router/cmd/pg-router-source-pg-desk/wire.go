// wire.go: this adapter's wire shapes. Input side: the pg-desk.changes/v1
// envelope (spec section 9.3), decoded with the published contract package
// packages/pg-desk/pkg/deskapi (the adapter imports nothing else of pg-desk).
// Output side: pg-router's per-record command-query
// source shape, the published api/event.Item.
package main

import (
	"io"

	apievent "github.com/phillipgreenii/pg-router/api/event"
)

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
