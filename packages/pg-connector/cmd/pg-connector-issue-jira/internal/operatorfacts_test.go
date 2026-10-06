package internal

import (
	"context"
	"errors"
	"strings"
	"testing"
)

const factsOperator = "operator@example.com"

// factsIssueJSON renders `pjira issue` output for an issue in status.
func factsIssueJSON(key, status, assigneeEmail string) string {
	assignee := ""
	if assigneeEmail != "" {
		assignee = `,"assignee":{"email":"` + assigneeEmail + `","display_name":"Someone"}`
	}
	return `{"key":"` + key + `","summary":"s","status":"` + status + `","issuetype":"Task","project":"PROJ"` + assignee + `}`
}

// factsSearchIssue renders the expanded search item for the same issue.
func factsSearchIssue(key, status, created, changelog, comments string) string {
	return `{"items":[{"key":"` + key + `","summary":"s","status":"` + status + `","created":"` + created +
		`","changelog":[` + changelog + `],"comments":[` + comments + `]}],"truncated":false}`
}

func factsTransition(id, to, authorEmail, at string) string {
	return `{"id":"` + id + `","field":"status","from":"To Do","to":"` + to + `","author":{"email":"` + authorEmail + `"},"at":"` + at + `"}`
}

func factsComment(id, authorEmail, created string) string {
	return `{"id":"` + id + `","author":{"email":"` + authorEmail + `"},"body":"b","created":"` + created + `"}`
}

func TestShow_OperatorFacts(t *testing.T) {
	tests := []struct {
		name          string
		issue         string
		search        string
		searchErr     error
		env           string
		wantChanged   string
		wantOperator  string
		wantSearchHit bool
	}{
		{
			name:          "latest transition into current status and latest operator update",
			issue:         factsIssueJSON("PROJ-1", "In Progress", factsOperator),
			search:        factsSearchIssue("PROJ-1", "In Progress", "2026-08-01T00:00:00.000+0000", factsTransition("1", "In Progress", "other@example.com", "2026-09-01T10:00:00.000+0000")+","+factsTransition("2", "In Progress", factsOperator, "2026-09-02T10:00:00.000+0000"), factsComment("9", factsOperator, "2026-09-10T08:30:00.000+0000")),
			env:           factsOperator,
			wantChanged:   "2026-09-02T10:00:00Z",
			wantOperator:  "2026-09-10T08:30:00Z",
			wantSearchHit: true,
		},
		{
			name:          "other users and bots do not count as operator updates",
			issue:         factsIssueJSON("PROJ-1", "In Progress", factsOperator),
			search:        factsSearchIssue("PROJ-1", "In Progress", "2026-08-01T00:00:00.000+0000", factsTransition("1", "In Progress", factsOperator, "2026-09-01T10:00:00.000+0000"), factsComment("5", "bot@example.com", "2026-09-20T00:00:00.000+0000")+","+factsComment("6", "other@example.com", "2026-09-21T00:00:00.000+0000")),
			env:           factsOperator,
			wantChanged:   "2026-09-01T10:00:00Z",
			wantOperator:  "2026-09-01T10:00:00Z",
			wantSearchHit: true,
		},
		{
			name:          "operator transition later than operator comment wins",
			issue:         factsIssueJSON("PROJ-1", "In Progress", factsOperator),
			search:        factsSearchIssue("PROJ-1", "In Progress", "2026-08-01T00:00:00.000+0000", factsTransition("1", "In Progress", factsOperator, "2026-09-12T10:00:00.000+0000"), factsComment("5", factsOperator, "2026-09-03T00:00:00.000+0000")),
			env:           factsOperator,
			wantChanged:   "2026-09-12T10:00:00Z",
			wantOperator:  "2026-09-12T10:00:00Z",
			wantSearchHit: true,
		},
		{
			name:          "a transition to a different status than the current one is not the entry time",
			issue:         factsIssueJSON("PROJ-1", "Done", factsOperator),
			search:        factsSearchIssue("PROJ-1", "Done", "2026-08-01T00:00:00.000+0000", factsTransition("1", "In Progress", factsOperator, "2026-09-01T10:00:00.000+0000")+","+factsTransition("2", "Done", "other@example.com", "2026-09-05T10:00:00.000+0000"), ""),
			env:           factsOperator,
			wantChanged:   "2026-09-05T10:00:00Z",
			wantOperator:  "2026-09-01T10:00:00Z",
			wantSearchHit: true,
		},
		{
			name:          "never transitioned: creation time is the entry time and the operator has not updated",
			issue:         factsIssueJSON("PROJ-1", "In Progress", factsOperator),
			search:        factsSearchIssue("PROJ-1", "In Progress", "2026-08-01T00:00:00.000+0000", "", ""),
			env:           factsOperator,
			wantChanged:   "2026-08-01T00:00:00Z",
			wantOperator:  "",
			wantSearchHit: true,
		},
		{
			name:          "issue assigned to someone else costs no extra call and carries no facts",
			issue:         factsIssueJSON("PROJ-1", "In Progress", "other@example.com"),
			env:           factsOperator,
			wantSearchHit: false,
		},
		{
			name:          "unassigned issue costs no extra call",
			issue:         factsIssueJSON("PROJ-1", "In Progress", ""),
			env:           factsOperator,
			wantSearchHit: false,
		},
		{
			name:          "search failure leaves the facts empty and Show still answers",
			issue:         factsIssueJSON("PROJ-1", "In Progress", factsOperator),
			searchErr:     errors.New("pjira: search: status 500 Internal Server Error"),
			env:           factsOperator,
			wantSearchHit: true,
		},
		{
			name:          "operator email matching is case-insensitive",
			issue:         factsIssueJSON("PROJ-1", "In Progress", "Operator@Example.com"),
			search:        factsSearchIssue("PROJ-1", "In Progress", "2026-08-01T00:00:00.000+0000", "", factsComment("5", "OPERATOR@example.com", "2026-09-03T00:00:00.000+0000")),
			env:           factsOperator,
			wantChanged:   "2026-08-01T00:00:00Z",
			wantOperator:  "2026-09-03T00:00:00Z",
			wantSearchHit: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var searches [][]string
			fr := &fakeRunner{handle: func(args []string) (string, error) {
				switch args[0] {
				case "issue":
					return tc.issue, nil
				case "search":
					searches = append(searches, args)
					return tc.search, tc.searchErr
				}
				return "", errors.New("unexpected pjira op " + args[0])
			}}
			b := New(fr)
			b.getenv = func(k string) string {
				if k == EnvEmail {
					return tc.env
				}
				return ""
			}
			got, err := b.Show(context.Background(), "PROJ-1")
			if err != nil {
				t.Fatalf("Show: %v", err)
			}
			if (len(searches) > 0) != tc.wantSearchHit {
				t.Fatalf("search calls = %d, want hit=%v", len(searches), tc.wantSearchHit)
			}
			if got.StatusChangedAt != tc.wantChanged {
				t.Errorf("StatusChangedAt = %q, want %q", got.StatusChangedAt, tc.wantChanged)
			}
			if got.OperatorUpdatedAt != tc.wantOperator {
				t.Errorf("OperatorUpdatedAt = %q, want %q", got.OperatorUpdatedAt, tc.wantOperator)
			}
			if got.ID != "PROJ-1" {
				t.Errorf("ID = %q: Show must still answer", got.ID)
			}
			if tc.wantSearchHit {
				joined := strings.Join(searches[0], " ")
				if !strings.Contains(joined, `key = "PROJ-1"`) || !strings.Contains(joined, "changelog,comments") {
					t.Errorf("search args = %q, want a key-scoped search expanding changelog,comments", joined)
				}
			}
		})
	}
}

// A key that is not issue-key-shaped is never interpolated into JQL.
func TestShow_OperatorFacts_RejectsNonKeyShape(t *testing.T) {
	fr := &fakeRunner{handle: func(args []string) (string, error) {
		if args[0] == "search" {
			t.Fatalf("search must not run for a malformed key: %v", args)
		}
		return `{"key":"PROJ-1\" OR 1=1","summary":"s","status":"To Do","assignee":{"email":"` + factsOperator + `"}}`, nil
	}}
	b := New(fr)
	b.getenv = func(string) string { return factsOperator }
	if _, err := b.Show(context.Background(), "PROJ-1"); err != nil {
		t.Fatalf("Show: %v", err)
	}
}
