package store

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestListEntityFlowMatchesListEntitiesFlowColumns(t *testing.T) {
	st := OpenForTest(t)
	st.SeedLiveSizeForTest(t, 40, 200, 10)
	full, err := st.ListEntities()
	if err != nil {
		t.Fatal(err)
	}
	flow, err := st.ListEntityFlow()
	if err != nil {
		t.Fatal(err)
	}
	if len(flow) != len(full) {
		t.Fatalf("%d flow rows, %d entities", len(flow), len(full))
	}
	byID := map[string]Entity{}
	for _, e := range full {
		byID[e.Repo+"|"+e.EntityType+"|"+e.EntityID] = e
	}
	for _, f := range flow {
		e := byID[f.Repo+"|"+f.EntityType+"|"+f.EntityID]
		keep := Entity{
			Repo: e.Repo, EntityType: e.EntityType, EntityID: e.EntityID, HydratedAt: e.HydratedAt, Inactive: e.Inactive,
			FirstSeenAt: e.FirstSeenAt, Backend: e.Backend, Watched: e.Watched, ReadAttemptAt: e.ReadAttemptAt, ReadOKAt: e.ReadOKAt,
		}
		if !reflect.DeepEqual(f, keep) {
			t.Fatalf("flow row %+v, want %+v", f, keep)
		}
	}
}

func TestListEntityFlowIsServedFromTheCoveringIndex(t *testing.T) {
	st := OpenForTest(t)
	rows, err := st.sql.QueryContext(context.Background(), `EXPLAIN QUERY PLAN SELECT repo, entity_type, entity_id, hydrated_at, active, first_seen_at, backend, watched, read_attempt_at, read_ok_at FROM entity ORDER BY entity_type, entity_id, repo`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var plan string
	for rows.Next() {
		var a, b, c int
		var d string
		if err := rows.Scan(&a, &b, &c, &d); err != nil {
			t.Fatal(err)
		}
		plan += d + "\n"
	}
	if !strings.Contains(plan, "COVERING INDEX entity_flow") {
		t.Fatalf("plan does not use the covering index:\n%s", plan)
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
		all, err := st.ChangeKindCountsAll()
		if err != nil {
			t.Fatal(err)
		}
		got, gotTop := all[typ].Counts, all[typ].MaxSeq
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
	if _, err := st.ListEntityFlow(); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ChangeKindCountsAll(); err != nil {
		t.Fatal(err)
	}
	t.Logf("narrow reads took %v, %d bytes", time.Since(start), cnt.Bytes())
	if cnt.Bytes() > 2<<20 {
		t.Fatalf("narrow reads moved %d bytes; facts must not be read", cnt.Bytes())
	}
}
