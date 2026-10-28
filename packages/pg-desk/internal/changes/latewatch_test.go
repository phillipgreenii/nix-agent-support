package changes

import (
	"errors"
	"reflect"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// seedKnown holds an active entity the way an on-demand hydration leaves it:
// a fresh hydrated_at and change row (so no age tier is due), watched NULL, the
// row's origin seed.
func seedKnown(t *testing.T, st *store.Store, id, listFP string) {
	t.Helper()
	fp := listFP
	if _, err := st.WriteEntityStateWithLogFP(store.Entity{Repo: "o/r", EntityType: "issue", EntityID: id, Facts: `{}`, AsOf: "x"},
		0, "2026-10-01T11:59:00Z", true, &fp, []string{"reconcile"}, "seed", "2026-10-01T11:59:00Z"); err != nil {
		t.Fatal(err)
	}
}

// fresh drops the seed rows from an envelope's records.
func fresh(recs []Record) []Record {
	var out []Record
	for _, r := range recs {
		if r.Origin != "seed" {
			out = append(out, r)
		}
	}
	return out
}

// kindsOfRows is the kinds of every row in the history, newest first.
func kindsOfRows(t *testing.T, st *store.Store, id string) [][]string {
	t.Helper()
	var out [][]string
	for _, r := range historyOf(t, st, id) {
		out = append(out, r.Kinds)
	}
	return out
}

func countAdded(t *testing.T, st *store.Store, id string) int {
	t.Helper()
	n := 0
	for _, r := range historyOf(t, st, id) {
		for _, k := range r.Kinds {
			if k == "added" {
				n++
			}
		}
	}
	return n
}

// An entity first seen in a query that already has its marker appends exactly
// one added row, and the entity's watched column is true after that write.
func TestFirstObservationAfterBaselineAppendsOneAddedAndMarksWatched(t *testing.T) {
	e, st, h := newListEngine(t, "open")
	e.Lister = fakeLister{res: listing("bd-1=F1")}
	env, _, err := runTick(t, e)
	if err != nil {
		t.Fatal(err)
	}
	if len(fresh(env.Records)) != 1 || fresh(env.Records)[0].ID != "bd-1" || !reflect.DeepEqual(fresh(env.Records)[0].Kinds, []string{"added"}) {
		t.Fatalf("records = %+v, want exactly one added for bd-1", fresh(env.Records))
	}
	if w, found := watchedOf(t, st, "bd-1"); !found || !w {
		t.Errorf("found=%v watched=%v, want held and watched after the first write", found, w)
	}

	// A second poll of the unchanged listing hydrates nothing and appends nothing.
	h.calls = nil
	env, _, err = runTick(t, e)
	if err != nil || len(fresh(env.Records)) != 0 || len(h.calls) != 0 {
		t.Fatalf("second poll: records=%+v hydrations=%v err=%v", fresh(env.Records), h.calls, err)
	}
	if n := countAdded(t, st, "bd-1"); n != 1 {
		t.Errorf("added rows = %d, want exactly 1", n)
	}
}

// A known, active entity of unchanged list_fp and NULL watched that a baselined
// query lists is selected, hydrated and appends exactly one added; a repeat
// appends no second added.
func TestLateWatchSelectsKnownUnwatchedEntityOnce(t *testing.T) {
	e, st, h := newListEngine(t, "open")
	seedKnown(t, st, "bd-1", "F1")
	if w, _ := watchedOf(t, st, "bd-1"); w {
		t.Fatal("fixture bd-1 is already watched")
	}
	e.Lister = fakeLister{res: listing("bd-1=F1")}

	env, _, err := runTick(t, e)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(h.calls, []string{"bd-1"}) {
		t.Fatalf("hydrations = %v, want the late watch of bd-1", h.calls)
	}
	if len(fresh(env.Records)) != 1 || !reflect.DeepEqual(fresh(env.Records)[0].Kinds, []string{"added"}) {
		t.Fatalf("records = %+v, want exactly one added", fresh(env.Records))
	}
	if w, _ := watchedOf(t, st, "bd-1"); !w {
		t.Error("watched is not true after the late watch")
	}

	// Once watched, the unchanged entity is never selected again.
	h.calls = nil
	for i := 0; i < 2; i++ {
		env, _, err = runTick(t, e)
		if err != nil || len(fresh(env.Records)) != 0 {
			t.Fatalf("repeat poll %d: records=%+v err=%v", i, fresh(env.Records), err)
		}
	}
	if len(h.calls) != 0 {
		t.Errorf("a watched, unchanged entity was hydrated again: %v", h.calls)
	}
	if n := countAdded(t, st, "bd-1"); n != 1 {
		t.Errorf("added rows = %d, want exactly 1", n)
	}
}

// A late watch the hydration budget cuts writes nothing, leaves watched NULL
// and is selected again at the next poll; the order is changed, added, then
// late watch.
func TestLateWatchCutByBudgetLeavesWatchedNullAndIsRetried(t *testing.T) {
	e, st, h := newListEngine(t, "open")
	one := 1
	e.Cfg.Hydration.MaxPerPoll = &one
	seedKnown(t, st, "bd-1", "F1")
	e.Lister = fakeLister{res: listing("bd-1=F1", "bd-2=F1")}

	// Tick 1: the added entity bd-2 uses the one hydration; the late watch waits.
	env, _, err := runTick(t, e)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(h.calls, []string{"bd-2"}) {
		t.Fatalf("tick 1 hydrations = %v, want only the added bd-2 (added before late watch)", h.calls)
	}
	if len(fresh(env.Records)) != 1 || fresh(env.Records)[0].ID != "bd-2" {
		t.Fatalf("tick 1 records = %+v", fresh(env.Records))
	}
	row, _, _ := st.GetEntity("o/r", "issue", "bd-1")
	if row.Watched != nil || row.Version != 1 {
		t.Fatalf("a cut late watch wrote: watched=%v version=%d", row.Watched, row.Version)
	}

	// Tick 2: selected again and hydrated.
	h.calls = nil
	env, _, err = runTick(t, e)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(h.calls, []string{"bd-1"}) || len(fresh(env.Records)) != 1 || fresh(env.Records)[0].ID != "bd-1" {
		t.Fatalf("tick 2: hydrations=%v records=%+v, want the retried late watch", h.calls, fresh(env.Records))
	}
	if w, _ := watchedOf(t, st, "bd-1"); !w {
		t.Error("bd-1 is not watched after the retry")
	}
}

// A late watch that fails to hydrate leaves watched NULL and is retried.
func TestLateWatchFailedHydrationLeavesWatchedNull(t *testing.T) {
	e, st, h := newListEngine(t, "open")
	seedKnown(t, st, "bd-1", "F1")
	e.Lister = fakeLister{res: listing("bd-1=F1")}
	h.fail["bd-1"] = errors.New("boom")
	for tick := 0; tick < 2; tick++ {
		before := len(h.calls)
		if _, _, err := runTick(t, e); err != nil {
			t.Fatal(err)
		}
		if len(h.calls) != before+1 {
			t.Fatalf("tick %d: hydrations = %v, want a retry every poll", tick, h.calls)
		}
	}
	if w, _ := watchedOf(t, st, "bd-1"); w {
		t.Error("a failed late watch set the mark")
	}
}

// The selection applies only to an entity listed by a baselined query: one
// listed only by a query still in a baseline run is left to the baseline.
func TestLateWatchNotSelectedOutsideABaselinedQuery(t *testing.T) {
	e, st, _ := newUnbaselinedEngine(t, "open", "done")
	if err := st.SetMeta(MetaBaselinePrefix+"done", "2026-09-30T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	seedKnown(t, st, "bd-1", "F1")
	seedKnown(t, st, "bd-2", "F1")

	got, err := e.diffListed("issue", []queryRun{
		{query: "open", res: listing("bd-1=F1")},
		{query: "done", res: listing("bd-2=F1")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"bd-2"}; !reflect.DeepEqual(idsOf(got), want) {
		t.Errorf("selected = %v, want %v (bd-1 is listed only by a query in a baseline run)", idsOf(got), want)
	}
	if len(got) == 1 && !got[0].lateWatch {
		t.Error("bd-2 is not marked as a late watch")
	}
}

// A held entity that is not watched is hydrated on demand only: no query lists
// it, so it is neither diffed nor emitted.
func TestLateWatchUnwatchedHeldEntityIsNeitherDiffedNorEmitted(t *testing.T) {
	e, st, h := newListEngine(t, "open")
	seedKnown(t, st, "bd-9", "F1")
	e.Lister = fakeLister{res: listing("bd-1=F1")}
	base := len(historyOf(t, st, "bd-9"))
	got, err := e.diffListed("issue", []queryRun{{query: "open", res: listing("bd-1=F1")}})
	if err != nil || !reflect.DeepEqual(idsOf(got), []string{"bd-1"}) {
		t.Errorf("diffListed = %v, %v; want only the listed bd-1", idsOf(got), err)
	}

	env, _, err := runTick(t, e)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range fresh(env.Records) {
		if r.ID == "bd-9" {
			t.Errorf("an unwatched held entity was emitted: %+v", r)
		}
	}
	for _, c := range h.calls {
		if c == "bd-9" {
			t.Error("an unwatched held entity was hydrated")
		}
	}
	if got := len(historyOf(t, st, "bd-9")); got != base {
		t.Errorf("history grew from %d to %d", base, got)
	}
}

// An observed close appends closed first, then pg-desk's deactivation clears
// the watched flag (written false, never NULL) in the deactivation transaction.
func TestLateWatchObservedCloseAppendsClosedThenClearsTheFlag(t *testing.T) {
	e, st, h := newListEngine(t, "open")
	e.Lister = fakeLister{res: listing("bd-1=F1")}
	if _, _, err := runTick(t, e); err != nil {
		t.Fatal(err)
	}
	if w, _ := watchedOf(t, st, "bd-1"); !w {
		t.Fatal("bd-1 is not watched after its first observation")
	}

	h.facts = map[string]string{"bd-1": `{"issue_show":{"id":"bd-1","state":"closed","title":"t"}}`}
	h.kindsOf = map[string][]string{"bd-1": {"closed"}}
	e.Lister = fakeLister{res: listing("bd-1=F2")}
	env, _, err := runTick(t, e)
	if err != nil {
		t.Fatal(err)
	}
	if len(fresh(env.Records)) != 1 || !reflect.DeepEqual(fresh(env.Records)[0].Kinds, []string{"closed"}) {
		t.Fatalf("records = %+v, want the closed row delivered", fresh(env.Records))
	}
	row, found, err := st.GetEntity("o/r", "issue", "bd-1")
	if err != nil || !found {
		t.Fatalf("entity: found=%v err=%v", found, err)
	}
	if !row.Inactive {
		t.Error("the closed entity is still active")
	}
	if row.Watched == nil || *row.Watched {
		t.Errorf("watched = %v, want a stated false after the deactivation", row.Watched)
	}
	if got := kindsOfRows(t, st, "bd-1"); !reflect.DeepEqual(got, [][]string{{"closed"}, {"added"}}) {
		t.Errorf("history (newest first) = %v, want closed after added", got)
	}
}

// A removal appends its removed row before the flag is cleared.
func TestDeactivateClearsTheFlagAfterTheRemovedRow(t *testing.T) {
	e, st, _ := newListEngine(t, "open")
	seedKnown(t, st, "bd-1", "F1")
	yes := true
	if err := st.SetEntityColumns("o/r", "issue", "bd-1", nil, &yes); err != nil {
		t.Fatal(err)
	}
	if err := e.deactivate("issue", "bd-1", []string{"removed"}); err != nil {
		t.Fatal(err)
	}
	row, _, _ := st.GetEntity("o/r", "issue", "bd-1")
	if !row.Inactive || row.Watched == nil || *row.Watched {
		t.Errorf("inactive=%v watched=%v, want inactive and a stated false", row.Inactive, row.Watched)
	}
	if got := kindsOfRows(t, st, "bd-1"); len(got) == 0 || !reflect.DeepEqual(got[0], []string{"removed"}) {
		t.Errorf("history = %v, want the removed row appended", got)
	}
}

// The flag of an entity an owning backend holds belongs to the feed path:
// deactivation does not clear it.
func TestDeactivateLeavesTheFlagOfAnOwningBackendEntity(t *testing.T) {
	e, st, _ := newListEngine(t, "open")
	e.Cfg.Watch.Issue.Backends = map[string]config.WatchBackend{"jira-q": {Instance: "jira-a", OwnsChanges: true}}
	e.Cfg.Watch.Issue.Queries = append(e.Cfg.Watch.Issue.Queries, "jira-q")
	seedKnown(t, st, "PROJ-1", "F1")
	yes := true
	backend := "jira-a"
	if err := st.SetEntityColumns("o/r", "issue", "PROJ-1", &backend, &yes); err != nil {
		t.Fatal(err)
	}
	if err := e.deactivate("issue", "PROJ-1", nil); err != nil {
		t.Fatal(err)
	}
	row, _, _ := st.GetEntity("o/r", "issue", "PROJ-1")
	if !row.Inactive || row.Watched == nil || !*row.Watched {
		t.Errorf("inactive=%v watched=%v, want inactive with the feed path's flag untouched", row.Inactive, row.Watched)
	}
}
