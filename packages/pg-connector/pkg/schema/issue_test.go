package schema

import (
	"encoding/json"
	"testing"
)

func TestIssue_JSONRoundTrip(t *testing.T) {
	in := Issue{
		ID:        "issue-1",
		Title:     "Fix the thing",
		State:     "open",
		URL:       "https://example.invalid/issue/1",
		Priority:  "High",
		Labels:    []string{"bug", "urgent"},
		IssueType: "Bug",
	}

	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var out Issue
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if out.ID != in.ID || out.Title != in.Title || out.State != in.State || out.URL != in.URL ||
		out.Priority != in.Priority || out.IssueType != in.IssueType {
		t.Fatalf("round-trip mismatch: got %+v, want %+v", out, in)
	}
	if len(out.Labels) != len(in.Labels) {
		t.Fatalf("labels round-trip mismatch: got %+v, want %+v", out.Labels, in.Labels)
	}
	for i := range in.Labels {
		if out.Labels[i] != in.Labels[i] {
			t.Fatalf("labels round-trip mismatch: got %+v, want %+v", out.Labels, in.Labels)
		}
	}
}

func TestIssue_OptionalFieldsOmittedWhenEmpty(t *testing.T) {
	raw, err := json.Marshal(Issue{ID: "issue-1", Title: "t", State: "open", URL: "u"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `{"id":"issue-1","title":"t","state":"open","url":"u"}`
	if string(raw) != want {
		t.Fatalf("got %s, want %s", raw, want)
	}
}

func TestIssue_IDIsString(t *testing.T) {
	// Issue.ID must be a string, carried over as-is from pg-pr's existing
	// api.Issue.ID string field — a compile-time assertion that this field
	// is string-typed, not numeric.
	var _ string = Issue{}.ID
}
