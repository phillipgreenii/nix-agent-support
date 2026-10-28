package changes

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/gather"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// This file pins the steady state of the issue entity type once both age
// sweep tiers are retired for it (INV-ICD-15, INV-CHANGES-12): nothing
// re-emits an issue that did not change. The tier functions themselves are
// tested in sweep_test.go and changes_test.go; Engine.Run reaches them for pr
// and thread only.

// steadyStateIDs are the ids of the baselined watched issues.
func steadyStateIDs(n int) []string {
	ids := make([]string, 0, n)
	for i := 0; i < n; i++ {
		ids = append(ids, fmt.Sprintf("bd-%02d", i))
	}
	return ids
}

// baselinedWatchedEngine holds ids as active, watched issues whose list_fp is
// what the fake lister returns, hydrated long before the engine clock (past
// sweep.max_age and reconcile_age, so a tier that ran for issue would select
// every one) and with NO change_log row.
func baselinedWatchedEngine(t *testing.T, ids []string) (*Engine, *store.Store, *storeHydrator, *time.Time) {
	t.Helper()
	e, st, h := newListEngine(t, "open")
	h.classified = true // the rows a hydration appends are the classifier's answer
	pairs := make([]string, 0, len(ids))
	watched := true
	for _, id := range ids {
		fp := "F-" + id
		if _, err := st.WriteEntityStateWithLogFP(store.Entity{Repo: "o/r", EntityType: "issue", EntityID: id, Facts: `{}`, AsOf: "x"},
			0, "2026-09-01T00:00:00Z", true, &fp, nil, OriginPGConnector, ""); err != nil {
			t.Fatal(err)
		}
		if err := st.SetEntityColumns("o/r", "issue", id, nil, &watched); err != nil {
			t.Fatal(err)
		}
		pairs = append(pairs, id+"="+fp)
	}
	e.Lister = fakeLister{res: listing(pairs...)}
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	e.Now = func() time.Time { return now }
	return e, st, h, &now
}

func changeLogOf(t *testing.T, st *store.Store) []store.ChangeRecord {
	t.Helper()
	recs, err := st.ListChangesAfter("issue", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	return recs
}

func maxSeq(recs []store.ChangeRecord) int64 {
	var m int64
	for _, r := range recs {
		if r.Seq > m {
			m = r.Seq
		}
	}
	return m
}

// assertNoTierRows fails on a change_log row of an issue that only an age
// sweep tier (or the retired reconcile kind) can write.
func assertNoTierRows(t *testing.T, recs []store.ChangeRecord) {
	t.Helper()
	for _, r := range recs {
		if r.Origin == OriginSweep || r.Origin == OriginLocalReconcile {
			t.Errorf("issue %s has a row of origin %q (seq %d)", r.EntityID, r.Origin, r.Seq)
		}
		for _, k := range r.Kinds {
			if k == "reconcile" {
				t.Errorf("issue %s has a reconcile row (seq %d, origin %q)", r.EntityID, r.Seq, r.Origin)
			}
		}
	}
}

// T-1: N watched issues, 100 polls at 5 minutes (100 x 5 = 500 minutes), the
// lister returning the fingerprints the store already holds: no change row, no
// hydration of a listed member, however old the entities are.
func TestSteadyStateIssuesAreNeverReEmittedByAge(t *testing.T) {
	const n = 40
	ids := steadyStateIDs(n)
	e, st, h, now := baselinedWatchedEngine(t, ids)
	before := changeLogOf(t, st)
	if len(before) != 0 {
		t.Fatalf("seeded change_log has %d rows, want none", len(before))
	}

	for poll := 0; poll < 100; poll++ {
		*now = now.Add(5 * time.Minute)
		_, out, err := runTick(t, e)
		if err != nil || out.Partial {
			t.Fatalf("poll %d = %+v, %v", poll, out, err)
		}
	}

	after := changeLogOf(t, st)
	if len(after) != len(before) || maxSeq(after) != maxSeq(before) {
		t.Errorf("change_log grew over 100 quiet polls: %d rows, max seq %d (was %d, %d)", len(after), maxSeq(after), len(before), maxSeq(before))
	}
	if len(h.calls) != 0 {
		t.Errorf("hydrated %d listed members, want zero: %v", len(h.calls), h.calls)
	}
	for _, id := range ids {
		row, found, err := st.GetEntity("o/r", "issue", id)
		if err != nil || !found || row.Version != 1 {
			t.Fatalf("%s = %+v found=%v err=%v, want the untouched seed version", id, row, found, err)
		}
	}
}

// T-4: after a mix of polls, a --reset, a refresh and a return after
// deactivation (and a day over a focus-annotated issue), no issue has a
// reconcile row nor a row of origin sweep or local-reconcile, and an
// annotation-free quiet day appends nothing.
func TestNoReconcileSweepOrLocalReconcileRowExistsForAnIssue(t *testing.T) {
	ids := steadyStateIDs(5)
	e, st, h, now := baselinedWatchedEngine(t, ids)
	ctx := context.Background()

	tick := func(opts ...Options) {
		t.Helper()
		*now = now.Add(5 * time.Minute)
		if _, out, err := runTick(t, e, opts...); err != nil || out.Partial {
			t.Fatalf("tick = %+v, %v", out, err)
		}
	}

	for range 3 {
		tick()
	}

	// A focus-annotated issue: the annotation row itself is a row; a day of
	// polls over it appends no more.
	if _, _, err := st.SetAnnotationSeq(store.KVAnnotation{
		Repo: "o/r", EntityType: "issue", EntityID: ids[0],
		Key: store.KeyDecider("focus", "pin"), Value: "1", Origin: "test", SetBy: "test", SetAt: now.Format(time.RFC3339),
	}); err != nil {
		t.Fatal(err)
	}
	afterAnnotation := maxSeq(changeLogOf(t, st))
	for range 24 * 12 {
		tick()
	}
	if got := maxSeq(changeLogOf(t, st)); got != afterAnnotation {
		t.Errorf("a day of polls appended rows over a focus-annotated issue: max seq %d, was %d", got, afterAnnotation)
	}

	// --reset replays only watched active issues, as added.
	tick(Options{EntityType: "issue", Consumer: "router", Reset: true})

	// A refresh.
	if err := e.hydrate(ctx, "issue", ids[1], gather.ChangeChanged, OriginPGConnector, false, nil); err != nil {
		t.Fatal(err)
	}

	// A return after deactivation: the row is inactive, the lister lists it
	// again, and the next poll hydrates it as a first observation.
	fp := "F-" + ids[2]
	row, _, err := st.GetEntity("o/r", "issue", ids[2])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.WriteEntityStateWithLogFP(row, row.Version, row.HydratedAt, false, &fp, []string{"removed"}, OriginPGConnector, now.Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	tick()
	tick()

	if len(h.calls) == 0 {
		t.Fatal("the reset, the refresh and the return hydrated nothing; the test proves nothing")
	}
	assertNoTierRows(t, changeLogOf(t, st))
}
