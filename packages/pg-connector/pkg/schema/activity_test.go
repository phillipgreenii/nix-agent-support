package schema

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestActivitySchemaVersion_RegisteredInCurrentSchemaVersions(t *testing.T) {
	if ActivitySchemaVersion != 1 {
		t.Fatalf("ActivitySchemaVersion = %d, want 1", ActivitySchemaVersion)
	}
	got, ok := CurrentSchemaVersions["activity"]
	if !ok {
		t.Fatal(`CurrentSchemaVersions has no "activity" key`)
	}
	if got != ActivitySchemaVersion {
		t.Fatalf(`CurrentSchemaVersions["activity"] = %d, want %d`, got, ActivitySchemaVersion)
	}
}

// TestActivityItem_JSONShape_OmitsOptionalWhenEmpty pins which keys are
// omitted when empty (approximate, url, labels) and which are always
// emitted (fields, as_of, stale).
func TestActivityItem_JSONShape_OmitsOptionalWhenEmpty(t *testing.T) {
	raw, err := json.Marshal(ActivityItem{
		ID: "o/r#1:merged", Kind: "pr.merged", EntityType: "pr", EntityID: "o/r#1",
		OccurredAt: "2026-01-02T03:04:05Z", Summary: "merged", Fields: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `{"id":"o/r#1:merged","kind":"pr.merged","entity_type":"pr","entity_id":"o/r#1","occurred_at":"2026-01-02T03:04:05Z","summary":"merged","fields":{},"as_of":"","stale":false}`
	if string(raw) != want {
		t.Fatalf("got  %s\nwant %s", raw, want)
	}

	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, k := range []string{"approximate", "url", "labels"} {
		if _, ok := decoded[k]; ok {
			t.Errorf("key %q must be omitted when empty, got %s", k, raw)
		}
	}
	for _, k := range []string{"fields", "as_of", "stale"} {
		if _, ok := decoded[k]; !ok {
			t.Errorf("key %q must always be emitted, got %s", k, raw)
		}
	}
}

func TestActivityItem_JSONShape_ZeroValueEmitsAlwaysKeys(t *testing.T) {
	raw, err := json.Marshal(ActivityItem{})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, k := range []string{"fields", "as_of", "stale"} {
		if _, ok := decoded[k]; !ok {
			t.Errorf("zero-value item must emit %q, got %s", k, raw)
		}
	}
}

func TestActivityItem_JSONRoundTrip(t *testing.T) {
	in := ActivityItem{
		ID: "o/r#2:reviewed:9", Kind: "pr.reviewed", EntityType: "pr", EntityID: "o/r#2",
		OccurredAt: "2026-01-02T03:04:05-05:00", Approximate: true,
		Summary: "reviewed", URL: "https://example.invalid/o/r/pull/2",
		Labels: []string{"repo:o/r"}, Fields: json.RawMessage(`{"state":"approved"}`),
		AsOf: "2026-01-03T00:00:00Z", Stale: true,
	}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out ActivityItem
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(in, out) {
		t.Fatalf("round trip mismatch:\n in  %+v\n out %+v", in, out)
	}
}

func TestActivityListArgs_JSONShape(t *testing.T) {
	raw, err := json.Marshal(ActivityListArgs{Before: "2026-02-01T00:00:00Z"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if want := `{"before":"2026-02-01T00:00:00Z"}`; string(raw) != want {
		t.Fatalf("got %s, want %s (since omitted when empty)", raw, want)
	}

	raw, err = json.Marshal(ActivityListArgs{Since: "2026-01-01T00:00:00Z", Before: "2026-02-01T00:00:00Z"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if want := `{"since":"2026-01-01T00:00:00Z","before":"2026-02-01T00:00:00Z"}`; string(raw) != want {
		t.Fatalf("got %s, want %s", raw, want)
	}
}

// TestActivityListResult_JSONShape pins that truncated is always emitted
// (even false) and that an empty, non-nil Items marshals as [].
func TestActivityListResult_JSONShape(t *testing.T) {
	raw, err := json.Marshal(ActivityListResult{Items: []ActivityItem{}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if want := `{"items":[],"truncated":false}`; string(raw) != want {
		t.Fatalf("got %s, want %s", raw, want)
	}

	raw, err = json.Marshal(ActivityListResult{Items: []ActivityItem{}, Truncated: true})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if want := `{"items":[],"truncated":true}`; string(raw) != want {
		t.Fatalf("got %s, want %s", raw, want)
	}
}
