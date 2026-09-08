package schema

import (
	"encoding/json"
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

func TestAttentionItem_JSONRoundTrip(t *testing.T) {
	in := AttentionItem{Type: "ci", ID: "run-1", Summary: "build failed", Severity: SeverityCritical}
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
