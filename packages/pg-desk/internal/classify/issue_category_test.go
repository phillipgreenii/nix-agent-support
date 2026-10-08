package classify

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

// catSnap builds an issue snapshot whose issue_show carries state and,
// when category is non-empty, status_category. updated_at is fixed so the
// comments proxy never fires.
func catSnap(state, category string) Snapshot {
	show := map[string]any{"id": "jira-1", "state": state, "updated_at": "2026-10-01T10:00:00Z"}
	if category != "" {
		show["status_category"] = category
	}
	b, _ := json.Marshal(map[string]any{"issue_show": show})
	return Snapshot{Type: "issue", ID: "jira-1", Exists: true, Active: true, Payload: b}
}

// TestIssueClosedKindFollowsJiraStatusCategory: the category, not the state
// name, decides closed/reopened; an empty category falls back to the name rule.
func TestIssueClosedKindFollowsJiraStatusCategory(t *testing.T) {
	cases := []struct {
		name             string
		oldState, oldCat string
		newState, newCat string
		want             []Kind
	}{
		{"done category with a custom name closes", "In Progress", "indeterminate", "Complete", "done", []Kind{KindClosed, KindStatusChanged}},
		{"done category Won't Do closes", "To Do", "new", "Won't Do", "done", []Kind{KindClosed, KindStatusChanged}},
		{"leaving a done category reopens", "Complete", "done", "In Progress", "indeterminate", []Kind{KindReopened, KindStatusChanged}},
		{"new to indeterminate is only a status change", "To Do", "new", "In Progress", "indeterminate", []Kind{KindStatusChanged}},
		{"done to done is only a status change", "Complete", "done", "Released", "done", []Kind{KindStatusChanged}},
		{"category wins over a terminal-looking name", "In Progress", "indeterminate", "Done", "indeterminate", []Kind{KindStatusChanged}},
		{"empty category falls back to the name rule (closes)", "open", "", "closed", "", []Kind{KindClosed, KindStatusChanged}},
		{"empty category, custom name never closes", "open", "", "Complete", "", []Kind{KindStatusChanged}},
		{"unrecognized category is treated as absent", "open", "", "closed", "weird", []Kind{KindClosed, KindStatusChanged}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := issueKinds(catSnap(tc.oldState, tc.oldCat), catSnap(tc.newState, tc.newCat))
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("kinds = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestCategoryRolloutEmitsClosedForAlreadyStoredDoneIssue: an issue stored as
// "Complete" before categories existed, re-read with category done, has the
// same state string and must emit closed.
func TestCategoryRolloutEmitsClosedForAlreadyStoredDoneIssue(t *testing.T) {
	got := issueKinds(catSnap("Complete", ""), catSnap("Complete", "done"))
	if want := []Kind{KindClosed}; !reflect.DeepEqual(got, want) {
		t.Fatalf("kinds = %v, want %v", got, want)
	}
	// A non-done category arriving on an unchanged state emits nothing.
	if got := issueKinds(catSnap("In Progress", ""), catSnap("In Progress", "indeterminate")); got != nil {
		t.Fatalf("category arriving on a non-done issue: kinds = %v, want none", got)
	}
}

// TestCategoryDisappearingEmitsNothing: the old view had a category and the new
// one does not (older pjira, degraded decode): the name fallback must not
// be read as a state movement.
func TestCategoryDisappearingEmitsNothing(t *testing.T) {
	if got := issueKinds(catSnap("Complete", "done"), catSnap("Complete", "")); got != nil {
		t.Fatalf("same state, category vanished: kinds = %v, want none", got)
	}
	// Different state strings: only status_changed, never closed/reopened/opened.
	if got, want := issueKinds(catSnap("Complete", "done"), catSnap("Released", "")), []Kind{KindStatusChanged}; !reflect.DeepEqual(got, want) {
		t.Fatalf("state differs, category vanished: kinds = %v, want %v", got, want)
	}
	if got, want := issueKinds(catSnap("In Progress", "indeterminate"), catSnap("open", "")), []Kind{KindStatusChanged}; !reflect.DeepEqual(got, want) {
		t.Fatalf("to open, category vanished: kinds = %v, want %v", got, want)
	}
}

// TestSourceTerminalFollowsJiraCategory: SourceTerminal's issue branch uses the
// category when present and the name set otherwise; its signature is unchanged.
func TestSourceTerminalFollowsJiraCategory(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name  string
		facts string
		want  bool
	}{
		{"done category, custom name", `{"issue_show":{"state":"Complete","status_category":"done"}}`, true},
		{"done category, upper case", `{"issue_show":{"state":"Released","status_category":"DONE"}}`, true},
		{"indeterminate category", `{"issue_show":{"state":"In Progress","status_category":"indeterminate"}}`, false},
		{"new category", `{"issue_show":{"state":"To Do","status_category":"new"}}`, false},
		{"category wins over a terminal-looking name", `{"issue_show":{"state":"Done","status_category":"indeterminate"}}`, false},
		{"no category, terminal name", `{"issue_show":{"state":"Closed"}}`, true},
		{"no category, custom name", `{"issue_show":{"state":"Complete"}}`, false},
		{"empty category, terminal name", `{"issue_show":{"state":"done","status_category":""}}`, true},
		{"no issue_show", `{}`, false},
		{"undecodable", `{`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := SourceTerminal("issue", json.RawMessage(tc.facts), now, 0); got != tc.want {
				t.Errorf("SourceTerminal = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestIssueStatusKindHelper pins the exported pure helper the focus rank calls.
func TestIssueStatusKindHelper(t *testing.T) {
	for _, tc := range []struct {
		name        string
		facts       string
		wantKind    IssueStatus
		wantPresent bool
	}{
		{"new", `{"issue_show":{"state":"To Do","status_category":"new"}}`, IssueStatusNotStarted, true},
		{"indeterminate", `{"issue_show":{"state":"Doing","status_category":"indeterminate"}}`, IssueStatusInProgress, true},
		{"done", `{"issue_show":{"state":"Complete","status_category":"done"}}`, IssueStatusDone, true},
		{"case and space tolerant", `{"issue_show":{"state":"x","status_category":" Done "}}`, IssueStatusDone, true},
		{"absent, terminal name", `{"issue_show":{"state":"cancelled"}}`, IssueStatusDone, false},
		{"absent, other name", `{"issue_show":{"state":"In Progress"}}`, IssueStatusUnknown, false},
		{"unrecognized category is absent", `{"issue_show":{"state":"open","status_category":"weird"}}`, IssueStatusUnknown, false},
		{"category wins over terminal name", `{"issue_show":{"state":"closed","status_category":"new"}}`, IssueStatusNotStarted, true},
		{"empty payload", ``, IssueStatusUnknown, false},
		{"no show", `{"issue_deps":{"ids":[]}}`, IssueStatusUnknown, false},
		{"undecodable", `not json`, IssueStatusUnknown, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			kind, present := IssueStatusKind(json.RawMessage(tc.facts))
			if kind != tc.wantKind || present != tc.wantPresent {
				t.Errorf("IssueStatusKind = (%q, %v), want (%q, %v)", kind, present, tc.wantKind, tc.wantPresent)
			}
		})
	}
}
