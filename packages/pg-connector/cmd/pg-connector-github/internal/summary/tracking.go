package summary

import (
	"context"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/entity"
)

// Counts are the tracked-entity classes and the watched count: the numbers
// behind the metrics tracked.entities{class} and watched.entities.
type Counts struct {
	// Members are the PRs that a configured query lists.
	Members int
	// OtherOpen are the PRs tracked by a read or a watch alone: not listed by a
	// configured query, and not closed or merged. Members and OtherOpen together
	// are the keep-alive set, the PRs the daemon refreshes in the background.
	OtherOpen int
	// ClosedGrace are the closed or merged PRs no configured query lists, that
	// the store still holds until terminal_ttl and then expiry take them: they are
	// no longer tracked, and are counted so a closed PR's wait is visible.
	ClosedGrace int
	// Watched are the entities whose watched flag is set, whatever else is true
	// of them (as Store.CountWatched counts them).
	Watched int
}

// Tracked is the size of the keep-alive set: Members plus OtherOpen.
func (c Counts) Tracked() int { return c.Members + c.OtherOpen }

// Classes counts the PR entities by tracking class. A tombstoned entity is in
// no class (GitHub no longer returns it); Watched counts it all the same when
// its flag is set. It reads the store only and calls no origin.
func (s *Service) Classes(ctx context.Context) (Counts, error) {
	rows, err := s.cfg.Store.ListEntities(ctx, entity.KindPR)
	if err != nil {
		return Counts{}, err
	}
	members, err := s.members(ctx)
	if err != nil {
		return Counts{}, err
	}
	var c Counts
	for _, row := range rows {
		if !row.WatchedAt.IsZero() {
			c.Watched++
		}
		if !row.TombstonedAt.IsZero() {
			continue
		}
		if members[row.ID] {
			c.Members++
			continue
		}
		closed, err := s.closed(ctx, row.ID)
		if err != nil {
			return Counts{}, err
		}
		if closed {
			c.ClosedGrace++
		} else {
			c.OtherOpen++
		}
	}
	return c, nil
}

// InKeepAliveSet reports, for each entity (node id) of ids the store holds,
// whether it is in the keep-alive set (InKeepAlive's rule). It computes the
// configured queries' members once for the whole list, so a check over hundreds
// of PRs costs one membership read. An id the store has no entity for is left
// out of the answer.
func (s *Service) InKeepAliveSet(ctx context.Context, ids []string) (map[string]bool, error) {
	members, err := s.members(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(ids))
	for _, id := range ids {
		row, ok, err := s.cfg.Store.GetEntity(ctx, entity.KindPR, id)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		in, err := s.inSet(ctx, row, members)
		if err != nil {
			return nil, err
		}
		out[id] = in
	}
	return out, nil
}
