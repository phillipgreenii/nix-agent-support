// Package passthrough is the pr instance's two METERED PASS-THROUGH ops, search
// and list_activity.
//
// "Metered pass-through" means the daemon performs the call itself, so the
// governor sees and limits its spend, but stores nothing: no group, no entity
// row, no query row, no cache. Both ops are the old backend's, ported unchanged
// in argument decoding (the dispatch tables of pkg/provider/search and
// pkg/provider/activity are reused), schema version and result shape:
//
//   - search runs one GitHub search-syntax query for pull requests and answers
//     schema.SearchResult entries;
//   - list_activity is range-shaped and stateless: it resolves the authenticated
//     viewer, runs GitHub searches bounded by the requested range and returns the
//     viewer's own happenings as schema.ActivityListResult.
//
// # Metering
//
// Every origin call of both ops is class 1 (a caller is waiting) with purpose
// passthrough (see passthroughCall). The Provider handed in through Deps MUST be
// the metered one (metered.NewProvider), which admits each gh call through the
// governor; the old backend's rate-reserve probe is NOT ported, because the
// governor's caps and reserve replace it. A refusal, or GitHub's own rate-limit
// answer, makes the op unavailable naming the cap, the reserve or the pause, with
// nothing further run (metered.WireError).
//
// These ops are not given an origin-call budget of their own beyond the
// governor's: nothing is stored, so there is no stale answer to fall back to.
package passthrough

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/api"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/metered"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/ratefeed"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/activity"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/search"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// The two ops, as the router keys them.
const (
	OpSearch       = "search"
	OpListActivity = scriptout.OpListActivity
)

// Ops is the set of ops this package serves, in a fixed order.
var Ops = []string{OpSearch, OpListActivity}

// searchSource is the Source of every search result. It stays the old backend's
// literal: result parity with the old backend is the stated test and the
// registry keeps the old instance names. It is a constant here, not derived
// from anything configurable.
const searchSource = "pg-connector-pr-github"

// GitHub is the Provider's reads these ops make. *github.Provider satisfies it;
// the one handed to Deps MUST be built over the metered runner.
type GitHub interface {
	SearchPRs(ctx context.Context, query string) ([]api.PR, error)
	SearchPRsActivity(ctx context.Context, query string, limit int) ([]api.PR, error)
	ViewerLogin(ctx context.Context) (string, error)
	GetPR(ctx context.Context, repo string, number int) (*api.PR, error)
	ListReviewsSubmitted(ctx context.Context, repo string, number int) ([]api.Review, error)
	ListComments(ctx context.Context, repo string, number int) ([]api.Comment, error)
}

// Deps is what the handlers need.
type Deps struct {
	// GH is the metered GitHub Provider.
	GH GitHub
}

// Handlers returns the handlers of search and list_activity, keyed by op, with
// the schema versions of the old backend (schema.SearchSchemaVersion and
// schema.ActivitySchemaVersion). Their auth_status entries are NOT taken: the
// identity package answers that op.
func Handlers(deps Deps) map[string]scriptout.OpHandler {
	b := &backend{gh: deps.GH}
	out := map[string]scriptout.OpHandler{}
	for op, table := range map[string]scriptout.DispatchTable{
		OpSearch:       search.NewDispatchTable(b),
		OpListActivity: activity.NewDispatchTable(b),
	} {
		out[op] = table[op]
	}
	return out
}

// backend implements search.Provider and activity.Provider over the metered
// GitHub Provider.
type backend struct {
	gh GitHub
}

var _ search.Provider = (*backend)(nil)

// passthroughCall states what every origin call of these ops is for: a class 1
// call (a caller is waiting) whose spend is booked as passthrough.
func passthroughCall(ctx context.Context) context.Context {
	return metered.WithCall(ctx, ratefeed.Meta{Class: governor.ClassInteractive, Purpose: governor.PurposePassthrough})
}

// isRefusal reports whether err is, or wraps, a governor refusal.
func isRefusal(err error) bool {
	var r *governor.Refusal
	return errors.As(err, &r)
}

// Search implements search.Provider: query is a single bare GitHub
// search-syntax string, passed straight through with no config.queries
// resolution. fields is unused: no result carries Attributes beyond the core
// set. The origin call is class 1, purpose passthrough, on the REST search
// bucket.
func (b *backend) Search(ctx context.Context, query string, _ []string) ([]schema.SearchResult, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, scriptout.WrapError(scriptout.ErrInvalidArgument, "search: query required")
	}
	prs, err := b.gh.SearchPRs(passthroughCall(ctx), query)
	if err != nil {
		return nil, metered.WireError(err)
	}
	out := make([]schema.SearchResult, 0, len(prs))
	for i := range prs {
		p := prs[i]
		out = append(out, schema.SearchResult{
			Type:   "pr",
			ID:     formatPRID(p.Repo, p.Number),
			Title:  p.Title,
			URL:    p.URL,
			Source: searchSource,
		})
	}
	return out, nil
}

// formatPRID renders the wire id of a PR, "<owner>/<repo>#<number>".
func formatPRID(repo string, number int) string {
	return fmt.Sprintf("%s#%d", repo, number)
}

// maxParallelWorkers bounds how many concurrent gh calls one fan-out
// (parallelMap) makes. Every call is still admitted by the governor one by one.
const maxParallelWorkers = 8

// parallelMap runs fn(items[i]) for every i using a bounded pool of at most
// maxParallelWorkers goroutines, preserving items' order in the returned slice.
// Every fn call runs to completion; the first non-nil error (by items' own
// index order) is returned.
func parallelMap[T, R any](ctx context.Context, items []T, fn func(ctx context.Context, item T) (R, error)) ([]R, error) {
	if len(items) == 0 {
		return nil, nil
	}
	workers := min(maxParallelWorkers, len(items))

	results := make([]R, len(items))
	errs := make([]error, len(items))
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup

	for i, item := range items {
		sem <- struct{}{}
		wg.Add(1)
		go func(i int, item T) {
			defer wg.Done()
			defer func() { <-sem }()
			results[i], errs[i] = fn(ctx, item)
		}(i, item)
	}
	wg.Wait()

	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	return results, nil
}
