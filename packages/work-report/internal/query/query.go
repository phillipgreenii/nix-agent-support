// Package query returns the resolved (latest-wins) entries of a work-report
// store for a filter. It is the one read path shared by the `query` verb and
// the reporting half, so neither holds a privilege the other lacks
// (ACTOR-READER).
package query

import (
	"context"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/work-report/internal/store"
)

// Run returns the entries matching f: one per id (the latest observation
// wins), ordered by occurrence time. Filter fields compose with AND across
// dimensions and OR within one. It builds nothing itself and delegates to the
// store.
func Run(ctx context.Context, st *store.Store, f store.Filter) ([]store.Entry, error) {
	return st.Query(ctx, f)
}
