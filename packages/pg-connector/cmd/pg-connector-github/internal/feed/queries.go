package feed

import (
	"context"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/query"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
)

// Queries is what the feed needs of the query runner.
type Queries interface {
	// Known reports whether name is a query the daemon is configured with.
	Known(name string) bool
	// Members returns the stored result of the configured query name.
	Members(ctx context.Context, name string) (Members, error)
	// Run makes the query's stored result fresh, re-running it only when it is
	// stale, and returns the error of a failed run.
	Run(ctx context.Context, name string) error
}

// Members is the stored membership of a query. Refs are the wire references
// owner/repo#n in the stored row's order; Baseline is true once the query's
// membership baseline exists.
type Members struct {
	Refs     []string
	Baseline bool
}

// runner is the part of *query.Runner the feed uses.
type runner interface {
	Expr(name string) (schema.QueryExpr, bool)
	Row(ctx context.Context, key query.Key) (store.QueryRow, bool, error)
	Fetch(ctx context.Context, key query.Key) (store.QueryRow, error)
}

var _ runner = (*query.Runner)(nil)

// queries is the Queries over a runner and the store.
type queries struct {
	r   runner
	st  *store.Store
	now func() time.Time
}

// NewQueries returns the Queries over the runner r and the store st that holds
// its rows. Freshness is judged against the system clock.
func NewQueries(r *query.Runner, st *store.Store) Queries {
	return &queries{r: r, st: st, now: time.Now}
}

func (q *queries) Known(name string) bool {
	_, ok := q.r.Expr(name)
	return ok
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
