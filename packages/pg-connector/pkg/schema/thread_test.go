package schema

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestThread_JSONRoundTrip mirrors TestIssue_JSONRoundTrip (issue_test.go):
// every field round-trips through JSON with its documented wire name.
func TestThread_JSONRoundTrip(t *testing.T) {
	in := Thread{
		ID:           "1726000000.000100",
		Channel:      "C0123ABCD",
		Permalink:    "https://example.slack.invalid/archives/C0123ABCD/p1726000000000100",
		StartedBy:    "U0123ABCD",
		Participants: []string{"U0123ABCD", "U0456EFGH"},
		LastReplyAt:  "1726000100.000200",
		ReplyCount:   3,
		Text:         "root message",
		MentionsMe:   true,
		AsOf:         "2026-09-17T00:00:00Z",
		Stale:        false,
	}

	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var out Thread
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if out.ID != in.ID || out.Channel != in.Channel || out.Permalink != in.Permalink ||
		out.StartedBy != in.StartedBy || out.LastReplyAt != in.LastReplyAt ||
		out.ReplyCount != in.ReplyCount || out.Text != in.Text || out.MentionsMe != in.MentionsMe ||
		out.AsOf != in.AsOf || out.Stale != in.Stale {
		t.Fatalf("round-trip mismatch (scalars): got %+v, want %+v", out, in)
	}
	if len(out.Participants) != len(in.Participants) {
		t.Fatalf("participants round-trip mismatch: got %+v, want %+v", out.Participants, in.Participants)
	}
	for i := range in.Participants {
		if out.Participants[i] != in.Participants[i] {
			t.Fatalf("participants round-trip mismatch: got %+v, want %+v", out.Participants, in.Participants)
		}
	}
}

// TestThread_OptionalFieldsOmittedWhenEmpty mirrors
// TestIssue_OptionalFieldsOmittedWhenEmpty: started_by/participants/
// last_reply_at are omitempty, but id/channel/permalink/text/reply_count/
// mentions_me/as_of/stale always appear, even on a zero-value struct
// (reply_count 0 and mentions_me false are themselves informative facts,
// not absent data).
func TestThread_OptionalFieldsOmittedWhenEmpty(t *testing.T) {
	raw, err := json.Marshal(Thread{ID: "t1", Channel: "C1", Permalink: "https://x.invalid", Text: "hi"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `{"id":"t1","channel":"C1","permalink":"https://x.invalid","reply_count":0,"text":"hi","mentions_me":false,"as_of":"","stale":false}`
	if string(raw) != want {
		t.Fatalf("got %s, want %s", raw, want)
	}
	for _, key := range []string{"started_by", "participants", "last_reply_at"} {
		if strings.Contains(string(raw), `"`+key+`"`) {
			t.Fatalf("expected %q omitted when empty, got %s", key, raw)
		}
	}
}

// TestThread_AsOfAndStale_AlwaysPresentInJSON mirrors
// TestIssue_AsOfAndStale_AlwaysPresentInJSON: as_of/stale must always be
// present, since Stale=false is itself informative and a consumer must be
// able to distinguish "explicitly not stale" from "field absent".
func TestThread_AsOfAndStale_AlwaysPresentInJSON(t *testing.T) {
	raw, err := json.Marshal(Thread{ID: "t1", AsOf: "2026-09-17T00:00:00Z", Stale: false})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := out["as_of"]; !ok {
		t.Fatalf("as_of missing from %s", raw)
	}
	staleVal, ok := out["stale"]
	if !ok {
		t.Fatalf("stale missing from %s", raw)
	}
	if staleVal != false {
		t.Fatalf("stale = %v, want false", staleVal)
	}
}

// TestThread_IDIsString mirrors TestIssue_IDIsString: a compile-time
// assertion that Thread.ID is string-typed, not numeric — Slack's own
// thread-identifying "ts" value is conventionally carried as a string.
func TestThread_IDIsString(t *testing.T) {
	var _ string = Thread{}.ID //nolint:staticcheck // QF1011: explicit type IS the assertion; omitting it would infer from the field and defeat the check.
}

// TestThreadListResult_TruncatedUnconditionallyTrue_WireShape locks in the
// binding decision this bead's own Contract states: every "list" reply's
// wire shape carries truncated:true. This is a schema-level shape lock
// only — the backend-level "always set Truncated true, never compute it
// conditionally" behavior is exercised by
// cmd/pg-connector-thread-slack/internal's own backend_test.go.
func TestThreadListResult_TruncatedUnconditionallyTrue_WireShape(t *testing.T) {
	raw, err := json.Marshal(ThreadListResult{
		Entities:   []Thread{{ID: "t1"}},
		PresentIDs: []string{"t1"},
		Cursor:     nil,
		Truncated:  true,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out["truncated"] != true {
		t.Fatalf("truncated = %v, want true", out["truncated"])
	}
	if out["cursor"] != nil {
		t.Fatalf("cursor = %v, want null (this capability has no incremental-listing backend)", out["cursor"])
	}
}
