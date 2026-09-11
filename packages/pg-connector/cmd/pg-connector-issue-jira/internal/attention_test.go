package internal

import (
	"context"
	"encoding/json"
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
		if !strings.Contains(jql, `duedate is not EMPTY`) || !strings.Contains(jql, `resolution = Unresolved`) {
			t.Fatalf("expected standard due/resolution clauses, got jql=%q", jql)
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
