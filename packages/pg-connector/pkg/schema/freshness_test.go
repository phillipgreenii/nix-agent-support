// freshness_test.go: the cross-type AsOf/Stale acceptance test bead
// pg2-2j5ac.28.3's own acceptance criteria require ("a schema test asserts
// AsOf/Stale on every entity type in pkg/schema"). Each of PR (bead
// pg2-681xo), CIRun (bead pg2-4aoeg), and Issue (bead pg2-2j5ac.28.3) has
// long carried its OWN individual "AsOf/Stale always present" test
// (pr_test.go, ci_test.go, issue_test.go) — this file is the first to
// assert the invariant ACROSS all three together via reflection, closing
// the design's cross-type acceptance criterion now that Issue's own pair
// has landed.
//
// SearchResult/AttentionItem/WorktreeInfo/BranchInfo are deliberately
// EXCLUDED from this reflection walk: none of them carries an AsOf/Stale
// pair today (none of their current backends caches remote fact data
// either — the same "no live-called-vs-cached distinction yet to expose"
// reasoning schema.PR.Stale's own doc comment gives), so "every entity
// type" here means every entity type that has actually adopted the
// AsOf/Stale convention, not every exported struct in this package.
package schema

import (
	"encoding/json"
	"reflect"
	"testing"
)

// freshnessBearingEntities lists the schema package's entity types that
// carry the AsOf/Stale pair. Extend this list (never a parallel list
// elsewhere) when a future entity type adopts the pair, so this one test
// stays the single place the cross-type invariant is enforced.
var freshnessBearingEntities = []any{
	PR{},
	CIRun{},
	Issue{},
}

func TestFreshnessBearingEntities_AllCarryAsOfAndStale(t *testing.T) {
	for _, entity := range freshnessBearingEntities {
		typ := reflect.TypeOf(entity)
		t.Run(typ.Name(), func(t *testing.T) {
			asOfField, ok := typ.FieldByName("AsOf")
			if !ok {
				t.Fatalf("%s has no AsOf field", typ.Name())
			}
			if asOfField.Type.Kind() != reflect.String {
				t.Fatalf("%s.AsOf must be a string, got %s", typ.Name(), asOfField.Type)
			}
			if asOfField.Tag.Get("json") != "as_of" {
				t.Fatalf(`%s.AsOf json tag = %q, want "as_of" (non-omitempty)`, typ.Name(), asOfField.Tag.Get("json"))
			}

			staleField, ok := typ.FieldByName("Stale")
			if !ok {
				t.Fatalf("%s has no Stale field", typ.Name())
			}
			if staleField.Type.Kind() != reflect.Bool {
				t.Fatalf("%s.Stale must be a bool, got %s", typ.Name(), staleField.Type)
			}
			if staleField.Tag.Get("json") != "stale" {
				t.Fatalf(`%s.Stale json tag = %q, want "stale" (non-omitempty)`, typ.Name(), staleField.Tag.Get("json"))
			}

			// Belt-and-suspenders: a zero-value instance must actually
			// marshal both keys onto the wire, not merely declare the
			// struct fields (a stray omitempty would pass the tag check
			// above but silently drop stale:false from the wire).
			zero := reflect.New(typ).Elem().Interface()
			raw, err := json.Marshal(zero)
			if err != nil {
				t.Fatalf("marshal zero-value %s: %v", typ.Name(), err)
			}
			var decoded map[string]any
			if err := json.Unmarshal(raw, &decoded); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if _, ok := decoded["as_of"]; !ok {
				t.Fatalf("%s zero-value JSON missing as_of: %s", typ.Name(), raw)
			}
			if v, ok := decoded["stale"]; !ok || v != false {
				t.Fatalf("%s zero-value JSON stale = %v (present=%v), want false present", typ.Name(), v, ok)
			}
		})
	}
}
