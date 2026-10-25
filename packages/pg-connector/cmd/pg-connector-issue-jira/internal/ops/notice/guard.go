package notice

import (
	"context"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/summary"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/groupkit"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/groupsvc"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

// GuardStore wraps the daemon store so that every commit of a group row goes
// through the hold: while the entity is held its PutGroupIf and PutGroupIfThen
// store NOTHING and report stored false (and PutGroupIfThen runs no callback,
// so no change row is appended). Everything else is the embedded store's.
//
// The daemon assembly passes it as summary.Persister.Store and as the Store of
// the detail service (the shared group service), the two writers of group rows.
type GuardStore struct {
	*store.Store
	table *Table
}

var (
	_ summary.Store      = (*GuardStore)(nil)
	_ groupsvc.Store     = (*GuardStore)(nil)
	_ groupkit.ThenStore = (*GuardStore)(nil)
)

// NewGuardStore returns the guard of st over the hold table t.
func NewGuardStore(st *store.Store, t *Table) *GuardStore {
	return &GuardStore{Store: st, table: t}
}

// PutGroupIf is store.Store.PutGroupIf, except that it stores nothing and reports
// stored false while the row's entity is held.
func (g *GuardStore) PutGroupIf(ctx context.Context, row store.GroupRow, expectGeneration int64) (bool, error) {
	if g.table.Held(row.ID) {
		return false, nil
	}
	return g.Store.PutGroupIf(ctx, row, expectGeneration)
}

// PutGroupIfThen is store.Store.PutGroupIfThen, except that it stores nothing,
// runs no callback and reports stored false while the row's entity is held.
func (g *GuardStore) PutGroupIfThen(ctx context.Context, row store.GroupRow, expectGeneration int64, then func(tx *store.Tx) error) (bool, error) {
	if g.table.Held(row.ID) {
		return false, nil
	}
	return g.Store.PutGroupIfThen(ctx, row, expectGeneration, then)
}
