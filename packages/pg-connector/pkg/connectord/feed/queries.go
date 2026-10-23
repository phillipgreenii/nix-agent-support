package feed

import "context"

// Queries is what the feed needs of the connector's query runner.
type Queries interface {
	// Known reports whether name is a query the daemon is configured with.
	Known(name string) bool
	// Names lists the configured query names.
	Names() []string
	// Watched reports whether the configured query name is a watched one.
	Watched(name string) bool
	// Members returns the stored result of the configured query name.
	Members(ctx context.Context, name string) (Members, error)
	// Run makes the query's stored result fresh, re-running it only when it is
	// stale, and returns the error of a failed run.
	Run(ctx context.Context, name string) error
	// WatchedRefs lists every watched entity's wire reference, for an unscoped
	// reset.
	WatchedRefs(ctx context.Context) ([]string, error)
}

// Members is the stored membership of a query. Refs are the wire references in
// the stored row's order; Baseline is true once the query's membership baseline
// exists.
type Members struct {
	Refs     []string
	Baseline bool
}
