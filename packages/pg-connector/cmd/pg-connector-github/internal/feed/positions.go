package feed

import (
	"context"
	"time"
)

// Position is the state of one stored key: its acknowledged sequence number, the
// head of the log, how many rows a poll would deliver after the position
// (LagRows), when the oldest of them was recorded (OldestUnacked, zero when
// none) and when the key was last seen.
type Position struct {
	Consumer, Kind, Query string
	AckedSeq, Head        int64
	LagRows               int
	OldestUnacked, SeenAt time.Time
}

// Positions lists every stored key. LagRows counts, without running any query,
// the rows a poll of the key would deliver after its position, filtered by the
// query's stored members; Head is 0 for an empty log.
func (f *Feed) Positions(ctx context.Context) ([]Position, error) {
	ps, err := f.sh.Positions(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Position, 0, len(ps))
	for _, p := range ps {
		out = append(out, Position{
			Consumer: p.Consumer, Kind: p.View, Query: p.Scope,
			AckedSeq: p.AckedSeq, Head: p.Head, LagRows: p.LagRows,
			OldestUnacked: p.OldestUnacked, SeenAt: p.SeenAt,
		})
	}
	return out, nil
}
