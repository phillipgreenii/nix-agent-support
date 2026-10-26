package read

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/origin"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/feedop"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	lq "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/query"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// The groups[] names of a list answer: the stored query row, and the stored
// summaries of its members as one aggregated entry.
const (
	groupQuery   = "query"
	groupSummary = groups.Summary
)

// listArgs are the wire arguments of list. cursor is accepted and ignored: the
// daemon has no incremental listing, and the answer's cursor is always null.
type listArgs struct {
	Query   string          `json:"query"`
	Cursor  json.RawMessage `json:"cursor"`
	IDsOnly bool            `json:"ids_only"`
}

// list answers a configured query from its stored row and the stored summaries.
func (h *handlers) list(ctx context.Context, raw json.RawMessage) (any, error) {
	var a listArgs
	if err := scriptout.Decode(raw, &a); err != nil {
		return nil, scriptout.WrapError(scriptout.ErrInvalidArgument, "decode list args: "+err.Error())
	}
	watch, err := feedop.WatchArg(raw)
	if err != nil {
		return nil, err
	}
	if watch {
		return nil, scriptout.WrapError(scriptout.ErrInvalidArgument, "the watch option is accepted on a read of one issue only, not on a listing")
	}
	if h.deps.Runner == nil || h.deps.Store == nil {
		return nil, unavailable("the query runner is not available")
	}
	// The daemon's OWN settings decide: the request's config block is never read.
	if !h.defined(a.Query) {
		return nil, scriptout.WrapError(scriptout.ErrQueryNotRecognized,
			fmt.Sprintf("query %q is not defined in this daemon's config", a.Query))
	}
	key := lq.Key{Name: a.Query}

	var srv served
	row, err := h.queryRow(ctx, key, &srv)
	if err != nil {
		return nil, err
	}

	res := &schema.IssueListResult{
		PresentIDs: []string{},
		// Cursor stays nil, which marshals as JSON null.
		Truncated: !row.Complete,
	}
	now := h.now()
	items := []servedGroup{{group: groupQuery, row: queryAsGroup(row), fromOrigin: srv.origin, stale: srv.stale}}
	if a.IDsOnly {
		res.PresentIDs = append(res.PresentIDs, row.IDs...)
	} else {
		sums, err := h.summaries(ctx, row.IDs)
		if err != nil {
			return nil, err
		}
		entities := make([]schema.Issue, 0, len(sums))
		for _, s := range sums {
			iss := s.content.Issue
			iss.AsOf = s.row.FetchedAt.UTC().Format(time.RFC3339)
			iss.Stale = srv.stale
			iss.Version = s.version
			iss.EntityMeta = s.meta
			entities = append(entities, iss)
			res.PresentIDs = append(res.PresentIDs, s.id)
		}
		res.Entities = entities
		if agg, ok := aggregate(sums); ok {
			items = append(items, servedGroup{group: groupSummary, row: agg, stale: srv.stale})
		}
	}
	prov := provenanceOf(items, now)
	prov.applyList(res)
	prov.record(ctx)
	return res, nil
}

// defined reports whether the daemon's own settings define the query name.
func (h *handlers) defined(name string) bool {
	if h.deps.Settings != nil {
		_, ok := h.deps.Settings.Expr(name)
		return ok
	}
	_, ok := h.deps.Runner.Expr(name)
	return ok
}

// served is what a list call learned about where its answer came from.
type served struct {
	origin bool // the query was re-run for this call
	stale  bool // the stored row was served although it could not be refreshed
}

// queryRow returns the stored row of key inside its TTL; otherwise it has the
// runner re-run the query and, when that cannot be done, serves the stored row
// inside the expiry as stale. With nothing usable stored it is an error: the
// authentication failure when the daemon is unauthenticated, else unavailable
// naming the pause, the cap or the origin failure.
func (h *handlers) queryRow(ctx context.Context, key lq.Key, srv *served) (store.QueryRow, error) {
	// A stored read is local and quick, and the answer at the caller's deadline
	// is made of exactly such reads.
	row, have, err := h.deps.Runner.Row(context.WithoutCancel(ctx), key)
	if err != nil {
		return store.QueryRow{}, unavailable("read query %q: %v", key, err)
	}
	now := h.now()
	if have && !row.FetchedAt.IsZero() && row.FreshUntil.After(now) {
		return row, nil
	}
	usable := have && !row.FetchedAt.IsZero() && now.Sub(row.FetchedAt) <= h.deps.Expiry

	var cause error
	if a := h.deps.Auth; a != nil {
		if bad, uerr := a.Unauthenticated(); bad {
			cause = unauthenticated(uerr)
		}
	}
	if cause == nil {
		got, ferr := h.deps.Runner.Fetch(ctx, key)
		if ferr == nil {
			srv.origin = true
			return got, nil
		}
		cause = ferr
	}
	if usable {
		srv.stale = true
		return row, nil
	}
	return store.QueryRow{}, h.classifyFetch(key.Name, cause)
}

// unauthenticated is the error of a daemon whose credentials the origin refused.
func unauthenticated(cause error) error {
	text := "the daemon is not authenticated with the origin"
	if cause != nil {
		text += ": " + cause.Error()
	}
	return scriptout.WrapError(scriptout.ErrUnauthenticated, text)
}

// classifyFetch maps why a query could not be run onto the closed error
// taxonomy.
func (h *handlers) classifyFetch(name string, cause error) error {
	var refusal *governor.Refusal
	switch {
	case inTaxonomy(cause):
		return h.wire(cause)
	case errors.As(cause, &refusal):
		return h.wire(unavailable("query %q: %s", name, refusal.Reason))
	case errors.Is(cause, lq.ErrPending):
		return h.wire(unavailable("query %q: still being fetched at the deadline; it completes in the background", name))
	case errors.Is(cause, origin.ErrPaused), origin.IsRateLimited(cause):
		return h.wire(unavailable("query %q: %v", name, cause))
	}
	return h.wire(unavailable("query %q: %v", name, cause))
}

// storedSummary is one member's stored summary.
type storedSummary struct {
	id      string
	content groups.SummaryContent
	row     store.GroupRow
	version int64
	// meta is the entity's metadata beside its content (the watched flag).
	meta *schema.EntityMeta
}

// summaries reads the stored summary of every id, in the order of ids. An id
// with no summary stored, a tombstoned entity and a summary marked gone are left
// out.
func (h *handlers) summaries(ctx context.Context, ids []string) ([]storedSummary, error) {
	ctx = context.WithoutCancel(ctx)
	out := make([]storedSummary, 0, len(ids))
	for _, id := range ids {
		fold := groups.FoldRef(id)
		ent, entFound, err := h.deps.Store.GetEntity(ctx, groups.EntityKind, fold)
		if err != nil {
			return nil, unavailable("read the entity row of %s: %v", id, err)
		}
		if entFound && !ent.TombstonedAt.IsZero() {
			continue
		}
		g, ok, err := h.deps.Store.GetGroup(ctx, groups.EntityKind, fold, groups.Summary)
		if err != nil {
			return nil, unavailable("read the summary of %s: %v", id, err)
		}
		if !ok || len(g.Content) == 0 {
			continue
		}
		var c groups.SummaryContent
		if err := json.Unmarshal(g.Content, &c); err != nil {
			return nil, unavailable("the stored summary of %s is unreadable: %v", id, err)
		}
		if c.Gone {
			continue
		}
		out = append(out, storedSummary{id: id, content: c, row: g, version: ent.Version, meta: feedop.EntityMetaOf(ent, entFound)})
	}
	return out, nil
}

// queryAsGroup presents a stored query row as the group row its groups[] entry
// is written from.
func queryAsGroup(q store.QueryRow) store.GroupRow {
	return store.GroupRow{FetchedAt: q.FetchedAt, FreshUntil: q.FreshUntil, LastError: q.LastError}
}

// aggregate folds the members' summary rows into one groups[] entry: the oldest
// fetch time and the earliest end of freshness. ok is false for no members.
func aggregate(sums []storedSummary) (store.GroupRow, bool) {
	var agg store.GroupRow
	for i, s := range sums {
		if i == 0 || s.row.FetchedAt.Before(agg.FetchedAt) {
			agg.FetchedAt = s.row.FetchedAt
		}
		if i == 0 || s.row.FreshUntil.Before(agg.FreshUntil) {
			agg.FreshUntil = s.row.FreshUntil
		}
		if agg.LastError == "" {
			agg.LastError = s.row.LastError
		}
	}
	return agg, len(sums) > 0
}
