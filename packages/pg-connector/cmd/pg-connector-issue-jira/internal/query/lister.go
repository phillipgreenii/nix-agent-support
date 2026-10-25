package query

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/origin"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/summary"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/engine"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/groupkit"
	lq "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/query"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// expand is what a query search asks pjira to carry along: the same expansion
// as the summary search, so a listed issue is persisted as a full summary.
var expand = []string{"changelog", "comments"}

// Searcher is the origin call the lister makes: one JQL search. The call's
// class, purpose and group ride on ctx (origin.WithCall). *origin.Runner
// satisfies it.
type Searcher interface {
	Search(ctx context.Context, jql string, expand []string) (*internal.PjiraSearchResult, error)
}

// Persister is the part of summary.Persister the lister uses;
// *summary.Persister satisfies it.
type Persister interface {
	Begin(ctx context.Context, id string) (groupkit.Token, error)
	Persist(ctx context.Context, tok groupkit.Token, c groups.SummaryContent, info engine.RefreshInfo) (summary.Stored, error)
}

var _ Persister = (*summary.Persister)(nil)

// QueryStore is the part of the daemon store the lister reads: the previously
// stored row of the query. *store.Store satisfies it.
type QueryStore interface {
	GetQuery(ctx context.Context, key string) (store.QueryRow, bool, error)
}

var _ QueryStore = (*store.Store)(nil)

// ListerConfig wires a Lister.
type ListerConfig struct {
	// Fetcher runs the searches.
	Fetcher Searcher
	// Persister stores each listed issue.
	Persister Persister
	// Store, when set, gives the previously stored row of the query: a token is
	// taken for each of its members BEFORE the search, so a write made between
	// the search and the commit makes that issue's commit lose. Nil means every
	// token is taken after the search.
	Store QueryStore
	// Clock is the time source; nil means the system clock.
	Clock governor.Clock
	// Sleep waits d or until ctx is done, returning ctx's error in the second
	// case; nil means a real timer. A test that drives a manual clock passes a
	// Sleep that advances it.
	Sleep func(ctx context.Context, d time.Duration) error
}

// Lister is the Jira implementation of the shared query.Lister.
type Lister struct {
	cfg ListerConfig
}

var _ lq.Lister = (*Lister)(nil)

// NewLister returns the Lister: for every expression it runs ONE full search
// (the cursor is always null) with the changelog and comments expanded, under a
// call of the given class and purpose and group "summary". It takes tokens for
// the members of the query's previously stored row before it searches, persists
// every returned issue (summary.ContentFromPjira and Persister.Persist) so the
// entity rows and their summary content exist before the runner looks at
// membership, unions the members across expressions deduplicated by key, and
// reports Complete = false when any search was truncated.
//
// When one expression's search is refused by the governor, the lister waits
// until the refusal's RetryAt (bounded by the context) and retries THAT
// expression, so a query with several expressions completes under the
// one-per-second background cap. A refusal with no known retry time is returned.
//
// The Jira daemon defines no query argument: a Key with arguments is an
// invalid_argument error.
func NewLister(cfg ListerConfig) *Lister {
	if cfg.Clock == nil {
		cfg.Clock = governor.SystemClock{}
	}
	if cfg.Sleep == nil {
		cfg.Sleep = sleep
	}
	return &Lister{cfg: cfg}
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// List implements lq.Lister.
func (l *Lister) List(ctx context.Context, key lq.Key, exprs []string, call lq.CallMeta) (lq.Listing, error) {
	if key.Adhoc() {
		return lq.Listing{}, scriptout.WrapError(scriptout.ErrInvalidArgument,
			fmt.Sprintf("query %q: the Jira daemon takes no query argument", key.Name))
	}
	toks, err := l.previousTokens(ctx, key)
	if err != nil {
		return lq.Listing{}, err
	}
	started := l.cfg.Clock.Now()
	info := engine.RefreshInfo{Started: started, Class: call.Class, Purpose: call.Purpose}
	octx := origin.WithCall(ctx, origin.Call{Class: call.Class, Purpose: call.Purpose, Group: groups.Summary})

	out := lq.Listing{Complete: true}
	seen := map[string]bool{}
	for _, expr := range exprs {
		res, err := l.search(octx, strings.TrimSpace(expr))
		if err != nil {
			return lq.Listing{}, err
		}
		if res.Truncated {
			out.Complete = false
		}
		for i := range res.Items {
			iss := &res.Items[i]
			id := groups.FoldRef(iss.Key)
			if id == "" || seen[id] {
				continue
			}
			seen[id] = true
			tok, ok := toks[id]
			if !ok {
				// A new issue has no group row: its token is taken now.
				if tok, err = l.cfg.Persister.Begin(ctx, id); err != nil {
					return lq.Listing{}, err
				}
			}
			content := summary.ContentFromPjira(iss, l.cfg.Clock.Now())
			if _, err := l.cfg.Persister.Persist(ctx, tok, content, info); err != nil {
				return lq.Listing{}, err
			}
			out.Members = append(out.Members, lq.Member{ID: groups.FoldRef(content.Issue.ID), Ref: content.Issue.ID})
		}
	}
	return out, nil
}

// previousTokens takes a token for every member of the query's previously
// stored row, keyed by the member's entity id.
func (l *Lister) previousTokens(ctx context.Context, key lq.Key) (map[string]groupkit.Token, error) {
	toks := map[string]groupkit.Token{}
	if l.cfg.Store == nil {
		return toks, nil
	}
	row, ok, err := l.cfg.Store.GetQuery(ctx, key.String())
	if err != nil || !ok {
		return toks, err
	}
	for _, ref := range row.IDs {
		id := groups.FoldRef(ref)
		if _, dup := toks[id]; dup {
			continue
		}
		tok, err := l.cfg.Persister.Begin(ctx, id)
		if err != nil {
			return nil, err
		}
		toks[id] = tok
	}
	return toks, nil
}

// search runs one expression, waiting out a governor refusal that states when
// the request may be retried.
func (l *Lister) search(ctx context.Context, jql string) (*internal.PjiraSearchResult, error) {
	immediate := false
	for {
		res, err := l.cfg.Fetcher.Search(ctx, jql, expand)
		if err == nil {
			return res, nil
		}
		var refusal *governor.Refusal
		if !errors.As(err, &refusal) || refusal.RetryAt.IsZero() {
			return nil, err
		}
		wait := refusal.RetryAt.Sub(l.cfg.Clock.Now())
		if wait <= 0 {
			// The retry time has already passed: retry at once, but only once
			// in a row, so a refusal that keeps naming the past cannot spin.
			if immediate {
				return nil, err
			}
			immediate = true
			continue
		}
		immediate = false
		if serr := l.cfg.Sleep(ctx, wait); serr != nil {
			return nil, serr
		}
	}
}
