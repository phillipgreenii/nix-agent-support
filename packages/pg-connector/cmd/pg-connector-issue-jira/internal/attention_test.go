package internal

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

func TestAttentionThresholdFrom_DefaultsWhenAbsentOrMalformed(t *testing.T) {
	cases := []json.RawMessage{
		nil,
		[]byte(`{}`),
		[]byte(`{"attention_threshold":""}`),
		[]byte(`{"attention_threshold":"nope"}`),
	}
	for _, c := range cases {
		if got := attentionThresholdFrom(c); got != defaultAttentionThreshold {
			t.Fatalf("attentionThresholdFrom(%s) = %v, want default %v", c, got, defaultAttentionThreshold)
		}
	}
}

func TestAttentionThresholdFrom_Configured(t *testing.T) {
	if got := attentionThresholdFrom([]byte(`{"attention_threshold":"48h"}`)); got.Hours() != 48 {
		t.Fatalf("attentionThresholdFrom = %v, want 48h", got)
	}
}

func TestAttentionExcludeFrom(t *testing.T) {
	if got := attentionExcludeFrom(nil); got != "" {
		t.Fatalf("attentionExcludeFrom(nil) = %q, want empty", got)
	}
	if got := attentionExcludeFrom([]byte(`{"attention_exclude":"labels = no-attention"}`)); got != "labels = no-attention" {
		t.Fatalf("attentionExcludeFrom = %q, want %q", got, "labels = no-attention")
	}
}

// TestBackend_ListAttention_DueSoonAndOverdueSplit proves ListAttention
// issues the due-within-threshold JQL search and the strictly-overdue JQL
// search, ANDing in the exclude fragment on both, and classifies severity
// from membership in the overdue set.
func TestBackend_ListAttention_DueSoonAndOverdueSplit(t *testing.T) {
	fr := &fakeRunner{handle: func(args []string) (string, error) {
		if args[0] != "search" {
			t.Fatalf("unexpected op: %v", args)
		}
		jql := args[2] // "search", "--jql", "<jql>", "--all"
		if !strings.Contains(jql, "AND NOT (labels = no-attention)") {
			t.Fatalf("expected exclude fragment ANDed out, got jql=%q", jql)
		}
		if !strings.Contains(jql, `duedate is not EMPTY`) || !strings.Contains(jql, `statusCategory != Done`) {
			t.Fatalf("expected standard due/statusCategory clauses, got jql=%q", jql)
		}
		if strings.Contains(jql, "duedate <=") {
			return `{"items":[
				{"key":"TP-1","summary":"overdue one","status":"To Do","issuetype":"Task","url":"https://example.invalid/TP-1","duedate":"2020-01-01"},
				{"key":"TP-2","summary":"due soon","status":"To Do","issuetype":"Task","url":"https://example.invalid/TP-2","duedate":"2099-01-01"}
			],"truncated":false}`, nil
		}
		if strings.Contains(jql, "duedate <") {
			return `{"items":[
				{"key":"TP-1","summary":"overdue one","status":"To Do","issuetype":"Task","url":"https://example.invalid/TP-1","duedate":"2020-01-01"}
			],"truncated":false}`, nil
		}
		t.Fatalf("unexpected jql: %q", jql)
		return "", nil
	}}
	b := New(fr)

	ctx := scriptout.WithConfig(context.Background(), json.RawMessage(`{"attention_exclude":"labels = no-attention"}`))
	got, err := b.ListAttention(ctx)
	if err != nil {
		t.Fatalf("ListAttention: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2: %+v", len(got), got)
	}
	byID := make(map[string]schema.AttentionItem, len(got))
	for _, it := range got {
		byID[it.ID] = it
	}
	if byID["TP-1"].Severity != schema.SeverityHigh {
		t.Fatalf("TP-1 severity = %q, want high (overdue)", byID["TP-1"].Severity)
	}
	if byID["TP-2"].Severity != schema.SeverityMedium {
		t.Fatalf("TP-2 severity = %q, want medium (due soon, not yet overdue)", byID["TP-2"].Severity)
	}
}

func TestBackend_ListAttention_NoExcludeConfigured(t *testing.T) {
	fr := &fakeRunner{handle: func(args []string) (string, error) {
		jql := args[2]
		if strings.Contains(jql, "AND NOT") {
			t.Fatalf("expected no exclude fragment when unconfigured: %q", jql)
		}
		return `{"items":[],"truncated":false}`, nil
	}}
	b := New(fr)
	got, err := b.ListAttention(context.Background())
	if err != nil {
		t.Fatalf("ListAttention: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("len(got) = %d, want 0", len(got))
	}
}

// TestBackend_ListAttention_ExcludesDoneCategoryWithNullResolution proves
// the pg2-uf2pq fix: a fixture issue moved to a Done-category status
// (e.g. "Closed") without ever having its resolution field set — this
// org's real Jira data does exactly this (pg2-uf2pq, live-verified
// against FSTS-2296: status=Closed, resolution=null) — is excluded by
// attentionSearch's own "AND statusCategory != Done" clause even though
// the old "resolution = Unresolved" clause alone would have let it leak
// through forever (a null resolution reads as Unresolved in JQL). The
// fake stands in for pjira's own server-side JQL evaluation (mirroring
// this file's other tests, which already key a fake's canned response
// off substrings of the generated JQL rather than modeling real JQL
// parsing): it omits the Done-category fixture from its response only
// when the JQL it was actually given carries the statusCategory clause,
// so this test fails if that clause is ever dropped from the generated
// JQL.
func TestBackend_ListAttention_ExcludesDoneCategoryWithNullResolution(t *testing.T) {
	// doneCategoryClosedFixture mirrors FSTS-2296's real shape from the
	// bug report: transitioned to a Done-category status with resolution
	// never set. pjiraIssue decodes no resolution/statusCategory field of
	// its own (this backend never inspects either client-side — filtering
	// is entirely server-side JQL, per this file's own doc comments), so
	// both are named here only to document the fixture's real-world
	// shape; the fake below decides inclusion from the JQL string, not
	// these JSON fields.
	const doneCategoryClosedFixture = `{"key":"TP-CLOSED","summary":"stale, already closed","status":"Closed","issuetype":"Task","url":"https://example.invalid/TP-CLOSED","duedate":"2020-01-01"}`
	const stillOpenFixture = `{"key":"TP-OPEN","summary":"genuinely still open","status":"To Do","issuetype":"Task","url":"https://example.invalid/TP-OPEN","duedate":"2020-01-01"}`

	fr := &fakeRunner{handle: func(args []string) (string, error) {
		jql := args[2] // "search", "--jql", "<jql>", "--all"
		items := stillOpenFixture
		if !strings.Contains(jql, "statusCategory != Done") {
			items += "," + doneCategoryClosedFixture
		}
		return fmt.Sprintf(`{"items":[%s],"truncated":false}`, items), nil
	}}
	b := New(fr)

	got, err := b.ListAttention(context.Background())
	if err != nil {
		t.Fatalf("ListAttention: %v", err)
	}
	for _, it := range got {
		if it.ID == "TP-CLOSED" {
			t.Fatalf("Done-category issue with null resolution leaked through: %+v", got)
		}
	}
	found := false
	for _, it := range got {
		if it.ID == "TP-OPEN" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected the still-open fixture to be present: %+v", got)
	}
}

// TestToSchemaIssue_PopulatesDueDate proves the bead pg2-7wqkr fix: pjira's
// own duedate field (now decoded by pjiraIssue) is carried through to
// schema.Issue.DueDate.
func TestToSchemaIssue_PopulatesDueDate(t *testing.T) {
	due := "2026-09-20"
	iss := &pjiraIssue{Key: "TP-9", Summary: "s", Duedate: &due}
	got := toSchemaIssue(iss, time.Now())
	if got.DueDate != due {
		t.Fatalf("DueDate = %q, want %q", got.DueDate, due)
	}
}
