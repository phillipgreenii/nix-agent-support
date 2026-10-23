package feed

import (
	"context"

	shared "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/feed"
)

// Position is the state of one stored cursor: its view, scope and acknowledged
// sequence number, the head of the log, how many rows a poll would deliver after
// the position (LagRows), when the oldest of them was recorded (OldestUnacked,
// zero when none) and when the cursor was last seen.
type Position = shared.Position

// Positions lists every stored cursor. LagRows counts, without running any query,
// the rows a poll of the cursor would deliver after its position, filtered by the
// scope's stored members; Head is 0 for an empty log.
func (f *Feed) Positions(ctx context.Context) ([]Position, error) { return f.sh.Positions(ctx) }
