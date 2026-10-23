package groups

import (
	"context"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/groupkit"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

// The generation-checked write path lives in the shared groupkit package; these
// names keep the GitHub daemon's existing call sites unchanged.

// GenStore is the part of the daemon store the generation helpers use.
type GenStore = groupkit.GenStore

// ThenStore is GenStore plus the compare-and-swap that runs a callback in the
// same transaction as the write.
type ThenStore = groupkit.ThenStore

// Token is the generation a group row had when a refresh started.
type Token = groupkit.Token

// Begin records the group row's current generation before a refresh fetches
// from origin; see groupkit.Begin.
func Begin(ctx context.Context, st GenStore, kind, id, group string) (Token, error) {
	return groupkit.Begin(ctx, st, kind, id, group)
}

// Commit stores row only if the stored generation still equals the token's; see
// groupkit.Commit.
func Commit(ctx context.Context, st GenStore, tok Token, row store.GroupRow) (stored bool, err error) {
	return groupkit.Commit(ctx, st, tok, row)
}

// CommitThen is Commit plus a callback run in the same transaction; see
// groupkit.CommitThen.
func CommitThen(ctx context.Context, st ThenStore, tok Token, row store.GroupRow, then func(tx *store.Tx) error) (stored bool, err error) {
	return groupkit.CommitThen(ctx, st, tok, row, then)
}

// Invalidate marks the group stale after a write; see groupkit.Invalidate.
func Invalidate(ctx context.Context, st GenStore, kind, id, group string, now time.Time) error {
	return groupkit.Invalidate(ctx, st, kind, id, group, now)
}
