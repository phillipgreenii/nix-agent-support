// listrange.go: the ranged-list helpers (bead pg2-ttk9t, work-tracker design
// WT-D18). The umbrella delivers list_since/list_before in the request config
// (scriptout.ListRangeFromContext). JQL date literals are day-granular here
// and are interpreted in the QUERYING USER's timezone, so rangedJQL WIDENS
// each end by whole days (never narrowing the window), and issueInRange then
// cuts precisely by the issue's own updated time, so present_ids is exactly
// the bounded match set.
package internal

import (
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

const jqlDateLayout = "2006-01-02"

// splitOrderBy separates a trailing ORDER BY (JQL requires it last) so the
// range clauses are inserted before it.
func splitOrderBy(jql string) (where, order string) {
	idx := strings.LastIndex(strings.ToLower(jql), " order by ")
	if idx < 0 {
		return jql, ""
	}
	return jql[:idx], jql[idx:]
}

// rangedJQL narrows jql to r. An unbounded range returns jql unchanged.
//
// since renders as `updated >= "<UTC date - 1 day>"` and before as
// `updated < "<UTC date + 2 days>"`: Jira reads a bare date as midnight in
// the user's timezone (anywhere from UTC-12 to UTC+14), so a one-day margin
// below and a two-day margin above guarantee the server-side window contains
// the requested one for every timezone. issueInRange does the exact cut.
func rangedJQL(jql string, r scriptout.TimeRange) string {
	if r.IsZero() {
		return jql
	}
	where, order := splitOrderBy(jql)
	var clauses []string
	if !r.Since.IsZero() {
		clauses = append(clauses, `updated >= "`+r.Since.UTC().AddDate(0, 0, -1).Format(jqlDateLayout)+`"`)
	}
	if !r.Before.IsZero() {
		clauses = append(clauses, `updated < "`+r.Before.UTC().AddDate(0, 0, 2).Format(jqlDateLayout)+`"`)
	}
	where = strings.TrimSpace(where)
	if where == "" {
		return strings.Join(clauses, " AND ") + order
	}
	return "(" + where + ") AND " + strings.Join(clauses, " AND ") + order
}

// jiraUpdatedLayouts are the timestamp spellings pjira may carry in
// Issue.Updated: strict RFC3339, and Jira's own "+0000" (no colon) offset
// forms with and without fractional seconds.
var jiraUpdatedLayouts = []string{
	time.RFC3339,
	"2006-01-02T15:04:05.000-0700",
	"2006-01-02T15:04:05-0700",
}

func parseJiraUpdated(s string) (time.Time, bool) {
	for _, layout := range jiraUpdatedLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// issueInRange applies r precisely to an issue's updated time. An issue whose
// timestamp is missing or unparseable cannot be judged: it is kept and
// flagged imprecise, so the caller MUST report truncated: true rather than
// claim the bound was honored (WT-D18).
func issueInRange(updated string, r scriptout.TimeRange) (keep, imprecise bool) {
	if r.IsZero() {
		return true, false
	}
	t, ok := parseJiraUpdated(updated)
	if !ok {
		return true, true
	}
	return r.Contains(t), false
}
