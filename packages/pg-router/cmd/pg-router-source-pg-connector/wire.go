// wire.go: this adapter's own output-side wire shape — pg-router's
// published per-record command-query source shape, api/event.Item
// [design: section 6.1]. This adapter
// emits id/type/title/metadata always. at/expiresAt (bead pg2-1ldvy) are
// emitted ONLY by "changes --retry-window <d>" with d > 0 and are omitted
// (omitempty) otherwise, so every other output stays byte-identical to what
// it was before they existed; emit stays absent (this adapter has no field
// for it).
package main

import (
	"io"

	apievent "github.com/phillipgreenii/pg-router/api/event"
)

// writeItems encodes items as one JSON array to stdout — every
// subcommand's own success path writes exactly one such array, never
// pg-connector's own wire envelope passed through unmodified [design:
// section 6.1]. items MUST be a non-nil (possibly empty) slice so a
// zero-match result still marshals as [] rather than null.
func writeItems(stdout io.Writer, items []apievent.Item) error {
	b, err := apievent.EncodeItems(items)
	if err != nil {
		return err
	}
	_, err = stdout.Write(b)
	return err
}
