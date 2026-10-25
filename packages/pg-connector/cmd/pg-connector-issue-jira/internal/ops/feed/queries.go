package feed

import (
	"context"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/groups"
	libfeed "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/feed"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	lq "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/query"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

// runner is the part of *lq.Runner the adapter uses.
type runner interface {
	Expr(name string) ([]string, bool)
	Names() []string
	Watched(name string) bool
	Row(ctx context.Context, key lq.Key) (store.QueryRow, bool, error)
	Fetch(ctx context.Context, key lq.Key, opts ...lq.FetchOptions) (store.QueryRow, error)
}

var _ runner = (*lq.Runner)(nil)

// queryStore is the part of the daemon store the adapter reads.
type queryStore interface {
	GetQuery(ctx context.Context, key string) (store.QueryRow, bool, error)
	ListWatched(ctx context.Context, kind string) ([]store.EntityRow, error)
}

var _ queryStore = (*store.Store)(nil)

type queries struct {
	r   runner
	st  queryStore
	clk governor.Clock
}

// Option adjusts NewQueries.
type Option func(*queries)

// WithClock sets the clock freshness is judged against; the default is the
// system clock.
func WithClock(c governor.Clock) Option {
	return func(q *queries) {
		if c != nil {
			q.clk = c
		}
	}
}

// NewQueries returns the feed's Queries over the query runner and the store
// that holds its rows. Known, Names and Watched come from the runner's
// settings; Members is the stored configured query row with its baseline flag;
// Run makes the stored result fresh and re-runs the query only when it is
// stale; WatchedRefs lists every watched, visible issue.
func NewQueries(r *lq.Runner, st *store.Store, opts ...Option) libfeed.Queries {
	return newQueries(r, st, opts...)
}

func newQueries(r runner, st queryStore, opts ...Option) *queries {
	q := &queries{r: r, st: st, clk: governor.SystemClock{}}
	for _, o := range opts {
		o(q)
	}
	return q
}

func (q *queries) Known(name string) bool {
	_, ok := q.r.Expr(name)
	return ok
}

func (q *queries) Names() []string { return q.r.Names() }

func (q *queries) Watched(name string) bool { return q.r.Watched(name) }

func (q *queries) Members(ctx context.Context, name string) (libfeed.Members, error) {
	row, ok, err := q.st.GetQuery(ctx, lq.Key{Name: name}.String())
	if err != nil || !ok {
		return libfeed.Members{}, err
	}
	return libfeed.Members{Refs: row.IDs, Baseline: !row.BaselineAt.IsZero()}, nil
}

// Run leaves a fresh stored result alone and otherwise re-runs the query now.
func (q *queries) Run(ctx context.Context, name string) error {
	key := lq.Key{Name: name}
	row, ok, err := q.r.Row(ctx, key)
	if err != nil {
		return err
	}
	if ok && !row.FetchedAt.IsZero() && row.FreshUntil.After(q.clk.Now()) {
		return nil
	}
	_, err = q.r.Fetch(ctx, key)
	return err
}

// WatchedRefs lists the reference of every watched issue that is not
// tombstoned, in the order the store lists them. A Jira key is its own stable
// id, so the reference is the id.
func (q *queries) WatchedRefs(ctx context.Context) ([]string, error) {
	rows, err := q.st.ListWatched(ctx, groups.EntityKind)
	if err != nil {
		return nil, err
	}
	refs := make([]string, 0, len(rows))
	for _, e := range rows {
		if !e.TombstonedAt.IsZero() {
			continue
		}
		refs = append(refs, e.ID)
	}
	return refs, nil
}
