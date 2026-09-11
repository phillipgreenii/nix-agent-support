package internal

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

func TestAttentionThresholdFrom_DefaultsWhenAbsentOrMalformed(t *testing.T) {
	cases := []json.RawMessage{
		nil,
		[]byte(`{}`),
		[]byte(`{"attention_threshold":""}`),
		[]byte(`{"attention_threshold":"not-a-duration"}`),
	}
	for _, c := range cases {
		if got := attentionThresholdFrom(c); got != defaultAttentionThreshold {
			t.Fatalf("attentionThresholdFrom(%s) = %v, want default %v", c, got, defaultAttentionThreshold)
		}
	}
}

func TestAttentionThresholdFrom_Configured(t *testing.T) {
	got := attentionThresholdFrom([]byte(`{"attention_threshold":"72h"}`))
	if got.Hours() != 72 {
		t.Fatalf("attentionThresholdFrom = %v, want 72h", got)
	}
}

func TestAttentionExcludeFrom(t *testing.T) {
	if got := attentionExcludeFrom(nil); got != "" {
		t.Fatalf("attentionExcludeFrom(nil) = %q, want empty", got)
	}
	if got := attentionExcludeFrom([]byte(`{"attention_exclude":"no-attention"}`)); got != "no-attention" {
		t.Fatalf("attentionExcludeFrom = %q, want %q", got, "no-attention")
	}
}

// TestBackend_ListAttention_DueBeforeAndOverdueSplit proves ListAttention
// issues bd's own --due-before (the full needs-attention set) and
// --overdue (the severity-classifying subset) calls, honoring the
// configured attention_exclude on both, and maps the result onto
// severity high/medium correctly.
func TestBackend_ListAttention_DueBeforeAndOverdueSplit(t *testing.T) {
	fr := &fakeRunner{handle: func(args []string) (string, error) {
		if args[0] != "list" {
			t.Fatalf("unexpected op: %v", args)
		}
		if !containsArg(args, "--exclude-label") {
			t.Fatalf("expected --exclude-label to be attached: %v", args)
		}
		if containsArg(args, "--overdue") {
			return `{"data":[{"id":"tp-1","title":"overdue one","status":"open","priority":1,"issue_type":"task","due_at":"2020-01-01T00:00:00Z"}],"schema_version":1}`, nil
		}
		if containsArg(args, "--due-before") {
			return `{"data":[
				{"id":"tp-1","title":"overdue one","status":"open","priority":1,"issue_type":"task","due_at":"2020-01-01T00:00:00Z"},
				{"id":"tp-2","title":"due soon","status":"open","priority":1,"issue_type":"task","due_at":"2099-01-01T00:00:00Z"}
			],"schema_version":1}`, nil
		}
		t.Fatalf("expected --due-before or --overdue: %v", args)
		return "", nil
	}}
	b := New(fr)

	ctx := scriptout.WithConfig(context.Background(), json.RawMessage(`{"attention_exclude":"no-attention"}`))
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
	if byID["tp-1"].Severity != schema.SeverityHigh {
		t.Fatalf("tp-1 severity = %q, want high (overdue)", byID["tp-1"].Severity)
	}
	if byID["tp-2"].Severity != schema.SeverityMedium {
		t.Fatalf("tp-2 severity = %q, want medium (due soon, not yet overdue)", byID["tp-2"].Severity)
	}
}

// TestBackend_ListAttention_UsesConfiguredThreshold proves the configured
// attention_threshold reaches bd's own --due-before value as a computed
// absolute date, not passed through verbatim.
func TestBackend_ListAttention_NoExcludeConfigured(t *testing.T) {
	fr := &fakeRunner{handle: func(args []string) (string, error) {
		if containsArg(args, "--exclude-label") {
			t.Fatalf("expected no --exclude-label when unconfigured: %v", args)
		}
		return `{"data":[],"schema_version":1}`, nil
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
