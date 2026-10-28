package changes

import (
	"context"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/gather"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/pipeline"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// These tests pin the detector for a dependant left stale by a blocker's
// closure: when a poll's hydration of an issue appends closed (a listed member
// or an orphan read), its watched, active, non-terminal direct blocks
// dependants are re-hydrated in the same poll, from what is left of
// hydration.max_per_poll.

const (
	// blockerClosedFacts is the stored facts of a blocker found closed; it
	// carries issue_deps, so its reverse-edge index is available.
	blockerClosedFacts = `{"issue_show":{"id":"B","state":"closed","title":"t"},"issue_deps":{"ids":[]}}`
	// depOpenFacts is a dependant blocked by B (B is the blocker) while open.
	depOpenFacts = `{"issue_show":{"state":"open","title":"t","deps":[{"id":"B","type":"blocks"}]}}`
	// depMovedFacts is the same dependant after its blocker closed.
	depMovedFacts = `{"issue_show":{"state":"open","title":"t2","deps":[{"id":"B","type":"blocks"}]}}`
)

// diffHydrator is the hydrator of these tests: it honours the pipeline's
// no-write answer for an equal snapshot (the normal classifier appends nothing
// then), returns the kinds it wrote, and writes `facts` per id.
type diffHydrator struct {
	*storeHydrator
}

func (h *diffHydrator) RunEntityChange(_ context.Context, entityType, id string, change gather.ChangeKind, opts pipeline.EntityChangeOptions) (pipeline.EntityChangeResult, error) {
	h.calls = append(h.calls, id)
	h.listFPs[id] = opts.ListFP
	h.changes[id] = change
	h.origins[id] = opts.Origin
	if err := h.fail[id]; err != nil {
		return pipeline.EntityChangeResult{}, err
	}
	row, found, err := h.st.GetEntity("o/r", entityType, id)
	if err != nil {
		return pipeline.EntityChangeResult{}, err
	}
	facts := `{}`
	if f, ok := h.facts[id]; ok {
		facts = f
	}
	if found && !row.Inactive && row.Facts == facts {
		return pipeline.EntityChangeResult{Unchanged: true}, nil
	}
	var expected int64
	if found {
		expected = row.Version
	}
	kinds := []string{"deps_changed"}
	if k, ok := h.kindsOf[id]; ok {
		kinds = k
	}
	v, err := h.st.WriteEntityStateWithLogOpts(store.Entity{Repo: "o/r", EntityType: entityType, EntityID: id, Facts: facts, AsOf: "x"},
		expected, "2026-10-01T11:59:00Z", true, opts.ListFP, kinds, opts.Origin, "2026-10-01T12:00:00Z", store.WriteOptions{})
	return pipeline.EntityChangeResult{Written: err == nil, Version: v, Kinds: kinds}, err
}

// seedDep stores a listed-and-unchanged dependant (list_fp F1) with the facts,
// watched or not, active or not.
func seedDep(t *testing.T, st *store.Store, id, facts string, watched, active bool) {
	t.Helper()
	fp := "F1"
	if _, err := st.WriteEntityStateWithLogFP(store.Entity{Repo: "o/r", EntityType: "issue", EntityID: id, Facts: facts, AsOf: "x"},
		0, "2026-10-01T11:00:00Z", active, &fp, []string{"reconcile"}, "seed", "2026-09-29T10:00:00Z"); err != nil {
		t.Fatal(err)
	}
	w := watched
	if err := st.SetEntityColumns("o/r", "issue", id, nil, &w); err != nil {
		t.Fatal(err)
	}
}

// depsEngine is a baselined engine over the query "open" whose persisted set
// holds ids (so none is an orphan) and whose hydrator is the diffHydrator.
func depsEngine(t *testing.T, ids ...string) (*Engine, *store.Store, *diffHydrator) {
	t.Helper()
	e, st, _ := newListEngine(t, "open")
	h := &diffHydrator{newStoreHydrator(st)}
	h.facts = map[string]string{}
	h.kindsOf = map[string][]string{}
	e.Hydrator = h
	e.Store.SetGateScope(store.AllIssueGateScope())
	set := map[string]bool{}
	for _, id := range ids {
		set[id] = true
	}
	if err := saveWatchSet(st, "issue", "open", set); err != nil {
		t.Fatal(err)
	}
	return e, st, h
}

func sortedCalls(h *diffHydrator) []string {
	out := append([]string(nil), h.calls...)
	sort.Strings(out)
	return out
}

func rowCount(t *testing.T, st *store.Store, id string) int { return len(changeRows(t, st, id)) }

// Blocker B is a listed member whose hydration appends closed: its watched
// dependants D1 and D2 are re-hydrated in the same poll; D1, whose payload now
// differs, appends one row, and D2, unchanged, appends none.
func TestListedBlockerClosingRehydratesItsWatchedDependants(t *testing.T) {
	e, st, h := depsEngine(t, "B", "D1", "D2")
	seedRow(t, st, "B", "F1", "2026-10-01T11:00:00Z", true)
	yes := true
	if err := st.SetEntityColumns("o/r", "issue", "B", nil, &yes); err != nil {
		t.Fatal(err)
	}
	seedDep(t, st, "D1", depOpenFacts, true, true)
	seedDep(t, st, "D2", depOpenFacts, true, true)
	h.facts["B"] = blockerClosedFacts
	h.kindsOf["B"] = []string{"closed"}
	h.facts["D1"] = depMovedFacts
	h.facts["D2"] = depOpenFacts
	e.Lister = fakeLister{res: listing("B=F2", "D1=F1", "D2=F1")}
	d1, d2 := rowCount(t, st, "D1"), rowCount(t, st, "D2")

	if _, out, err := runTick(t, e); err != nil || out.Partial {
		t.Fatalf("poll = %+v, %v", out, err)
	}
	if got := sortedCalls(h); !reflect.DeepEqual(got, []string{"B", "D1", "D2"}) {
		t.Fatalf("hydrations = %v, want B then its two dependants", got)
	}
	if got := rowCount(t, st, "D1"); got != d1+1 {
		t.Errorf("D1 rows = %d, want one new row (%d)", got, d1+1)
	}
	if got := rowCount(t, st, "D2"); got != d2 {
		t.Errorf("D2 rows = %d, want none appended (%d)", got, d2)
	}
	if h.changes["D1"] != gather.ChangeChanged || h.listFPs["D1"] != nil || h.origins["D1"] != OriginPGConnector {
		t.Errorf("dependant read shape: change=%q listFP=%v origin=%q", h.changes["D1"], h.listFPs["D1"], h.origins["D1"])
	}
	if row := entityOf(t, st, "D1"); row.ListFP != "F1" {
		t.Errorf("D1 list_fp = %q, want it untouched", row.ListFP)
	}
}

// The blocker is an ORPHAN read that finds it closed (a closed bead leaves the
// open-status queries): its dependants are re-hydrated in the same poll.
func TestOrphanReadFindingBlockerClosedRehydratesItsDependants(t *testing.T) {
	e, st, h := depsEngine(t, "D1", "D2") // B is in no query's set: an orphan
	seedOrphan(t, st, "B")
	seedDep(t, st, "D1", depOpenFacts, true, true)
	seedDep(t, st, "D2", depOpenFacts, true, true)
	h.facts["B"] = blockerClosedFacts
	h.kindsOf["B"] = []string{"closed"}
	h.facts["D1"] = depMovedFacts
	h.facts["D2"] = depOpenFacts
	e.Lister = fakeLister{res: listing("D1=F1", "D2=F1")}
	d1, d2 := rowCount(t, st, "D1"), rowCount(t, st, "D2")

	if _, _, err := runTick(t, e); err != nil {
		t.Fatal(err)
	}
	if got := sortedCalls(h); !reflect.DeepEqual(got, []string{"B", "D1", "D2"}) {
		t.Fatalf("hydrations = %v, want the orphan read then both dependants", got)
	}
	if !entityOf(t, st, "B").Inactive {
		t.Error("the closed orphan stayed active")
	}
	if got := rowCount(t, st, "D1"); got != d1+1 {
		t.Errorf("D1 rows = %d, want one new row (%d)", got, d1+1)
	}
	if got := rowCount(t, st, "D2"); got != d2 {
		t.Errorf("D2 rows = %d, want none appended (%d)", got, d2)
	}
}

// An inactive, a terminal and an unwatched dependant are not re-hydrated; the
// watched, active, open one is.
func TestInactiveTerminalAndUnwatchedDependantsAreNotRehydrated(t *testing.T) {
	e, st, h := depsEngine(t, "B", "Dok", "Dterm", "Dunw")
	seedRow(t, st, "B", "F1", "2026-10-01T11:00:00Z", true)
	yes := true
	if err := st.SetEntityColumns("o/r", "issue", "B", nil, &yes); err != nil {
		t.Fatal(err)
	}
	seedDep(t, st, "Dok", depOpenFacts, true, true)
	seedDep(t, st, "Dina", depOpenFacts, true, false) // inactive
	seedDep(t, st, "Dterm", `{"issue_show":{"state":"closed","deps":[{"id":"B","type":"blocks"}]}}`, true, true)
	seedDep(t, st, "Dunw", depOpenFacts, false, true) // watched stated false
	h.facts["B"] = blockerClosedFacts
	h.kindsOf["B"] = []string{"closed"}
	h.facts["Dok"] = depMovedFacts
	e.Lister = fakeLister{res: listing("B=F2", "Dok=F1", "Dterm=F1", "Dunw=F1")}

	if _, _, err := runTick(t, e); err != nil {
		t.Fatal(err)
	}
	if got := sortedCalls(h); !reflect.DeepEqual(got, []string{"B", "Dok"}) {
		t.Errorf("hydrations = %v, want only the blocker and the watched, active, open dependant", got)
	}
}

// With hydration.max_per_poll 2, a blocker's own hydration uses one unit and
// exactly the remainder (one) of three dependants is re-hydrated.
func TestDependantsDrawOnTheBudgetRemainderOnly(t *testing.T) {
	e, st, h := depsEngine(t, "B", "D1", "D2", "D3")
	two := 2
	e.Cfg.Hydration.MaxPerPoll = &two
	seedRow(t, st, "B", "F1", "2026-10-01T11:00:00Z", true)
	yes := true
	if err := st.SetEntityColumns("o/r", "issue", "B", nil, &yes); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"D1", "D2", "D3"} {
		seedDep(t, st, id, depOpenFacts, true, true)
		h.facts[id] = depMovedFacts
	}
	h.facts["B"] = blockerClosedFacts
	h.kindsOf["B"] = []string{"closed"}
	e.Lister = fakeLister{res: listing("B=F2", "D1=F1", "D2=F1", "D3=F1")}

	env, out, err := runTick(t, e)
	if err != nil {
		t.Fatal(err)
	}
	if got := h.calls; !reflect.DeepEqual(got, []string{"B", "D1"}) {
		t.Errorf("hydrations = %v, want the blocker and exactly one dependant (the budget's remainder)", got)
	}
	if !out.Partial {
		t.Error("a budget-cut dependant was not reported as degraded")
	}
	var reason string
	for _, s := range env.Sources {
		reason = s.Reason
	}
	if reason == "" || !strings.Contains(reason, ReasonHydrationBudget) {
		t.Errorf("source reason = %q, want %q", reason, ReasonHydrationBudget)
	}
}

// A blocker whose hydration appended no closed row triggers nothing.
func TestBlockerHydrationWithoutClosedTriggersNothing(t *testing.T) {
	e, st, h := depsEngine(t, "B", "D1")
	seedRow(t, st, "B", "F1", "2026-10-01T11:00:00Z", true)
	yes := true
	if err := st.SetEntityColumns("o/r", "issue", "B", nil, &yes); err != nil {
		t.Fatal(err)
	}
	seedDep(t, st, "D1", depOpenFacts, true, true)
	h.facts["B"] = `{"issue_show":{"id":"B","state":"open","title":"edited"},"issue_deps":{"ids":[]}}`
	h.facts["D1"] = depMovedFacts
	e.Lister = fakeLister{res: listing("B=F2", "D1=F1")}

	if _, _, err := runTick(t, e); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(h.calls, []string{"B"}) {
		t.Errorf("hydrations = %v, want only the blocker", h.calls)
	}
}

// A closed blocker with no stored issue_deps and no stored edge is unavailable:
// it is skipped (nothing proves it has no dependants), not treated as having
// none; the poll is not failed.
func TestClosedBlockerWithoutIssueDepsIsUnavailableAndSkipped(t *testing.T) {
	e, st, h := depsEngine(t, "B", "D1")
	seedRow(t, st, "B", "F1", "2026-10-01T11:00:00Z", true)
	yes := true
	if err := st.SetEntityColumns("o/r", "issue", "B", nil, &yes); err != nil {
		t.Fatal(err)
	}
	seedDep(t, st, "D1", `{"issue_show":{"state":"open","title":"t"}}`, true, true) // no stored edge
	h.facts["B"] = `{"issue_show":{"id":"B","state":"closed","title":"t"}}`         // no issue_deps
	h.kindsOf["B"] = []string{"closed"}
	var warn warnBuf
	e.Warn = &warn
	e.Lister = fakeLister{res: listing("B=F2", "D1=F1")}

	if _, out, err := runTick(t, e); err != nil || out.Partial {
		t.Fatalf("poll = %+v, %v", out, err)
	}
	if !reflect.DeepEqual(h.calls, []string{"B"}) {
		t.Errorf("hydrations = %v, want only the blocker", h.calls)
	}
	if !strings.Contains(warn.s, "unavailable") {
		t.Errorf("warning = %q, want the unavailable note", warn.s)
	}
}

type warnBuf struct{ s string }

func (w *warnBuf) Write(p []byte) (int, error) { w.s += string(p); return len(p), nil }

// A dependant of two closing blockers is re-hydrated once, and one already
// read fresh this poll is not read again.
func TestDependantOfTwoClosedBlockersIsRehydratedOnce(t *testing.T) {
	e, st, h := depsEngine(t, "B", "B2", "D1")
	yes := true
	for _, id := range []string{"B", "B2"} {
		seedRow(t, st, id, "F1", "2026-10-01T11:00:00Z", true)
		if err := st.SetEntityColumns("o/r", "issue", id, nil, &yes); err != nil {
			t.Fatal(err)
		}
		h.kindsOf[id] = []string{"closed"}
	}
	h.facts["B"] = blockerClosedFacts
	h.facts["B2"] = `{"issue_show":{"id":"B2","state":"closed"},"issue_deps":{"ids":[]}}`
	seedDep(t, st, "D1", `{"issue_show":{"state":"open","title":"t","deps":[{"id":"B","type":"blocks"},{"id":"B2","type":"blocks"}]}}`, true, true)
	h.facts["D1"] = depMovedFacts
	e.Lister = fakeLister{res: listing("B=F2", "B2=F2", "D1=F1")}

	if _, _, err := runTick(t, e); err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, c := range h.calls {
		if c == "D1" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("D1 hydrations = %d in %v, want exactly one", n, h.calls)
	}
}
