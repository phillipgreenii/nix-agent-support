package summary

import (
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/groups"
)

// commentLayouts are the spellings of a comment's creation time: Jira's own
// offset with no colon, then strict RFC 3339.
var commentLayouts = []string{"2006-01-02T15:04:05.000-0700", time.RFC3339}

func parseCommentTime(s string) (time.Time, bool) {
	for _, layout := range commentLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// ContentFromPjira maps a decoded pjira issue to the stored summary content.
// Issue is internal.ToSchemaIssue with asOf; CommentCount is the number of
// comments the search carried; LatestCommentAt is the raw creation text of the
// comment created last (by parsed time; a comment whose text does not parse is
// ignored for that maximum, and an equal time keeps the earlier comment), empty
// when there is none; Updated is the origin's own updated text.
func ContentFromPjira(iss *internal.PjiraIssue, asOf time.Time) groups.SummaryContent {
	is := internal.ToSchemaIssue(iss, asOf)
	is.CommentCount = len(iss.Comments)
	var (
		latest    time.Time
		latestRaw string
	)
	for _, c := range iss.Comments {
		t, ok := parseCommentTime(c.Created)
		if !ok {
			continue
		}
		if latestRaw == "" || t.After(latest) {
			latest, latestRaw = t, c.Created
		}
	}
	is.LatestCommentAt = latestRaw
	return groups.SummaryContent{Issue: *is, Updated: iss.Updated}
}
