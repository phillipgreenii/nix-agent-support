package interpret

import (
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
)

func TestDeriveIssueAttentionFacts(t *testing.T) {
	show := func(body string) string { return `{"issue_show":` + body + `}` }
	at := func(s string) time.Time {
		t.Helper()
		v, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}

	tests := []struct {
		name  string
		facts string
		cfg   *config.Config
		want  IssueAttentionFacts
	}{
		{
			name:  "in progress with operator facts",
			facts: show(`{"id":"K-1","state":"In Progress","assignee":"Someone","status_changed_at":"2026-09-01T10:00:00Z","operator_updated_at":"2026-09-10T08:30:00Z"}`),
			want: IssueAttentionFacts{
				Status: "In Progress", StatusCategory: IssueStatusCategoryInProgress, Assignee: "Someone",
				InProgressSince: at("2026-09-01T10:00:00Z"), OperatorFactsKnown: true, OperatorUpdatedAt: at("2026-09-10T08:30:00Z"),
			},
		},
		{
			name:  "status matching is case-insensitive",
			facts: show(`{"state":"in progress","status_changed_at":"2026-09-01T10:00:00Z"}`),
			want: IssueAttentionFacts{
				Status: "in progress", StatusCategory: IssueStatusCategoryInProgress,
				InProgressSince: at("2026-09-01T10:00:00Z"), OperatorFactsKnown: true,
			},
		},
		{
			name:  "configured in-progress statuses replace the default",
			facts: show(`{"state":"Doing","status_changed_at":"2026-09-01T10:00:00Z"}`),
			cfg:   &config.Config{Jira: &config.JiraConfig{InProgressStatuses: []string{"Doing", "Review"}}},
			want: IssueAttentionFacts{
				Status: "Doing", StatusCategory: IssueStatusCategoryInProgress,
				InProgressSince: at("2026-09-01T10:00:00Z"), OperatorFactsKnown: true,
			},
		},
		{
			name:  "the default status is not in progress once the config replaces it",
			facts: show(`{"state":"In Progress","status_changed_at":"2026-09-01T10:00:00Z"}`),
			cfg:   &config.Config{Jira: &config.JiraConfig{InProgressStatuses: []string{"Doing"}}},
			want:  IssueAttentionFacts{Status: "In Progress", StatusCategory: IssueStatusCategoryOther, OperatorFactsKnown: true},
		},
		{
			name:  "another status is not in progress and carries no in-progress-since",
			facts: show(`{"state":"To Do","assignee":"Someone","status_changed_at":"2026-09-01T10:00:00Z"}`),
			want:  IssueAttentionFacts{Status: "To Do", StatusCategory: IssueStatusCategoryOther, Assignee: "Someone", OperatorFactsKnown: true},
		},
		{
			name:  "no operator facts from the connector means unknown, not never-updated",
			facts: show(`{"state":"In Progress","assignee":"Someone"}`),
			want:  IssueAttentionFacts{Status: "In Progress", StatusCategory: IssueStatusCategoryInProgress, Assignee: "Someone"},
		},
		{
			name:  "an unparsable timestamp is unknown",
			facts: show(`{"state":"In Progress","status_changed_at":"yesterday","operator_updated_at":"2026-09-10"}`),
			want:  IssueAttentionFacts{Status: "In Progress", StatusCategory: IssueStatusCategoryInProgress},
		},
		{
			name:  "offset timestamps normalize to UTC",
			facts: show(`{"state":"In Progress","status_changed_at":"2026-09-01T12:00:00+02:00"}`),
			want: IssueAttentionFacts{
				Status: "In Progress", StatusCategory: IssueStatusCategoryInProgress,
				InProgressSince: at("2026-09-01T10:00:00Z"), OperatorFactsKnown: true,
			},
		},
		{
			name:  "a bare due date is date-only, at midnight UTC of the due day",
			facts: show(`{"state":"To Do","due_date":"2026-10-09"}`),
			want:  IssueAttentionFacts{Status: "To Do", StatusCategory: IssueStatusCategoryOther, DueDateKnown: true, DueDateOnly: true, DueDate: at("2026-10-09T00:00:00Z")},
		},
		{
			name:  "an RFC3339 due date is an instant, normalized to UTC",
			facts: show(`{"state":"open","due_date":"2026-10-09T12:00:00+02:00"}`),
			want:  IssueAttentionFacts{Status: "open", StatusCategory: IssueStatusCategoryOther, DueDateKnown: true, DueDate: at("2026-10-09T10:00:00Z")},
		},
		{
			name:  "an unreadable due date is unknown, not zero",
			facts: show(`{"state":"open","due_date":"soon"}`),
			want:  IssueAttentionFacts{Status: "open", StatusCategory: IssueStatusCategoryOther},
		},
		{
			name:  "a done status (case-insensitive, covering beads' closed) is done",
			facts: show(`{"state":"CLOSED","due_date":"2026-10-09"}`),
			want:  IssueAttentionFacts{Status: "CLOSED", StatusCategory: IssueStatusCategoryDone, Done: true, DueDateKnown: true, DueDateOnly: true, DueDate: at("2026-10-09T00:00:00Z")},
		},
		{
			name:  "configured done statuses replace the default",
			facts: show(`{"state":"Shipped"}`),
			cfg:   &config.Config{Jira: &config.JiraConfig{DoneStatuses: []string{"Shipped"}}},
			want:  IssueAttentionFacts{Status: "Shipped", StatusCategory: IssueStatusCategoryDone, Done: true},
		},
		{
			name:  "the default done status is not done once the config replaces the list",
			facts: show(`{"state":"Done"}`),
			cfg:   &config.Config{Jira: &config.JiraConfig{DoneStatuses: []string{"Shipped"}}},
			want:  IssueAttentionFacts{Status: "Done", StatusCategory: IssueStatusCategoryOther},
		},
		{
			name:  "a status listed as both in progress and done stays in progress",
			facts: show(`{"state":"Review"}`),
			cfg:   &config.Config{Jira: &config.JiraConfig{InProgressStatuses: []string{"Review"}, DoneStatuses: []string{"Review"}}},
			want:  IssueAttentionFacts{Status: "Review", StatusCategory: IssueStatusCategoryInProgress, Done: true},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := DeriveIssueAttentionFacts(tc.facts, tc.cfg)
			if err != nil {
				t.Fatalf("DeriveIssueAttentionFacts: %v", err)
			}
			if got != tc.want {
				t.Errorf("facts = %+v\n want   %+v", got, tc.want)
			}
		})
	}

	t.Run("missing or malformed facts are an error", func(t *testing.T) {
		for _, bad := range []string{`{}`, `not json`, `{"issue_show":"x"}`} {
			if _, err := DeriveIssueAttentionFacts(bad, nil); err == nil || !strings.Contains(err.Error(), "interpret:") {
				t.Errorf("%q: err = %v, want an interpret error", bad, err)
			}
		}
	})
}
