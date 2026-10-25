package summary

import (
	"context"
	"fmt"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

// Sweep deletes every issue entity whose closed_at is set and at least expiry
// old at now, with its groups and its alias row, and returns how many it
// evicted. Deleting the row clears the watched flag, so the issue is a first
// observation again if it reappears. Change-log rows are not touched: they keep
// their own retention.
func Sweep(ctx context.Context, st *store.Store, expiry time.Duration, now time.Time) (evicted int, err error) {
	rows, err := st.ListEntities(ctx, groups.EntityKind)
	if err != nil {
		return evicted, err
	}
	for _, e := range rows {
		if e.ClosedAt.IsZero() || now.Before(e.ClosedAt.Add(expiry)) {
			continue
		}
		if err := st.DeleteEntity(ctx, groups.EntityKind, e.ID); err != nil {
			return evicted, err
		}
		if err := st.DeleteAlias(ctx, e.ID); err != nil {
			return evicted, fmt.Errorf("summary: sweep %s: %w", e.ID, err)
		}
		evicted++
	}
	return evicted, nil
}
