package feed

import (
	"context"
	"time"
)

// Position is the state of one stored cursor: its acknowledged sequence number,
// the head of the log, how many rows a poll would deliver after the position
// (LagRows), when the oldest of them was recorded (OldestUnacked, zero when
// none) and when the cursor was last seen.
type Position struct {
	Consumer, View, Scope string
	AckedSeq, Head        int64
	LagRows               int
	OldestUnacked, SeenAt time.Time
}

// Positions lists every stored cursor. LagRows counts, without running any
// query, the rows a poll of the cursor would deliver after its position,
// filtered by the scope's stored members; Head is 0 for an empty log. A cursor
// of a view that is no longer registered counts no lag.
func (f *Feed) Positions(ctx context.Context) ([]Position, error) {
	cursors, err := f.cfg.Store.ListConsumers(ctx)
	if err != nil {
		return nil, err
	}
	head, err := f.cfg.Store.ChangeHead(ctx)
	if err != nil {
		return nil, err
	}
	sets := map[string]memberSet{}
	out := make([]Position, 0, len(cursors))
	for _, c := range cursors {
		p := Position{Consumer: c.Consumer, View: c.Kind, Scope: c.Query, AckedSeq: c.AckedSeq, Head: head, SeenAt: c.SeenAt}
		view, verr := f.resolveView(c.Kind)
		if c.AckedSeq < head && verr == nil {
			set, ok := sets[c.Query]
			if !ok {
				set = newMemberSet(nil, f.cfg.Spec.FoldRef)
				if f.cfg.Queries.Known(c.Query) {
					m, err := f.cfg.Queries.Members(ctx, c.Query)
					if err != nil {
						return nil, err
					}
					set = newMemberSet(m.Refs, f.cfg.Spec.FoldRef)
				}
				sets[c.Query] = set
			}
			rows, _, err := f.scan(ctx, view, c.Query, c.AckedSeq, head, 0, set)
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
