// activity.go: Backend implements pkg/provider/activity.Provider against
// GitHub. list_activity is range-shaped and stateless: every call resolves
// the authenticated viewer, runs GitHub searches bounded by the requested
// range, and returns the viewer's own happenings; nothing is stored.
//
// This file emits the once-per-entity kinds pr.opened, pr.merged and
// pr.closed. Each happens at most once per PR, so its item id is
// "<OWNER/REPO#N>#<kind>", identical on every call over any range.
//
// Seam for further kinds: ListActivity resolves the viewer once and then calls
// each collector of the form
//
//	func (b *Backend) collect...(ctx, viewer string, since, before time.Time) (items []schema.ActivityItem, truncated bool, err error)
//
// concatenating their items and ORing their truncated flags. A new kind family
// adds one collector and one call in ListActivity, and builds its items with
// newActivityItem.
//
// Scope: the queries are account-wide (author = the viewer); this backend has
// no repo/org configuration key, so none narrows them.
//
// Documented item "fields" keys (an object, always present; the envelope is
// the contract, these keys are this backend's own):
//
//	pr.opened:  title, author, draft
//	pr.merged:  title, author, base
//	pr.closed:  title, author
package internal

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-pr-github/internal/api"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/activity"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

var _ activity.Provider = (*Backend)(nil)

// Activity kinds this file emits.
const (
	KindPROpened = "pr.opened"
	KindPRMerged = "pr.merged"
	KindPRClosed = "pr.closed"
)

// ActivityKindsOnce is the vocabulary this file contributes to
// capabilities vocabulary.activity_kinds.
var ActivityKindsOnce = []string{KindPROpened, KindPRMerged, KindPRClosed}

// activitySearchCap is GitHub search's per-query result cap. A query that
// returns exactly this many results may have been cut short, so the range is
// reported as incompletely covered (truncated).
const activitySearchCap = 1000

// ListActivity implements activity.Provider. It checks the GraphQL rate-limit
// reserve before its first search, resolves the authenticated viewer ONCE
// (failing as unavailable when it cannot, never returning an unscoped result),
// and then runs each kind collector.
func (b *Backend) ListActivity(ctx context.Context, since, before time.Time) (*schema.ActivityListResult, error) {
	if err := b.checkRateReserve(ctx); err != nil {
		return nil, err
	}
	viewer, err := b.gh.ViewerLogin(ctx)
	if err != nil || strings.TrimSpace(viewer) == "" {
		reason := "empty login"
		if err != nil {
			reason = err.Error()
		}
		return nil, scriptout.WrapError(scriptout.ErrUnavailable, fmt.Sprintf(
			"pg-connector-pr-github: cannot resolve the authenticated GitHub viewer login (missing or invalid authentication): %s", reason,
		))
	}

	items := []schema.ActivityItem{}
	truncated := false

	once, onceTrunc, err := b.collectOnceKinds(ctx, viewer, since, before)
	if err != nil {
		return nil, err
	}
	items = append(items, once...)
	truncated = truncated || onceTrunc

	return &schema.ActivityListResult{Items: items, Truncated: truncated}, nil
}

// newActivityItem builds an item for a pr entity. It does NOT build the id:
// the caller sets item.ID (once-per-entity kinds use "<entityID>#<kind>";
// repeatable kinds append an event id). fields is marshaled as a JSON object
// ({} when nil or not an object); as_of is the read time in RFC3339 UTC and
// stale is false (this backend keeps no cache).
func newActivityItem(entityID, kind, occurredAt, summary, url string, labels []string, fields any) schema.ActivityItem {
	raw := json.RawMessage("{}")
	if fields != nil {
		if b, err := json.Marshal(fields); err == nil && len(b) > 0 && b[0] == '{' {
			raw = b
		}
	}
	return schema.ActivityItem{
		Kind:       kind,
		EntityType: "pr",
		EntityID:   entityID,
		OccurredAt: occurredAt,
		Summary:    summary,
		URL:        url,
		Labels:     labels,
		Fields:     raw,
		AsOf:       time.Now().UTC().Format(time.RFC3339),
		Stale:      false,
	}
}

// activityQualifier renders GitHub's date qualifier (key is "created",
// "merged" or "closed") for [since, before) as full RFC3339 UTC timestamps.
// GitHub's range is inclusive at both ends, so the caller still filters each
// item by its own timestamp. A zero since uses the upper-bound-only form.
func activityQualifier(key string, since, before time.Time) string {
	b := before.UTC().Format(time.RFC3339)
	if since.IsZero() {
		return fmt.Sprintf("%s:<=%s", key, b)
	}
	return fmt.Sprintf("%s:%s..%s", key, since.UTC().Format(time.RFC3339), b)
}

// inActivityRange reports whether ts (RFC3339) lies in [since, before); a zero
// since means no lower bound. An unparseable or empty ts is not in range, so a
// happening the backend cannot date is never emitted.
func inActivityRange(ts string, since, before time.Time) bool {
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return false
	}
	if !since.IsZero() && t.Before(since) {
		return false
	}
	return t.Before(before)
}

// collectOnceKinds emits pr.opened, pr.merged and pr.closed for the viewer's
// own PRs in [since, before). truncated is true when any query returned the
// GitHub search cap.
func (b *Backend) collectOnceKinds(ctx context.Context, viewer string, since, before time.Time) (items []schema.ActivityItem, truncated bool, err error) {
	// The resolved login is substituted for @me (identical meaning), and each
	// result is still filtered to the viewer below.
	author := "author:" + viewer

	// pr.opened: created_at from the search result itself.
	opened, capped, err := b.searchActivity(ctx, author+" "+activityQualifier("created", since, before))
	if err != nil {
		return nil, false, err
	}
	truncated = truncated || capped
	for _, p := range opened {
		if !sameLogin(p.Author, viewer) || !inActivityRange(p.CreatedAt, since, before) {
			continue
		}
		id := formatPRID(p.Repo, p.Number)
		items = append(items, onceItem(id, KindPROpened, p.CreatedAt, "opened "+id+": "+p.Title, p, map[string]any{
			"title": p.Title, "author": p.Author, "draft": p.Draft,
		}))
	}

	// pr.merged: gh search has no merge timestamp, so merged_at comes from the
	// PR read the backend already has. A PR whose merge time cannot be
	// obtained is not emitted.
	merged, capped, err := b.searchActivity(ctx, author+" "+activityQualifier("merged", since, before))
	if err != nil {
		return nil, false, err
	}
	truncated = truncated || capped
	var candidates []api.PR
	for _, p := range merged {
		if sameLogin(p.Author, viewer) {
			candidates = append(candidates, p)
		}
	}
	full, err := parallelMap(ctx, candidates, func(ctx context.Context, c api.PR) (*api.PR, error) {
		got, gerr := b.gh.GetPR(ctx, c.Repo, c.Number)
		if gerr != nil {
			if isGHNotFound(gerr) {
				return nil, nil
			}
			return nil, gerr
		}
		return got, nil
	})
	if err != nil {
		return nil, false, classifyGHError(err)
	}
	for i, c := range candidates {
		got := full[i]
		if got == nil || !inActivityRange(got.MergedAt, since, before) {
			continue
		}
		id := formatPRID(c.Repo, c.Number)
		items = append(items, onceItem(id, KindPRMerged, got.MergedAt, "merged "+id+": "+c.Title, c, map[string]any{
			"title": c.Title, "author": c.Author, "base": got.Base,
		}))
	}

	// pr.closed: closed without merging, closed_at from the search result.
	closed, capped, err := b.searchActivity(ctx, author+" is:unmerged "+activityQualifier("closed", since, before))
	if err != nil {
		return nil, false, err
	}
	truncated = truncated || capped
	for _, p := range closed {
		if !sameLogin(p.Author, viewer) || !inActivityRange(p.ClosedAt, since, before) {
			continue
		}
		id := formatPRID(p.Repo, p.Number)
		items = append(items, onceItem(id, KindPRClosed, p.ClosedAt, "closed "+id+": "+p.Title, p, map[string]any{
			"title": p.Title, "author": p.Author,
		}))
	}

	return dedupeActivity(items), truncated, nil
}

// searchActivity runs one capped activity search and reports whether the cap
// was reached.
func (b *Backend) searchActivity(ctx context.Context, query string) (prs []api.PR, capped bool, err error) {
	prs, err = b.gh.SearchPRsActivity(ctx, query, activitySearchCap)
	if err != nil {
		return nil, false, classifyGHError(err)
	}
	return prs, len(prs) >= activitySearchCap, nil
}

// onceItem builds a once-per-entity item: id is "<entityID>#<kind>".
func onceItem(entityID, kind, occurredAt, summary string, p api.PR, fields any) schema.ActivityItem {
	it := newActivityItem(entityID, kind, occurredAt, summary, p.URL, []string{"repo:" + p.Repo}, fields)
	it.ID = entityID + "#" + kind
	return it
}

// dedupeActivity drops repeated ids (a PR returned twice by one search),
// keeping the first.
func dedupeActivity(items []schema.ActivityItem) []schema.ActivityItem {
	seen := make(map[string]bool, len(items))
	out := make([]schema.ActivityItem, 0, len(items))
	for _, it := range items {
		if seen[it.ID] {
			continue
		}
		seen[it.ID] = true
		out = append(out, it)
	}
	return out
}

// sameLogin compares two GitHub logins case-insensitively; an empty login
// never matches.
func sameLogin(a, b string) bool {
	return a != "" && strings.EqualFold(a, b)
}
