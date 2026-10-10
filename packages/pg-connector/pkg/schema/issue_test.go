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

// TestIssueSchemaVersion_PinnedAtTenAndRegistered pins the issue
// capability's schema version at 10 (bead pg2-2j5ac.44.2 bumped 9 -> 10 for
// the CreatedAt field; pg2-nd60k's earlier 8 -> 9 added the children op) and that CurrentSchemaVersions derives from the
// constant rather than carrying its own copy.
func TestIssueSchemaVersion_PinnedAtTenAndRegistered(t *testing.T) {
	if IssueSchemaVersion != 10 {
		t.Fatalf("IssueSchemaVersion = %d, want 10 (9 -> 10 by bead pg2-2j5ac.44.2)", IssueSchemaVersion)
	}
	if got := CurrentSchemaVersions["issue"]; got != IssueSchemaVersion {
		t.Fatalf(`CurrentSchemaVersions["issue"] = %d, want %d`, got, IssueSchemaVersion)
	}
}

// TestIssue_StatusCategory_JSONRoundTrip: each closed value round-trips under
// the wire key status_category, and an empty category is omitted entirely
// (the beads backend never sets it).
func TestIssue_StatusCategory_JSONRoundTrip(t *testing.T) {
	for _, cat := range []string{"new", "indeterminate", "done"} {
		raw, err := json.Marshal(Issue{ID: "i", StatusCategory: cat})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if !strings.Contains(string(raw), `"status_category":"`+cat+`"`) {
			t.Fatalf("category %q missing from %s", cat, raw)
		}
		var out Issue
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if out.StatusCategory != cat {
			t.Fatalf("round trip: StatusCategory = %q, want %q", out.StatusCategory, cat)
		}
	}
	raw, err := json.Marshal(Issue{ID: "i"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw), "status_category") {
		t.Fatalf("empty StatusCategory must be omitted, got %s", raw)
	}
}

// TestIssueChildrenResult_JSONShape: the wire key is children, each entry is
// the shared Issue shape, and an empty result still carries an empty array
// (never null), so a consumer can tell "no children" from a malformed reply.
func TestIssueChildrenResult_JSONShape(t *testing.T) {
	raw, err := json.Marshal(IssueChildrenResult{Children: []Issue{{ID: "c-1", State: "open"}}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back IssueChildrenResult
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(back.Children) != 1 || back.Children[0].ID != "c-1" || back.Children[0].State != "open" {
		t.Fatalf("round trip = %+v (raw %s)", back, raw)
	}
	empty, err := json.Marshal(IssueChildrenResult{Children: []Issue{}})
	if err != nil {
		t.Fatalf("marshal empty: %v", err)
	}
	if string(empty) != `{"children":[]}` {
		t.Fatalf("empty result = %s, want {\"children\":[]}", empty)
	}
}

// TestIssue_CreatedAt_RoundTripAndOmitWhenEmpty: created_at (bead
// pg2-2j5ac.44.2) is omitted when empty and round-trips verbatim, unparsed
// (a Jira-style offset with no colon is not RFC3339 and must survive as is).
func TestIssue_CreatedAt_RoundTripAndOmitWhenEmpty(t *testing.T) {
	empty, err := json.Marshal(Issue{ID: "issue-1"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(empty), `"created_at"`) {
		t.Fatalf("expected created_at omitted when empty, got %s", empty)
	}
	for _, created := range []string{"2026-01-01T00:00:00Z", "2026-01-01T00:00:00.000+0000"} {
		raw, err := json.Marshal(Issue{ID: "issue-1", CreatedAt: created})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if !strings.Contains(string(raw), `"created_at":"`+created+`"`) {
			t.Fatalf("created_at missing from %s", raw)
		}
		var out Issue
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if out.CreatedAt != created {
			t.Fatalf("CreatedAt = %q, want %q", out.CreatedAt, created)
		}
	}
}
