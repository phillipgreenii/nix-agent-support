// search.go: Backend implements pkg/provider/search.Provider by wrapping
// `bd search` (bead pg2-wyeq4; modelled on pg-connector-issue-jira's Search and
// pg-connector-calendar-osx-bridge's). It lets the umbrella's `pg-connector
// search <query>` fan out to each beads tracker instance.
//
// bd search matches the query against bead titles and ids ("ID-like" queries
// such as "pg2-5q" use bd's fast prefix match) and, by default, ALL statuses
// including closed, so "was this already filed?" cannot silently answer no. This
// wrapper keeps that default (it never passes --status) and reports each hit's
// status as an attribute instead.
//
// Wire result (schema.SearchResult): type "issue", id the bead id, title the
// bead title, source "pg-connector-issue-beads", no url (bd has no hosted
// page). bd's own ordering is kept. At most searchLimit hits are returned per
// call (bd drops the rest, status-blind): the search op has no truncated flag,
// so a caller that needs more narrows the query.
//
// Attributes (the --fields probe): the umbrella validates a requested --fields
// value against the core result shape plus the names a backend declares under
// capabilities vocabulary.search_attributes. This backend declares
// SearchAttributes and fills exactly the requested ones; a requested name it
// does not know (including the core names, which are always present) is
// silently ignored, as search.Provider requires. With no fields requested no
// attribute is populated.
//
// Time bound: search_since/search_before (RFC3339, since inclusive, before
// exclusive) in the backend's config block, set by the umbrella's --since/
// --before flags, bound each hit's updated_at, because bd search has no
// updated-window flag the wrapper can rely on across versions. A hit without a
// parseable updated_at cannot be placed in a bounded window and is dropped; an
// unbounded search keeps every hit.
package internal

import (
	"context"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/search"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

var _ search.Provider = (*Backend)(nil)

const (
	// searchSourceName is SearchResult.Source for this backend (mirrors
	// pg-connector-issue-jira).
	searchSourceName = "pg-connector-issue-beads"
	// searchResultType is SearchResult.Type.
	searchResultType = "issue"
	// searchLimit is the per-call hit cap passed to bd (bd's own default is 50).
	searchLimit = "100"
)

// SearchAttributes is the attribute vocabulary this backend contributes to
// capabilities vocabulary.search_attributes: the names Search fills on request.
var SearchAttributes = []string{"status", "priority", "labels", "issue_type", "assignee", "owner", "tracker"}

// Search implements search.Provider via `bd search`.
func (b *Backend) Search(ctx context.Context, query string, fields []string) ([]schema.SearchResult, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, scriptout.WrapError(scriptout.ErrInvalidArgument, "search: query required")
	}
	rng, err := scriptout.SearchRangeFromContext(ctx)
	if err != nil {
		return nil, scriptout.WrapError(scriptout.ErrInvalidArgument, "search: "+err.Error())
	}
	// --query=<q> binds the text to the flag, so a query like "--help" is a
	// search term and never a bd flag.
	data, err := b.run(ctx, "search", "--readonly", "--json", "-n", searchLimit, "--query="+query)
	if err != nil {
		return nil, err
	}
	issues, err := bdIssuesFromArray(data)
	if err != nil {
		return nil, err
	}

	wanted := map[string]bool{}
	for _, f := range fields {
		wanted[f] = true
	}
	tracker := ""
	if wanted["tracker"] {
		tracker = b.tracker()
	}
	results := make([]schema.SearchResult, 0, len(issues))
	for i := range issues {
		iss := &issues[i]
		if iss.ID == "" || !inSearchRange(rng, iss.UpdatedAt) {
			continue
		}
		results = append(results, schema.SearchResult{
			Type:       searchResultType,
			ID:         iss.ID,
			Title:      iss.Title,
			Source:     searchSourceName,
			Attributes: searchAttributesFor(iss, wanted, tracker),
		})
	}
	return results, nil
}

// inSearchRange reports whether a hit last updated at updatedAt falls in rng.
// An open range keeps every hit, dated or not.
func inSearchRange(rng scriptout.TimeRange, updatedAt string) bool {
	if rng.IsZero() {
		return true
	}
	t, err := time.Parse(time.RFC3339, updatedAt)
	if err != nil {
		return false
	}
	return rng.Contains(t)
}

// searchAttributesFor fills the requested SearchAttributes for iss; nil when
// none of the requested names is one this backend declares.
func searchAttributesFor(iss *bdIssue, wanted map[string]bool, tracker string) map[string]any {
	var attrs map[string]any
	put := func(name string, v any) {
		if !wanted[name] {
			return
		}
		if attrs == nil {
			attrs = map[string]any{}
		}
		attrs[name] = v
	}
	put("status", iss.Status)
	put("priority", formatPriority(iss.Priority))
	put("labels", append([]string{}, iss.Labels...))
	put("issue_type", iss.IssueType)
	put("assignee", iss.Assignee)
	put("owner", iss.Owner)
	put("tracker", tracker)
	return attrs
}
