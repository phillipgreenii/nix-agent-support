package summary

import (
	"context"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

// Counts are the tracked-entity classes and the watched count, the numbers
// behind the metrics tracked.entities{class} and watched.entities.
type Counts struct {
	// Members are the open, visible issues that are members of a configured query.
	Members int
	// OtherOpen are the open, visible issues that are not (tracked by a read or a
	// watch alone).
	OtherOpen int
	// ClosedGrace are the visible issues closed for less than the terminal TTL.
	ClosedGrace int
	// Watched are the entities whose watched flag is set.
	Watched int
}

// Classes counts the issue entities. members holds the ids (groups.FoldRef
// form) of the issues listed by a configured query. A tombstoned entity and an
// issue closed for the terminal TTL or longer are no longer tracked and are in
// no class; Watched counts every entity with the flag set, as
// Store.CountWatched does.
func Classes(ctx context.Context, st *store.Store, members map[string]bool, now time.Time, terminalTTL time.Duration) (Counts, error) {
	rows, err := st.ListEntities(ctx, groups.EntityKind)
	if err != nil {
		return Counts{}, err
	}
	var c Counts
	for _, e := range rows {
		if !e.WatchedAt.IsZero() {
			c.Watched++
		}
		if !e.TombstonedAt.IsZero() {
			continue
		}
		switch {
		case !e.ClosedAt.IsZero():
			if now.Before(e.ClosedAt.Add(terminalTTL)) {
				c.ClosedGrace++
			}
		case members[e.ID]:
			c.Members++
		default:
			c.OtherOpen++
		}
	}
	return c, nil
}
