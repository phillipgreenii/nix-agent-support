package schema

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAttentionItem_JSONShape_NoSeverity(t *testing.T) {
	// AttentionItem.Severity is omitempty — a source with no opinion omits
	// the field entirely, never a forced default.
	raw, err := json.Marshal(AttentionItem{Type: "pr", ID: "pr-1", Summary: "needs review"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `{"type":"pr","id":"pr-1","summary":"needs review"}`
	if string(raw) != want {
		t.Fatalf("got %s, want %s", raw, want)
	}
}

func TestAttentionItem_JSONShape_WithSeverity(t *testing.T) {
	raw, err := json.Marshal(AttentionItem{Type: "pr", ID: "pr-1", Summary: "needs review", Severity: SeverityHigh})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `{"type":"pr","id":"pr-1","summary":"needs review","severity":"high"}`
	if string(raw) != want {
		t.Fatalf("got %s, want %s", raw, want)
	}
}

// TestAttentionItem_JSONShape_URL pins INV-ATTN-URL-1's wire half: url is
// omitempty (an item with no page omits the key entirely, never "url":"")
// and a set url is emitted verbatim as the last key.
func TestAttentionItem_JSONShape_URL(t *testing.T) {
	raw, err := json.Marshal(AttentionItem{Type: "pr", ID: "o/r#1", Summary: "needs review", Severity: SeverityHigh, URL: "https://example.invalid/o/r/pull/1"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `{"type":"pr","id":"o/r#1","summary":"needs review","severity":"high","url":"https://example.invalid/o/r/pull/1"}`
	if string(raw) != want {
		t.Fatalf("got %s, want %s", raw, want)
	}

	raw, err = json.Marshal(AttentionItem{Type: "pr", ID: "o/r#1", Summary: "needs review"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw), "url") {
		t.Fatalf("empty URL must be omitted entirely, got %s", raw)
	}
}

// TestAttentionItem_UnmarshalV1Item proves the additive bump: a version-1
// item (no url or group key) still decodes, with URL empty and Group nil.
func TestAttentionItem_UnmarshalV1Item(t *testing.T) {
	var out AttentionItem
	if err := json.Unmarshal([]byte(`{"type":"pr","id":"pr-1","summary":"needs review","severity":"low"}`), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.URL != "" {
		t.Fatalf("URL = %q, want empty for a v1 item", out.URL)
	}
}

// TestAttentionSchemaVersion_BumpedForGroup pins the one-bump-per-additive-
// field convention: 2 added url, 3 adds group, and the version is
// registered in CurrentSchemaVersions so skew is detectable (INV-VER-1).
func TestAttentionSchemaVersion_BumpedForGroup(t *testing.T) {
	if AttentionSchemaVersion != 3 {
		t.Fatalf("AttentionSchemaVersion = %d, want 3 (the additive group field)", AttentionSchemaVersion)
	}
	got, ok := CurrentSchemaVersions["attention"]
	if !ok {
		t.Fatal(`CurrentSchemaVersions has no "attention" entry`)
	}
	if got != AttentionSchemaVersion {
		t.Fatalf("CurrentSchemaVersions[attention] = %d, want %d", got, AttentionSchemaVersion)
	}
}

// TestAttentionItem_JSONShape_Group pins INV-ATTN-GROUP-1's wire half: group
// is omitempty (an item with no group omits the key entirely, never
// "group":null or "group":{}) and a set group is emitted as {key,label},
// after url.
func TestAttentionItem_JSONShape_Group(t *testing.T) {
	raw, err := json.Marshal(AttentionItem{
		Type: "pr", ID: "o/r#1", Summary: "needs review", Severity: SeverityHigh,
		URL:   "https://example.invalid/o/r/pull/1",
		Group: &AttentionGroup{Key: "issue:ABC-1", Label: "ABC-1: fix the thing"},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `{"type":"pr","id":"o/r#1","summary":"needs review","severity":"high","url":"https://example.invalid/o/r/pull/1","group":{"key":"issue:ABC-1","label":"ABC-1: fix the thing"}}`
	if string(raw) != want {
		t.Fatalf("got %s, want %s", raw, want)
	}

	raw, err = json.Marshal(AttentionItem{Type: "pr", ID: "o/r#1", Summary: "needs review"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw), "group") {
		t.Fatalf("nil Group must be omitted entirely, got %s", raw)
	}
}

// TestAttentionItem_UnmarshalV2Item proves the additive bump from version 2:
// a version-2 item (url, no group key) still decodes, with Group left nil.
func TestAttentionItem_UnmarshalV2Item(t *testing.T) {
	var out AttentionItem
	if err := json.Unmarshal([]byte(`{"type":"pr","id":"pr-1","summary":"needs review","severity":"low","url":"https://example.invalid/1"}`), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Group != nil {
		t.Fatalf("Group = %+v, want nil for a v2 item", out.Group)
	}
	if out.URL == "" {
		t.Fatal("URL lost decoding a v2 item")
	}
}

// TestAttentionItem_V2ConsumerIgnoresGroup proves the other direction of
// additivity: a version-2 consumer, whose item shape has no group field,
// decodes a version-3 item without error and without being disturbed by the
// unknown key.
func TestAttentionItem_V2ConsumerIgnoresGroup(t *testing.T) {
	type attentionItemV2 struct {
		Type     string `json:"type"`
		ID       string `json:"id"`
		Summary  string `json:"summary"`
		Severity string `json:"severity,omitempty"`
		URL      string `json:"url,omitempty"`
	}
	raw, err := json.Marshal(AttentionItem{
		Type: "pr", ID: "o/r#1", Summary: "needs review", Severity: SeverityHigh,
		URL:   "https://example.invalid/o/r/pull/1",
		Group: &AttentionGroup{Key: "issue:ABC-1", Label: "ABC-1"},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var v2 attentionItemV2
	if err := json.Unmarshal(raw, &v2); err != nil {
		t.Fatalf("v2 consumer failed on a v3 item: %v", err)
	}
	if v2.Type != "pr" || v2.ID != "o/r#1" || v2.Severity != "high" || v2.URL != "https://example.invalid/o/r/pull/1" {
		t.Fatalf("v2 consumer misread a v3 item: %+v", v2)
	}
}

func TestAttentionItem_JSONRoundTrip(t *testing.T) {
	in := AttentionItem{Type: "ci", ID: "run-1", Summary: "build failed", Severity: SeverityCritical, URL: "https://example.invalid/run/1"}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out AttentionItem
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out != in {
		t.Fatalf("round-trip mismatch: got %+v, want %+v", out, in)
	}
}

func TestSeverity_IsValid(t *testing.T) {
	for _, s := range ValidSeverities {
		if !s.IsValid() {
			t.Errorf("%q should be valid", s)
		}
	}
	if Severity("bogus").IsValid() {
		t.Error(`"bogus" should not be valid`)
	}
	if Severity("").IsValid() {
		t.Error(`"" should not be valid`)
	}
}

func TestSeverity_Rank_CanonicalOrdering(t *testing.T) {
	// low < medium < high < critical — the exact ordering a consuming
	// aggregation algorithm relies on to sort/merge without re-deriving its
	// own ranking.
	if SeverityLow.Rank() >= SeverityMedium.Rank() {
		t.Fatalf("low.Rank()=%d should be < medium.Rank()=%d", SeverityLow.Rank(), SeverityMedium.Rank())
	}
	if SeverityMedium.Rank() >= SeverityHigh.Rank() {
		t.Fatalf("medium.Rank()=%d should be < high.Rank()=%d", SeverityMedium.Rank(), SeverityHigh.Rank())
	}
	if SeverityHigh.Rank() >= SeverityCritical.Rank() {
		t.Fatalf("high.Rank()=%d should be < critical.Rank()=%d", SeverityHigh.Rank(), SeverityCritical.Rank())
	}
}

func TestSeverity_Rank_InvalidIsNegative(t *testing.T) {
	if Severity("bogus").Rank() != -1 {
		t.Fatalf("Rank() of an invalid Severity = %d, want -1", Severity("bogus").Rank())
	}
	if Severity("").Rank() != -1 {
		t.Fatalf("Rank() of an empty Severity = %d, want -1", Severity("").Rank())
	}
}
