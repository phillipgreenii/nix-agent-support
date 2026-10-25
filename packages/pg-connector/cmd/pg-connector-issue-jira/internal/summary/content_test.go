package summary

import (
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal"
)

func decode(t *testing.T, raw string) *internal.PjiraIssue {
	t.Helper()
	iss, err := internal.DecodeIssue(raw)
	if err != nil {
		t.Fatal(err)
	}
	return iss
}

func TestContentFromPjiraMapsTheIssueAndTheCommentSummary(t *testing.T) {
	asOf := time.Date(2026, 10, 24, 1, 2, 3, 0, time.UTC)
	iss := decode(t, pj("ABC-7", "indeterminate",
		"2026-10-01T10:00:00.000+0000",
		"2026-10-03T10:00:00.000+0000", // the latest
		"2026-10-02T10:00:00.000+0000"))
	c := ContentFromPjira(iss, asOf)
	if c.Issue.ID != "ABC-7" || c.Issue.Title != "title of ABC-7" || c.Issue.StatusCategory != "indeterminate" {
		t.Errorf("issue = %+v", c.Issue)
	}
	if c.Issue.AsOf != asOf.Format(time.RFC3339) {
		t.Errorf("AsOf = %q, want %q", c.Issue.AsOf, asOf.Format(time.RFC3339))
	}
	if c.Issue.CommentCount != 3 {
		t.Errorf("CommentCount = %d, want 3", c.Issue.CommentCount)
	}
	if c.Issue.LatestCommentAt != "2026-10-03T10:00:00.000+0000" {
		t.Errorf("LatestCommentAt = %q, want the creation text of the latest comment", c.Issue.LatestCommentAt)
	}
	if c.Updated != "2026-10-05T10:11:12.000+0000" || c.Gone {
		t.Errorf("Updated = %q Gone = %v", c.Updated, c.Gone)
	}
}

func TestContentFromPjiraWithoutCommentsHasNoLatestComment(t *testing.T) {
	c := ContentFromPjira(decode(t, pj("ABC-7", "new")), t0)
	if c.Issue.CommentCount != 0 || c.Issue.LatestCommentAt != "" {
		t.Errorf("comment summary = %d %q, want 0 and empty", c.Issue.CommentCount, c.Issue.LatestCommentAt)
	}
}

func TestContentFromPjiraLatestCommentIsByParsedTimeNotByText(t *testing.T) {
	// "2026-10-03T09:00:00.000-0500" is 14:00 UTC, later than the "+0000" 13:00
	// one that sorts after it as text; RFC 3339 spelling counts too; a text that
	// does not parse is ignored for the maximum but still counted.
	iss := decode(t, pj("ABC-7", "new",
		"2026-10-03T13:00:00.000+0000",
		"2026-10-03T09:00:00.000-0500",
		"not a time",
		"2026-10-03T12:00:00Z"))
	c := ContentFromPjira(iss, t0)
	if c.Issue.LatestCommentAt != "2026-10-03T09:00:00.000-0500" {
		t.Errorf("LatestCommentAt = %q, want the -0500 comment (14:00 UTC)", c.Issue.LatestCommentAt)
	}
	if c.Issue.CommentCount != 4 {
		t.Errorf("CommentCount = %d, want 4 (an unparseable comment still counts)", c.Issue.CommentCount)
	}

	rfc := ContentFromPjira(decode(t, pj("ABC-7", "new", "2026-10-03T13:00:00.000+0000", "2026-10-03T13:00:01Z")), t0)
	if rfc.Issue.LatestCommentAt != "2026-10-03T13:00:01Z" {
		t.Errorf("LatestCommentAt = %q, want the RFC 3339 comment one second later", rfc.Issue.LatestCommentAt)
	}
}

func TestContentFromPjiraOnlyUnparseableCommentsLeaveLatestEmpty(t *testing.T) {
	c := ContentFromPjira(decode(t, pj("ABC-7", "new", "garbage")), t0)
	if c.Issue.LatestCommentAt != "" || c.Issue.CommentCount != 1 {
		t.Errorf("got %q count %d, want empty and 1", c.Issue.LatestCommentAt, c.Issue.CommentCount)
	}
}
