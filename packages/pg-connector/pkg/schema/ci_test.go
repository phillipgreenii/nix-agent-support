package schema

import (
	"encoding/json"
	"reflect"
	"strings"
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
		Attempt:    2,
		AsOf:       "2026-09-09T00:00:00Z",
		Stale:      false,
		Jobs: []CIJob{
			{ID: "9", Name: "build-test-validate", Status: "completed", Conclusion: "failure", URL: "https://example.invalid/job/9"},
		},
	}

	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var out CIRun
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if !reflect.DeepEqual(out, in) {
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

func TestCIRun_AttemptOmittedWhenZero(t *testing.T) {
	raw, err := json.Marshal(CIRun{ID: "run-1", PRID: "pr-1"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := out["attempt"]; ok {
		t.Fatalf("attempt present in %s, want omitted when 0", raw)
	}
	raw, _ = json.Marshal(CIRun{ID: "run-1", Attempt: 3})
	if !strings.Contains(string(raw), `"attempt":3`) {
		t.Fatalf("attempt missing when set: %s", raw)
	}
}

// TestCISchemaVersion_IsCurrent pins CISchemaVersion at its current value
// so a bump is a deliberate, reviewed change.
func TestCISchemaVersion_IsCurrent(t *testing.T) {
	if CISchemaVersion != 5 {
		t.Fatalf("CISchemaVersion = %d, want 5", CISchemaVersion)
	}
}

func TestCIRun_JobsOmittedWhenNil(t *testing.T) {
	raw, err := json.Marshal(CIRun{ID: "run-1", PRID: "pr-1"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := out["jobs"]; ok {
		t.Fatalf("jobs present in %s, want omitted when nil", raw)
	}
}

func TestCIRun_JobsWireShape(t *testing.T) {
	raw, err := json.Marshal(CIRun{ID: "run-1", Jobs: []CIJob{
		{ID: "7", Name: "build-test-validate", Status: "completed", Conclusion: "failure", URL: "https://example.test/job/7"},
		{Name: "lint", Status: "completed", Conclusion: "success"},
	}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out struct {
		Jobs []map[string]any `json:"jobs"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out.Jobs) != 2 {
		t.Fatalf("jobs = %v, want 2 entries", out.Jobs)
	}
	if out.Jobs[0]["name"] != "build-test-validate" || out.Jobs[0]["conclusion"] != "failure" || out.Jobs[0]["id"] != "7" || out.Jobs[0]["url"] == nil {
		t.Fatalf("job[0] = %v", out.Jobs[0])
	}
	if _, ok := out.Jobs[1]["id"]; ok {
		t.Fatalf("job[1] id present, want omitted when empty: %v", out.Jobs[1])
	}
	if _, ok := out.Jobs[1]["url"]; ok {
		t.Fatalf("job[1] url present, want omitted when empty: %v", out.Jobs[1])
	}
}
