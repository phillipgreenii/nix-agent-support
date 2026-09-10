package schema

import (
	"encoding/json"
	"strings"
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
	// as_of/stale are NOT omitempty (bead pg2-2j5ac.28.3, mirroring PR/CIRun)
	// — they always appear even on a zero-value struct.
	want := `{"id":"issue-1","title":"t","state":"open","url":"u","as_of":"","stale":false}`
	if string(raw) != want {
		t.Fatalf("got %s, want %s", raw, want)
	}
}

func TestIssue_IDIsString(t *testing.T) {
	// Issue.ID must be a string, carried over as-is from pg-pr's existing
	// api.Issue.ID string field — a compile-time assertion that this field
	// is string-typed, not numeric.
	var _ string = Issue{}.ID //nolint:staticcheck // QF1011: explicit type IS the assertion; omitting it would infer from the field and defeat the check.
}

// TestIssue_AsOfAndStale_AlwaysPresentInJSON mirrors
// TestPR_AsOfAndStale_AlwaysPresentInJSON (pr_test.go) and
// TestCIRun_AsOfAndStale_AlwaysPresentInJSON (ci_test.go): as_of/stale must
// always be present, since Stale=false is itself informative and a
// consumer must be able to distinguish "explicitly not stale" from "field
// absent" (bead pg2-2j5ac.28.3).
func TestIssue_AsOfAndStale_AlwaysPresentInJSON(t *testing.T) {
	raw, err := json.Marshal(Issue{ID: "issue-1", AsOf: "2026-09-10T00:00:00Z", Stale: false})
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

// TestIssue_FreshnessAndMetadataFields_RoundTripAndOmitWhenEmpty covers the
// four new field-shape additions this bead's own Contract names
// (UpdatedAt, DueDate, Metadata, ExternalRefs) beyond the AsOf/Stale pair
// already tested above.
func TestIssue_FreshnessAndMetadataFields_RoundTripAndOmitWhenEmpty(t *testing.T) {
	empty, err := json.Marshal(Issue{ID: "issue-1"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, key := range []string{"updated_at", "due_date", "metadata", "external_refs"} {
		if strings.Contains(string(empty), `"`+key+`"`) {
			t.Fatalf("expected %q omitted when empty, got %s", key, empty)
		}
	}

	in := Issue{
		ID:           "issue-1",
		UpdatedAt:    "2026-09-10T00:00:00Z",
		DueDate:      "2027-01-15T00:00:00Z",
		Metadata:     map[string]string{"foo": "bar", "num": "42"},
		ExternalRefs: []string{"gh-9"},
	}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out Issue
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.UpdatedAt != in.UpdatedAt || out.DueDate != in.DueDate {
		t.Fatalf("got %+v, want %+v", out, in)
	}
	if len(out.Metadata) != 2 || out.Metadata["foo"] != "bar" || out.Metadata["num"] != "42" {
		t.Fatalf("Metadata round-trip mismatch: got %+v", out.Metadata)
	}
	if len(out.ExternalRefs) != 1 || out.ExternalRefs[0] != "gh-9" {
		t.Fatalf("ExternalRefs round-trip mismatch: got %+v", out.ExternalRefs)
	}
}

// TestIssueDepsResult_IDsPopulatedRegardlessOfFull_EntitiesOmittedWhenEmpty
// locks in IssueDepsResult's own wire shape: ids is never omitempty (a
// well-formed empty-set answer still writes "ids":[]), entities is
// omitempty (never sent unless the caller passed full=true).
func TestIssueDepsResult_IDsPopulatedRegardlessOfFull_EntitiesOmittedWhenEmpty(t *testing.T) {
	raw, err := json.Marshal(IssueDepsResult{IDs: []string{}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `{"ids":[]}`
	if string(raw) != want {
		t.Fatalf("got %s, want %s", raw, want)
	}

	full, err := json.Marshal(IssueDepsResult{IDs: []string{"issue-2"}, Entities: []Issue{{ID: "issue-2"}}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out IssueDepsResult
	if err := json.Unmarshal(full, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out.IDs) != 1 || out.IDs[0] != "issue-2" {
		t.Fatalf("IDs = %+v", out.IDs)
	}
	if len(out.Entities) != 1 || out.Entities[0].ID != "issue-2" {
		t.Fatalf("Entities = %+v", out.Entities)
	}
}
