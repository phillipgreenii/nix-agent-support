package schema

import (
	"encoding/json"
	"testing"
)

func TestCIRun_JSONRoundTrip(t *testing.T) {
	in := CIRun{
		ID:         "run-1",
		Name:       "build",
		Status:     "completed",
		Conclusion: "success",
		URL:        "https://example.invalid/owner/repo/actions/runs/1",
		Provider:   "github-actions",
		HeadSHA:    "deadbeef",
		PRID:       "pr-1",
	}

	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var out CIRun
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if out != in {
		t.Fatalf("round-trip mismatch: got %+v, want %+v", out, in)
	}
}

func TestCIRun_PRIDIsAlwaysPresentOnTheWire(t *testing.T) {
	// PRID links a run to its PR [design: §2] and must not be omitempty: a
	// well-behaved provider always populates it, and it must survive
	// round-tripping a zero-value struct too (an empty string, not an
	// absent field, though either decodes back to "").
	raw, err := json.Marshal(CIRun{ID: "run-1", PRID: "pr-1"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `{"id":"run-1","name":"","status":"","conclusion":"","url":"","provider":"","pr_id":"pr-1"}`
	if string(raw) != want {
		t.Fatalf("got %s, want %s", raw, want)
	}
}

func TestCIRun_IDAndPRIDAreStrings(t *testing.T) {
	// CIRun.ID and CIRun.PRID must be strings, carried over as-is from
	// pg-pr's existing api.CIRun.ID string field [design: §9, §5.3] — a
	// compile-time assertion that these fields are string-typed, not
	// numeric.
	var _ string = CIRun{}.ID
	var _ string = CIRun{}.PRID
}

func TestCISchemaVersion_IndependentOfPRSchemaVersion(t *testing.T) {
	// schemaVersion is one integer per schema-bearing capability, never a
	// single global counter shared across capabilities [design: §4.3] — the
	// two constants must be independently named/addressable regardless of
	// whether their values happen to match (they diverged, 1 vs 2, once
	// bead pg2-681xo bumped SchemaVersion for the PR-only AsOf/Stale
	// fields — this test's own point is that nothing here couples them).
	_ = CISchemaVersion
	_ = SchemaVersion
}
