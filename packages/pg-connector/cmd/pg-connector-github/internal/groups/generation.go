package groups

import (
	"context"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

// GenStore is the part of the daemon store the generation helpers use;
// *store.Store satisfies it.
type GenStore interface {
	GetGroup(ctx context.Context, kind, id, group string) (store.GroupRow, bool, error)
	PutGroupIf(ctx context.Context, g store.GroupRow, expectGeneration int64) (bool, error)
	BumpGroup(ctx context.Context, kind, id, group string, freshUntil time.Time) (int64, error)
}

var _ GenStore = (*store.Store)(nil)

// ThenStore is GenStore plus the compare-and-swap that runs a callback in the
// same transaction as the write; *store.Store satisfies it.
type ThenStore interface {
	GenStore
	PutGroupIfThen(ctx context.Context, g store.GroupRow, expectGeneration int64, then func(tx *store.Tx) error) (bool, error)
}

var _ ThenStore = (*store.Store)(nil)

// Token is the generation a group row had when a refresh started. A Commit with
// it stores only if the row has not moved since.
type Token struct {
	Kind, ID, Group string
	// Generation is the stored generation when the refresh began; 0 when no row
	// existed.
	Generation int64
}

// Begin records the group row's current generation before a refresh fetches
// from origin. A refresh that began before a write (an Invalidate, or another
// refresh's Commit) loses its Commit.
func Begin(ctx context.Context, st GenStore, kind, id, group string) (Token, error) {
	g, ok, err := st.GetGroup(ctx, kind, id, group)
	if err != nil {
		return Token{}, err
	}
	tok := Token{Kind: kind, ID: id, Group: group}
	if ok {
		tok.Generation = g.Generation
	}
	return tok, nil
}

// Commit stores row, forcing its key to the token's and its generation to the
// token's plus one, only if the stored generation still equals the token's. It
// reports false when superseded: nothing was written, and the caller MUST NOT
// report a change from the content it fetched.
func Commit(ctx context.Context, st GenStore, tok Token, row store.GroupRow) (stored bool, err error) {
	row.Kind, row.ID, row.Group = tok.Kind, tok.ID, tok.Group
	row.Generation = tok.Generation + 1
	return st.PutGroupIf(ctx, row, tok.Generation)
}

// CommitThen is Commit, and ONLY when it stored the row it calls then in the
// same transaction (then may be nil). An error from then rolls the group write
// back and is returned with stored false. then MUST use only the transaction's
// own methods: the store's single connection is occupied by it.
func CommitThen(ctx context.Context, st ThenStore, tok Token, row store.GroupRow, then func(tx *store.Tx) error) (stored bool, err error) {
	row.Kind, row.ID, row.Group = tok.Kind, tok.ID, tok.Group
	row.Generation = tok.Generation + 1
	return st.PutGroupIfThen(ctx, row, tok.Generation, then)
}

// Invalidate marks the group stale after a write: it bumps the generation,
// keeps the content and sets fresh_until to now, so the next read goes to
// origin and any refresh begun earlier loses. A never-fetched non-summary group
// gets an empty row, so that a refresh begun before the write loses; a missing
// summary stays missing.
func Invalidate(ctx context.Context, st GenStore, kind, id, group string, now time.Time) error {
	_, err := st.BumpGroup(ctx, kind, id, group, now)
	return err
}
