// Package query is pr-pool's typed-union of work sources. Each concrete query
// returns []item.Item; bead-backed queries map beads.Issue -> Item. Run errors are
// propagated, never returned as "no work" (pg2-qq9v): a bd/exec failure must not
// masquerade as an idle pool.
package query

import (
	"context"

	"github.com/phillipgreenii/pr-pool/internal/beads"
	"github.com/phillipgreenii/pr-pool/internal/item"
)

type QueryFormat string

const (
	FormatJSONL QueryFormat = "jsonl"
	FormatJSON  QueryFormat = "json"
)

// Commander runs an executable and returns its stdout (one-method interface, like
// beads.Runner / ccpool.Runner — not a bare func field).
type Commander interface {
	Run(ctx context.Context, argv []string) ([]byte, error)
}

// Env carries the capabilities a query needs. The orchestrator builds it from its
// own fields in phase 1 (the Deps bag arrives in phase 2).
type Env struct {
	BD       beads.Runner
	RepoRoot string
	Cmd      Commander
}

type Query interface {
	Validate() error
	Run(ctx context.Context, env Env) ([]item.Item, error)
}

// FromIssue maps a single bd issue to an item, copying its metadata (keeps item a
// leaf — the adapter lives here). The query/drain path (fromIssues) and the
// direct-bead run-role path (pg2-jpci) share this one adapter, so a dispatched Item
// carries the bead's metadata identically no matter which path built it.
func FromIssue(i beads.Issue) item.Item {
	return item.Item{ID: i.ID, Type: i.Type, Title: i.Title, Metadata: i.Metadata}
}

// fromIssues maps bd issues to items via FromIssue.
func fromIssues(in []beads.Issue) []item.Item {
	out := make([]item.Item, 0, len(in))
	for _, i := range in {
		out = append(out, FromIssue(i))
	}
	return out
}
