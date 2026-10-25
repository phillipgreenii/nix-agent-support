package query

import (
	"context"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

// QueryRows is the part of the daemon store Members reads; *store.Store
// satisfies it.
type QueryRows interface {
	ListQueries(ctx context.Context, configuredOnly bool) ([]store.QueryRow, error)
}

var _ QueryRows = (*store.Store)(nil)

// Members is the set of entity ids (groups.FoldRef form) that are members of a
// stored CONFIGURED query row. The summary refresh uses it to skip the issues a
// query run refreshes, and the telemetry to count the tracked classes. An
// ad-hoc row and a row of a query the config no longer defines (demoted to not
// configured by the runner's Schedule) hold no members.
func Members(ctx context.Context, st QueryRows) (map[string]bool, error) {
	rows, err := st.ListQueries(ctx, true)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, row := range rows {
		for _, ref := range row.IDs {
			out[groups.FoldRef(ref)] = true
		}
	}
	return out, nil
}
