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
		Repo:       "owner/repo",
		PRID:       "pr-1",
		AsOf:       "2026-09-09T00:00:00Z",
		Stale:      false,
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
	// PRID links a run to its PR (interfaces.md's op catalog) and must not be omitempty: a
	// well-behaved provider always populates it, and it must survive
	// round-tripping a zero-value struct too (an empty string, not an
	// absent field, though either decodes back to "").
	raw, err := json.Marshal(CIRun{ID: "run-1", PRID: "pr-1"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `{"id":"run-1","name":"","status":"","conclusion":"","url":"","provider":"","repo":"","pr_id":"pr-1","as_of":"","stale":false}`
	if string(raw) != want {
		t.Fatalf("got %s, want %s", raw, want)
	}
}

func TestCIRun_AsOfAndStale_AlwaysPresentInJSON(t *testing.T) {
	// AsOf/Stale (bead pg2-4aoeg, mirroring schema.PR's own pg2-681xo pair)
	// are not omitempty — Stale in particular must always be present, since
	// false is itself informative, and a consumer must be able to
	// distinguish "explicitly not stale" from "field absent."
	raw, err := json.Marshal(CIRun{ID: "run-1", PRID: "pr-1", AsOf: "2026-09-09T00:00:00Z", Stale: false})
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

func TestCIRun_IDAndPRIDAreStrings(t *testing.T) {
	// CIRun.ID and CIRun.PRID must be strings, carried over as-is from
	// pg-pr's existing api.CIRun.ID string field — a
	// compile-time assertion that these fields are string-typed, not
	// numeric.
	var _ string = CIRun{}.ID   //nolint:staticcheck // QF1011: explicit type IS the assertion; omitting it would infer from the field and defeat the check.
	var _ string = CIRun{}.PRID //nolint:staticcheck // QF1011: same as above.
}

func TestCISchemaVersion_IndependentOfPRSchemaVersion(t *testing.T) {
	// schemaVersion is one integer per schema-bearing capability, never a
	// single global counter shared across capabilities (INV-VER-1) — the
	// two constants must be independently named/addressable regardless of
	// whether their values happen to match (they diverged, 1 vs 2, once
	// bead pg2-681xo bumped PRSchemaVersion for the PR-only AsOf/Stale
	// fields — this test's own point is that nothing here couples them).
	_ = CISchemaVersion
	_ = PRSchemaVersion
}
