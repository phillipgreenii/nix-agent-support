package store

import (
	"reflect"
	"testing"
	"time"
)

func TestListEntitiesNoFactsMatchesListEntitiesWithoutFacts(t *testing.T) {
	st := OpenForTest(t)
	st.SeedLiveSizeForTest(t, 40, 200, 10)
	full, err := st.ListEntities()
	if err != nil {
		t.Fatal(err)
	}
	narrow, err := st.ListEntitiesNoFacts()
	if err != nil {
		t.Fatal(err)
	}
	for i := range full {
		full[i].Facts = ""
	}
	if !reflect.DeepEqual(full, narrow) {
		t.Fatalf("narrow rows differ from full rows with Facts blanked")
	}
}

func TestChangeKindCountsMatchesListChangesAfter(t *testing.T) {
	st := OpenForTest(t)
	st.SeedLiveSizeForTest(t, 40, 100, 300)
	for _, typ := range []string{"pr", "issue", "absent"} {
		recs, err := st.ListChangesAfter(typ, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		type key struct{ k, o string }
		want := map[key]int64{}
		var top int64
		for _, r := range recs {
			if r.Seq > top {
				top = r.Seq
			}
			for _, k := range r.Kinds {
				want[key{k, r.Origin}]++
			}
		}
		got, gotTop, err := st.ChangeKindCounts(typ)
		if err != nil {
			t.Fatal(err)
		}
		if gotTop != top {
			t.Fatalf("%s: max seq %d, want %d", typ, gotTop, top)
		}
		if len(got) != len(want) {
			t.Fatalf("%s: %d groups, want %d", typ, len(got), len(want))
		}
		for i, c := range got {
			if want[key{c.Kind, c.Origin}] != c.Count {
				t.Fatalf("%s: %+v, want %d", typ, c, want[key{c.Kind, c.Origin}])
			}
			if i > 0 && (got[i-1].Kind > c.Kind || (got[i-1].Kind == c.Kind && got[i-1].Origin >= c.Origin)) {
				t.Fatalf("%s: not sorted at %d", typ, i)
			}
		}
	}
}

func TestListHiddenWIPMatchesPRHiddenWIPAndIsConstantQueries(t *testing.T) {
	for _, n := range []int{10, 1000} {
		base := OpenForTest(t)
		base.SeedLiveSizeForTest(t, n, 10, 1)
		now := "2026-11-01T00:00:00Z"
		for _, id := range []string{"id-1", "id-2"} {
			if err := base.SetAnnotation(KVAnnotation{Repo: "o/r", EntityType: "issue", EntityID: id, Key: AnnotationWIP, Value: "true", Origin: "cli", SetBy: "me", SetAt: now}); err != nil {
				t.Fatal(err)
			}
		}
		st, cnt := base.WithCounterForTest(t)
		got, err := st.ListHiddenWIP()
		if err != nil {
			t.Fatal(err)
		}
		if q := cnt.Queries(); q > 3 {
			t.Fatalf("n=%d: %d queries, want a constant (<=3)", n, q)
		}
		for key, hw := range got {
			h, r, w, err := base.PRHiddenWIP(key.Repo, key.EntityType, key.EntityID)
			if err != nil {
				t.Fatal(err)
			}
			if h != hw.Hidden || r != hw.Reason || w != hw.WIP {
				t.Fatalf("%v: batch %+v, single %v %q %v", key, hw, h, r, w)
			}
		}
		if !got[HiddenWIPKey{"o/r", "issue", "id-1"}].WIP {
			t.Fatal("wip not read")
		}
	}
}

func TestLiveSizeNarrowReadsMoveNoFactsAndAreFast(t *testing.T) {
	base := OpenForTest(t)
	start := time.Now()
	base.SeedLiveSizeForTest(t, 2700, 37000, 40000)
	t.Logf("fixture built in %v", time.Since(start))
	st, cnt := base.WithCounterForTest(t)
	start = time.Now()
	if _, err := st.ListEntitiesNoFacts(); err != nil {
		t.Fatal(err)
	}
	for _, typ := range []string{"issue", "pr", "thread"} {
		if _, _, err := st.ChangeKindCounts(typ); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("narrow reads took %v, %d bytes", time.Since(start), cnt.Bytes())
	if cnt.Bytes() > 2<<20 {
		t.Fatalf("narrow reads moved %d bytes; facts must not be read", cnt.Bytes())
	}
}
