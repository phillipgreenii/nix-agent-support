package feed

import (
	"context"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/changes"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/entity"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/query"
	shared "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/feed"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
)

// Queries is what the feed needs of the query runner: the six methods of the
// shared interface (Known, Names, Watched, Members, Run, WatchedRefs).
type Queries = shared.Queries

// Members is the stored membership of a query. Refs are the wire references
// owner/repo#n in the stored row's order; Baseline is true once the query's
// membership baseline exists.
type Members = shared.Members

// runner is the part of *query.Runner the feed uses.
type runner interface {
	Expr(name string) (schema.QueryExpr, bool)
	// Names lists the configured query names; Watched reports a configured
	// query's watched flag (both are the daemon's config, query.Settings).
	Names() []string
	Watched(name string) bool
	Row(ctx context.Context, key query.Key) (store.QueryRow, bool, error)
	Fetch(ctx context.Context, key query.Key) (store.QueryRow, error)
}

var _ runner = (*query.Runner)(nil)

// RefLookup is the alias table as the feed uses it: the wire reference recorded
// for a node id. *entity.Identity satisfies it.
type RefLookup interface {
	Ref(ctx context.Context, nodeID string) (entity.Ref, bool, error)
}

var _ RefLookup = (*entity.Identity)(nil)

// queries is the Queries over a runner and the store.
type queries struct {
	r       runner
	st      *store.Store
	aliases RefLookup
	now     func() time.Time
}

// NewQueries returns the Queries over the runner r and the store st that holds
// its rows; aliases turns a watched entity's node id into its wire reference.
// Freshness is judged against the system clock.
func NewQueries(r *query.Runner, st *store.Store, aliases RefLookup) Queries {
	return &queries{r: r, st: st, aliases: aliases, now: time.Now}
}

func (q *queries) Known(name string) bool {
	_, ok := q.r.Expr(name)
	return ok
}

// Names lists the configured query names.
func (q *queries) Names() []string { return q.r.Names() }

// Watched reports whether the configured query name is a watched one.
func (q *queries) Watched(name string) bool { return q.r.Watched(name) }

// WatchedRefs lists the wire reference of every watched PR, in the order the
// flag was first set: the replay of an unscoped reset (a scoped reset replays the
// watched members of its query). A tombstoned PR, and a PR whose alias row is
// gone, is left out: there is no reference to replay it under.
func (q *queries) WatchedRefs(ctx context.Context) ([]string, error) {
	rows, err := q.st.ListWatched(ctx, changes.LogKindPR)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range rows {
		if !e.TombstonedAt.IsZero() {
			continue
		}
		ref, ok, err := q.aliases.Ref(ctx, e.ID)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, ref.String())
		}
	}
	return out, nil
}

func (q *queries) Members(ctx context.Context, name string) (Members, error) {
	row, ok, err := q.st.GetQuery(ctx, query.Key{Name: name}.String())
	if err != nil || !ok {
		return Members{}, err
	}
	return Members{Refs: row.IDs, Baseline: !row.BaselineAt.IsZero()}, nil
}

func (q *queries) Run(ctx context.Context, name string) error {
	key := query.Key{Name: name}
	row, ok, err := q.r.Row(ctx, key)
	if err != nil {
		return err
	}
	if ok && !row.FetchedAt.IsZero() && row.FreshUntil.After(q.now()) {
		return nil
	}
	_, err = q.r.Fetch(ctx, key)
	return err
}
