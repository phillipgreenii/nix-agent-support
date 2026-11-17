package store

import (
	"context"
	"strings"
	"testing"
)

// The readers that only need a PR's open/closed state read the pruned
// focus_facts column through the covering index, never the facts overflow
// pages: on a store whose PR facts are large, FinishedPRIDs reads a small
// fraction of the facts bytes and answers the same set it gets from the full
// facts. After the index is dropped (a store that predates it) it falls back to
// the full facts and answers the same set again.
func TestFinishedPRIDsReadsThePrunedProjectionAndMatchesTheFullFacts(t *testing.T) {
	base := OpenForTest(t)
	pad := `"body":"` + strings.Repeat("x", 30000) + `"`
	seed := map[string]string{
		"o/r#1": `{"pr_show":{"state":"open",` + pad + `}}`,
		"o/r#2": `{"pr_show":{"state":"closed",` + pad + `}}`,
		"o/r#3": `{"pr_show":{"state":"open","merged":true,` + pad + `}}`,
		"o/r#4": `{"pr_show":{"state":"merged",` + pad + `}}`,
		"o/r#5": `{}`,
		"o/r#6": `not json`,
		"o/r#7": `{"pr_show":{"state":"open",` + pad + `}}`, // made inactive below
	}
	for id, facts := range seed {
		if err := base.UpsertEntity(Entity{Repo: "o/r", EntityType: "pr", EntityID: id, Facts: facts, AsOf: "2026-11-01T00:00:00Z", ContentHash: "h" + id}); err != nil {
			t.Fatal(err)
		}
	}
	base.ExecForTest(t, `UPDATE entity SET active = 0 WHERE entity_id = 'o/r#7'`)
	want := map[string]bool{"o/r#2": true, "o/r#3": true, "o/r#4": true, "o/r#7": true}

	st, cnt := base.WithCounterForTest(t)
	b0 := cnt.Bytes()
	got, err := st.FinishedPRIDs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	pruned := cnt.Bytes() - b0
	assertSet(t, "pruned", got, want)
	// Five large rows hold about 150 KB of facts; the pruned read is a few KB.
	if pruned > 40_000 {
		t.Fatalf("FinishedPRIDs read %d bytes; the facts overflow pages must not be read", pruned)
	}

	base.ExecForTest(t, `DROP INDEX entity_focus`)
	b1 := cnt.Bytes()
	got, err = st.FinishedPRIDs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	assertSet(t, "fallback", got, want)
	if full := cnt.Bytes() - b1; full <= pruned {
		t.Fatalf("fallback read %d bytes, not more than the pruned read (%d): the test would not tell the two paths apart", full, pruned)
	}
}

func assertSet(t *testing.T, label string, got, want map[string]bool) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: got %v, want %v", label, got, want)
	}
	for k := range want {
		if !got[k] {
			t.Fatalf("%s: got %v, want %v", label, got, want)
		}
	}
}
