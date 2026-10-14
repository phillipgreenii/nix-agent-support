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
	keys, err := f.cfg.Store.ListConsumers(ctx)
	if err != nil {
		return nil, err
	}
	head, err := f.cfg.Store.ChangeHead(ctx)
	if err != nil {
		return nil, err
	}
	sets := map[string]memberSet{}
	out := make([]Position, 0, len(keys))
	for _, c := range keys {
		p := Position{Consumer: c.Consumer, Kind: c.Kind, Query: c.Query, AckedSeq: c.AckedSeq, Head: head, SeenAt: c.SeenAt}
		if c.AckedSeq < head {
			set, ok := sets[c.Query]
			if !ok {
				if f.cfg.Queries.Known(c.Query) {
					m, err := f.cfg.Queries.Members(ctx, c.Query)
					if err != nil {
						return nil, err
					}
					set = newMemberSet(m.Refs)
				}
				sets[c.Query] = set
			}
			rows, _, err := f.scan(ctx, c.Kind, c.Query, c.AckedSeq, head, 0, set)
			if err != nil {
				return nil, err
			}
			p.LagRows = len(rows)
			for _, r := range rows {
				if p.OldestUnacked.IsZero() || r.At.Before(p.OldestUnacked) {
					p.OldestUnacked = r.At
				}
			}
		}
		out = append(out, p)
	}
	return out, nil
}
