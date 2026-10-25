package summary

import (
	"context"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/groupsvc"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

// KeepAliveStore is the part of the daemon store KeepAlive uses; *store.Store
// satisfies it.
type KeepAliveStore interface {
	GetEntity(ctx context.Context, kind, id string) (store.EntityRow, bool, error)
	TouchEntity(ctx context.Context, kind, id string, at time.Time) error
}

var _ KeepAliveStore = (*store.Store)(nil)

// KeepAlive is the daemon's retention rule, "tracked until closed for 24
// hours": every retrieved issue, whether a query member, read by a caller or
// watched, is refreshed in the background until it has been closed for
// TerminalTTL, and a direct read follows the same rule as a query result.
type KeepAlive struct {
	Store KeepAliveStore
	// Clock is the time source; nil means the system clock.
	Clock governor.Clock
	// TerminalTTL is terminal_ttl: how long an issue stays tracked after it
	// closed.
	TerminalTTL time.Duration
}

var _ groupsvc.KeepAlive = KeepAlive{}

// Retain is true when the issue's entity exists, is not tombstoned and either
// has no closed-at (an open issue, however long ago it was read) or is still
// inside TerminalTTL of it.
func (k KeepAlive) Retain(ctx context.Context, id string) (bool, error) {
	row, ok, err := k.Store.GetEntity(ctx, groups.EntityKind, groups.FoldRef(id))
	if err != nil || !ok || !row.TombstonedAt.IsZero() {
		return false, err
	}
	if row.ClosedAt.IsZero() {
		return true, nil
	}
	return k.now().Before(row.ClosedAt.Add(k.TerminalTTL)), nil
}

// Access records that the issue was read: last_access is now and keep_alive is
// set, creating the entity row when there is none.
func (k KeepAlive) Access(ctx context.Context, id string) error {
	return k.Store.TouchEntity(ctx, groups.EntityKind, groups.FoldRef(id), k.now())
}

func (k KeepAlive) now() time.Time {
	if k.Clock == nil {
		return time.Now()
	}
	return k.Clock.Now()
}
