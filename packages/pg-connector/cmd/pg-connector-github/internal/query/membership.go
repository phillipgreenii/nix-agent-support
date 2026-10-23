package query

import (
	"context"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/changes"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/engine"
	shared "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/query"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

// Changes is the part of the GitHub change engine the runner uses to record a
// membership change; *changes.Engine satisfies it. The call runs inside the
// transaction that stores the run's result, so the row and the result are
// visible together or not at all.
type Changes interface {
	AppendMembership(ctx context.Context, tx *store.Tx, m changes.Membership) error
}

var _ Changes = (*changes.Engine)(nil)

// changesAdapter is the shared query.Changes over a GitHub Changes facade.
type changesAdapter struct{ c Changes }

var _ shared.Changes = changesAdapter{}

func (a changesAdapter) AppendMembership(ctx context.Context, tx *store.Tx, m engine.Membership) error {
	return a.c.AppendMembership(ctx, tx, changes.Membership{
		Kind: m.Kind, Query: m.Query, Node: m.ID, Ref: m.Ref, Extra: m.Extra,
	})
}

// StateFieldPrefix starts the name of the extra field a left_query row carries:
// the field "query_state:<query>" whose After is the confirming read's State (a
// JSON string such as "MERGED"), so a consumer can tell a PR that closed or
// merged from one that was filtered out of the query.
const StateFieldPrefix = "query_state:"
