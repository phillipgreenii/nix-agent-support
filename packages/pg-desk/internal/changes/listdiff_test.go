package changes

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/gather"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/pipeline"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// okSources is one healthy backend row.
var okSources = []gather.ListChangesSource{{Backend: "b", Status: "succeeded"}}

// okListing is a complete, empty listing from one healthy backend.
func okListing() gather.ListFingerprintsResult {
	return gather.ListFingerprintsResult{Sources: okSources, Fingerprints: map[string]string{}}
}

// listing builds a complete listing of id=fingerprint pairs (in the order
// given, as "id=fp" strings).
func listing(pairs ...string) gather.ListFingerprintsResult {
	res := okListing()
	for _, p := range pairs {
		id, fp, _ := strings.Cut(p, "=")
		res.Entities = append(res.Entities, gather.ListedEntity{ID: id, Title: "title of " + id})
		res.Fingerprints[id] = fp
	}
	return res
}

// storeHydrator stands in for the pipeline: it writes the entity row the way
// RunEntityChange does (one transaction carrying the observed ListFP), or
// fails per entity.
type storeHydrator struct {
	st      *store.Store
	fail    map[string]error
	calls   []string
	listFPs map[string]*string
	changes map[string]gather.ChangeKind
	origins map[string]string
}

func newStoreHydrator(st *store.Store) *storeHydrator {
	return &storeHydrator{st: st, fail: map[string]error{}, listFPs: map[string]*string{}, changes: map[string]gather.ChangeKind{}, origins: map[string]string{}}
}

func (h *storeHydrator) RunEntityChange(_ context.Context, entityType, id string, change gather.ChangeKind, opts pipeline.EntityChangeOptions) (pipeline.EntityChangeResult, error) {
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
	var expected int64
	if found {
		expected = row.Version
	}
	v, err := h.st.WriteEntityStateWithLogFP(store.Entity{Repo: "o/r", EntityType: entityType, EntityID: id, Facts: `{}`, AsOf: "x"},
		expected, "2026-10-01T11:59:00Z", true, opts.ListFP, []string{"reconcile"}, opts.Origin, "2026-10-01T12:00:00Z")
	return pipeline.EntityChangeResult{Written: err == nil, Version: v}, err
}

func newListEngine(t *testing.T, queries ...string) (*Engine, *store.Store, *storeHydrator) {
	t.Helper()
	st := store.OpenNewSchemaForTest(t)
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	cfg := &config.Config{Repos: []config.RepoConfig{{Remote: "o/r"}}}
	cfg.Watch.Issue.Queries = queries
	h := newStoreHydrator(st)
	return &Engine{
		Cfg: cfg, Store: st, Hydrator: h,
		Now: func() time.Time { return time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC) },
	}, st, h
}

func runTick(t *testing.T, e *Engine, opts ...Options) (Envelope, Outcome, error) {
	t.Helper()
	o := Options{EntityType: "issue", Consumer: "router"}
	if len(opts) > 0 {
		o = opts[0]
	}
	var env Envelope
	out, err := e.Run(context.Background(), o, func(v Envelope) error { env = v; return nil })
	return env, out, err
}

func listFPOf(t *testing.T, st *store.Store, id string) string {
	t.Helper()
	row, found, err := st.GetEntity("o/r", "issue", id)
	if err != nil || !found {
		t.Fatalf("entity %s: found=%v err=%v", id, found, err)
	}
	return row.ListFP
}

func seedRow(t *testing.T, st *store.Store, id, listFP, hydratedAt string, active bool) {
	t.Helper()
	fp := listFP
	if _, err := st.WriteEntityStateWithLogFP(store.Entity{Repo: "o/r", EntityType: "issue", EntityID: id, Facts: `{}`, AsOf: "x"},
		0, hydratedAt, active, &fp, []string{"reconcile"}, "sweep", "2026-09-29T10:00:00Z"); err != nil {
		t.Fatal(err)
	}
}

func idsOf(entities []*listedEntity) []string {
	var out []string
	for _, e := range entities {
		out = append(out, e.id)
	}
	return out
}

// --- fingerprint compare [T-1] ---

func TestDiffListedComparesListAgainstStoredListFP(t *testing.T) {
	e, st, _ := newListEngine(t, "open")
	seedRow(t, st, "same", "F1", "2026-10-01T11:00:00Z", true)
	seedRow(t, st, "differs", "F1", "2026-10-01T11:00:00Z", true)
	seedRow(t, st, "gone", "F1", "2026-10-01T11:00:00Z", false)
	seedRow(t, st, "cutover", "", "2026-10-01T11:00:00Z", true)

	res := listing("same=F1", "differs=F2", "new=F1", "gone=F1", "cutover=F1", "stale=F9")
	res.Entities[5].Stale = true
	got, err := e.diffListed("issue", []queryRun{{query: "open", res: res}})
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]gather.ChangeKind{}
	for _, g := range got {
		kinds[g.id] = g.change
	}
	want := map[string]gather.ChangeKind{
		"differs": gather.ChangeChanged,
		"cutover": gather.ChangeChanged, // an empty list_fp is changed once
		"new":     gather.ChangeAdded,
		"gone":    gather.ChangeAdded, // an inactive row is added again
	}
	if !reflect.DeepEqual(kinds, want) {
		t.Errorf("verdicts = %v, want %v (same is unchanged, stale is ignored)", kinds, want)
	}
}

func TestDiffListedOrdersChangedOldestFirstThenAdded(t *testing.T) {
	e, st, _ := newListEngine(t, "open")
	seedRow(t, st, "c-new", "F1", "2026-10-01T11:00:00Z", true)
	seedRow(t, st, "c-old", "F1", "2026-09-01T00:00:00Z", true)
	seedRow(t, st, "c-never", "F1", "", true)
	res := listing("z-added=F1", "a-added=F1", "c-new=F2", "c-old=F2", "c-never=F2")
	got, err := e.diffListed("issue", []queryRun{{query: "open", res: res}})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"c-never", "c-old", "c-new", "a-added", "z-added"}
	if !reflect.DeepEqual(idsOf(got), want) {
		t.Errorf("order = %v, want %v", idsOf(got), want)
	}
}

func TestDiffListedMergesAnEntityListedByTwoQueries(t *testing.T) {
	e, _, _ := newListEngine(t, "a", "b")
	got, err := e.diffListed("issue", []queryRun{
		{query: "a", res: listing("x=F1")},
		{query: "b", res: listing("x=F1")},
	})
	if err != nil || len(got) != 1 || !reflect.DeepEqual(got[0].queries, []string{"a", "b"}) {
		t.Fatalf("got %+v, %v", got, err)
	}
}

// --- hydrate-and-store behavior [T-3, T-4, T-5] ---

func TestRunStoresTheObservedListFingerprintAndThenIsQuiet(t *testing.T) {
	e, st, h := newListEngine(t, "open")
	e.Lister = fakeLister{res: listing("bd-1=F1")}
	env, _, err := runTick(t, e)
	if err != nil || len(env.Records) != 1 {
		t.Fatalf("tick 1 = %+v, %v", env, err)
	}
	if got := listFPOf(t, st, "bd-1"); got != "F1" {
		t.Errorf("list_fp = %q, want the observed F1", got)
	}
	if h.changes["bd-1"] != gather.ChangeAdded || h.origins["bd-1"] != OriginPGConnector {
		t.Errorf("hydrated as %v/%v", h.changes["bd-1"], h.origins["bd-1"])
	}
	h.calls = nil
	if _, _, err := runTick(t, e); err != nil {
		t.Fatal(err)
	}
	if len(h.calls) != 0 {
		t.Errorf("an unchanged fingerprint hydrated %v", h.calls)
	}
}

// A hydration that fails writes nothing, so the next tick finds the entity
// different again, with no deferral queue and no give-up (even past five
// consecutive failures).
func TestFailedHydrationIsRetriedEveryTickWithNoQueueAndNoGiveUp(t *testing.T) {
	e, st, h := newListEngine(t, "open")
	e.Lister = fakeLister{res: listing("bd-1=F1")}
	h.fail["bd-1"] = errors.New("boom")
	for tick := 1; tick <= 8; tick++ {
		before := len(h.calls)
		_, out, err := runTick(t, e)
		if err != nil {
			t.Fatalf("tick %d: %v", tick, err)
		}
		if !out.Partial || len(h.calls) != before+1 {
			t.Fatalf("tick %d: partial=%v calls=%v, want the failed entity retried", tick, out.Partial, h.calls)
		}
		if _, found, _ := st.GetEntity("o/r", "issue", "bd-1"); found {
			t.Fatalf("tick %d: a failed hydration wrote a row", tick)
		}
		if _, found, _ := st.GetMeta("change_flow.deferred.issue"); found {
			t.Fatalf("tick %d: a deferral queue was persisted", tick)
		}
	}
	delete(h.fail, "bd-1")
	env, _, err := runTick(t, e)
	if err != nil || len(env.Records) != 1 || listFPOf(t, st, "bd-1") != "F1" {
		t.Errorf("recovery = %+v, %v", env, err)
	}
}

func TestDegradedHydrationLeavesListFPUntouched(t *testing.T) {
	e, st, _ := newListEngine(t, "open")
	seedRow(t, st, "bd-1", "F1", "2026-10-01T11:00:00Z", true)
	e.Lister = fakeLister{res: listing("bd-1=F2")}
	e.Hydrator = degradedHydrator{}
	if _, out, err := runTick(t, e); err != nil || !out.Partial {
		t.Fatalf("tick = %+v, %v", out, err)
	}
	if got := listFPOf(t, st, "bd-1"); got != "F1" {
		t.Errorf("list_fp = %q after a degraded read, want F1", got)
	}
}

type degradedHydrator struct{}

func (degradedHydrator) RunEntityChange(context.Context, string, string, gather.ChangeKind, pipeline.EntityChangeOptions) (pipeline.EntityChangeResult, error) {
	return pipeline.EntityChangeResult{Degraded: "rate_limited"}, nil
}

// A list that moves from F1 to F2 during a run is found on the next tick:
// the stored value is the F1 the hydration observed.
func TestListMovingDuringARunIsDetectedNextTick(t *testing.T) {
	e, st, h := newListEngine(t, "open")
	e.Lister = fakeLister{res: listing("bd-1=F1")}
	if _, _, err := runTick(t, e); err != nil {
		t.Fatal(err)
	}
	h.calls = nil
	// The entity changed after the listing was taken: the next listing shows F2.
	e.Lister = fakeLister{res: listing("bd-1=F2")}
	if _, _, err := runTick(t, e); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(h.calls, []string{"bd-1"}) || h.changes["bd-1"] != gather.ChangeChanged {
		t.Errorf("calls = %v changes = %v, want bd-1 hydrated as changed", h.calls, h.changes)
	}
	if got := listFPOf(t, st, "bd-1"); got != "F2" {
		t.Errorf("list_fp = %q, want F2", got)
	}
}

func TestBudgetLeftoverIsFoundAgainWithoutAQueue(t *testing.T) {
	e, st, h := newListEngine(t, "open")
	one := 1
	e.Cfg.Hydration.MaxPerPoll = &one
	e.Lister = fakeLister{res: listing("bd-1=F1", "bd-2=F1")}
	env, out, err := runTick(t, e)
	if err != nil || !out.Partial || len(h.calls) != 1 || h.calls[0] != "bd-1" {
		t.Fatalf("tick 1 = %+v %+v calls=%v err=%v", env, out, h.calls, err)
	}
	if len(env.Sources) != 1 || !strings.Contains(env.Sources[0].Reason, ReasonHydrationBudget) {
		t.Errorf("sources = %+v", env.Sources)
	}
	env, out, err = runTick(t, e)
	if err != nil || out.Partial || len(h.calls) != 2 || h.calls[1] != "bd-2" {
		t.Fatalf("tick 2 = %+v %+v calls=%v err=%v", env, out, h.calls, err)
	}
	if _, found, _ := st.GetMeta("change_flow.deferred.issue"); found {
		t.Error("a deferral queue was persisted")
	}
}

func TestStaleListedEntityIsNotHydrated(t *testing.T) {
	e, _, h := newListEngine(t, "open")
	res := listing("bd-1=F1")
	res.Entities[0].Stale = true
	e.Lister = fakeLister{res: res}
	if _, _, err := runTick(t, e); err != nil {
		t.Fatal(err)
	}
	if len(h.calls) != 0 {
		t.Errorf("a stale listed entity was hydrated: %v", h.calls)
	}
}

func TestResetAndSweepHydrateWithNilListFP(t *testing.T) {
	e, st, h := newListEngine(t, "open")
	seedRow(t, st, "bd-1", "F1", "2026-09-01T00:00:00Z", true) // due for the sweep
	e.Lister = fakeLister{res: listing("bd-1=F1")}
	if _, _, err := runTick(t, e); err != nil {
		t.Fatal(err)
	}
	if h.origins["bd-1"] != OriginSweep || h.listFPs["bd-1"] != nil {
		t.Errorf("sweep hydration origin=%q listFP=%v, want sweep/nil", h.origins["bd-1"], h.listFPs["bd-1"])
	}
	if got := listFPOf(t, st, "bd-1"); got != "F1" {
		t.Errorf("a nil listFP must leave list_fp untouched, got %q", got)
	}
	if _, _, err := runTick(t, e, Options{EntityType: "issue", Consumer: "router", Reset: true}); err != nil {
		t.Fatal(err)
	}
	if h.origins["bd-1"] != OriginReset || h.listFPs["bd-1"] != nil {
		t.Errorf("reset hydration origin=%q listFP=%v", h.origins["bd-1"], h.listFPs["bd-1"])
	}
}

func TestTruncatedListingIsDegradedAndNeverRemoves(t *testing.T) {
	e, st, _ := newListEngine(t, "open")
	seedRow(t, st, "bd-1", "F1", "2026-10-01T11:00:00Z", true)
	if err := saveWatchSet(st, "issue", "open", map[string]bool{"bd-1": true}); err != nil {
		t.Fatal(err)
	}
	res := listing()
	res.Truncated = true
	e.Lister = fakeLister{res: res}
	env, out, err := runTick(t, e)
	if err != nil || !out.Partial || len(env.Sources) != 1 || env.Sources[0].Status != StatusDegraded ||
		!strings.Contains(env.Sources[0].Reason, ReasonListTruncated) {
		t.Fatalf("env = %+v %+v %v", env, out, err)
	}
	if row, _, _ := st.GetEntity("o/r", "issue", "bd-1"); row.Inactive {
		t.Error("a truncated listing removed an entity")
	}
}

func TestUnsupportedTypeIsRefusedBeforeAnyCall(t *testing.T) {
	e, _, h := newListEngine(t, "open")
	e.Cfg.Watch.Thread.Queries = []string{"mentions"}
	e.Lister = panicLister{}
	_, _, err := runTick(t, e, Options{EntityType: "thread", Consumer: "router"})
	if !errors.Is(err, ErrUnsupportedType) || len(h.calls) != 0 {
		t.Fatalf("err = %v calls = %v", err, h.calls)
	}
	if _, _, err := runTick(t, e, Options{EntityType: "thread", Consumer: "router", Cached: true}); !errors.Is(err, ErrUnsupportedType) {
		t.Errorf("--cached on thread: %v", err)
	}
}

type panicLister struct{}

func (panicLister) ListFingerprints(context.Context, string, string) (gather.ListFingerprintsResult, error) {
	panic("the connector must not be called")
}

// --- membership and removal candidates ---

func persisted(t *testing.T, st *store.Store, query string) []string {
	t.Helper()
	set := loadWatchSet(st, "issue", query)
	var ids []string
	for id := range set {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func isInactive(t *testing.T, st *store.Store, id string) bool {
	t.Helper()
	row, found, err := st.GetEntity("o/r", "issue", id)
	if err != nil || !found {
		t.Fatalf("entity %s: found=%v err=%v", id, found, err)
	}
	return row.Inactive
}

func TestMembershipCompleteListingRewritesThePersistedSet(t *testing.T) {
	e, st, _ := newListEngine(t, "open")
	e.Lister = fakeLister{res: listing("bd-1=F1", "bd-2=F1")}
	if _, _, err := runTick(t, e); err != nil {
		t.Fatal(err)
	}
	if got := persisted(t, st, "open"); !reflect.DeepEqual(got, []string{"bd-1", "bd-2"}) {
		t.Fatalf("persisted = %v", got)
	}
	e.Lister = fakeLister{res: listing("bd-2=F1", "bd-3=F1")}
	if _, _, err := runTick(t, e); err != nil {
		t.Fatal(err)
	}
	if got := persisted(t, st, "open"); !reflect.DeepEqual(got, []string{"bd-2", "bd-3"}) {
		t.Errorf("persisted = %v, want exactly the listed ids", got)
	}
	if !isInactive(t, st, "bd-1") || isInactive(t, st, "bd-2") {
		t.Error("bd-1 left the listing and must be removed; bd-2 stays")
	}
}

func TestMembershipIdStillListedByAnotherQueryIsNotACandidate(t *testing.T) {
	e, st, _ := newListEngine(t, "a", "b")
	e.Lister = queryLister{"a": {res: listing("bd-1=F1")}, "b": {res: listing("bd-1=F1")}}
	if _, _, err := runTick(t, e); err != nil {
		t.Fatal(err)
	}
	e.Lister = queryLister{"a": {res: listing()}, "b": {res: listing("bd-1=F1")}}
	if _, _, err := runTick(t, e); err != nil {
		t.Fatal(err)
	}
	if isInactive(t, st, "bd-1") {
		t.Error("bd-1 is still listed by query b and must survive query a dropping it")
	}
	if got := persisted(t, st, "a"); len(got) != 0 {
		t.Errorf("a's persisted set = %v, want empty", got)
	}
	e.Lister = queryLister{"a": {res: listing()}, "b": {res: listing()}}
	if _, _, err := runTick(t, e); err != nil {
		t.Fatal(err)
	}
	if !isInactive(t, st, "bd-1") {
		t.Error("bd-1 is absent from every complete listing and must be removed")
	}
}

func TestMembershipDegradedListingNeverRemovesAndOnlyAdds(t *testing.T) {
	e, st, _ := newListEngine(t, "open")
	e.Lister = fakeLister{res: listing("bd-1=F1")}
	if _, _, err := runTick(t, e); err != nil {
		t.Fatal(err)
	}
	degraded := listing("bd-2=F1")
	degraded.Sources = []gather.ListChangesSource{{Backend: "b", Status: "degraded", Reason: "rate_limited"}}
	e.Lister = fakeLister{res: degraded}
	if _, out, err := runTick(t, e); err != nil || !out.Partial {
		t.Fatalf("degraded tick: %+v %v", out, err)
	}
	if isInactive(t, st, "bd-1") {
		t.Error("a degraded listing removed an entity")
	}
	if got := persisted(t, st, "open"); !reflect.DeepEqual(got, []string{"bd-1", "bd-2"}) {
		t.Errorf("persisted = %v, want add-only {bd-1, bd-2}", got)
	}
}

func TestMembershipFailedDeactivationKeepsTheIdPersistedAndRetries(t *testing.T) {
	e, st, _ := newListEngine(t, "open")
	e.Lister = fakeLister{res: listing("bd-1=F1")}
	if _, _, err := runTick(t, e); err != nil {
		t.Fatal(err)
	}
	e.Lister = fakeLister{res: listing()}
	st.SetBetweenBumpAndAppendHook(func() error { return errors.New("injected") })
	_, out, err := runTick(t, e)
	st.SetBetweenBumpAndAppendHook(nil)
	if err != nil || !out.Partial {
		t.Fatalf("failing tick = %+v, %v (a failed removal is surfaced, not fatal)", out, err)
	}
	if isInactive(t, st, "bd-1") {
		t.Fatal("the failed deactivation must leave the entity active")
	}
	if got := persisted(t, st, "open"); !reflect.DeepEqual(got, []string{"bd-1"}) {
		t.Fatalf("persisted = %v, want bd-1 kept so it is found again", got)
	}
	if _, _, err := runTick(t, e); err != nil {
		t.Fatal(err)
	}
	if !isInactive(t, st, "bd-1") {
		t.Error("the next tick must remove bd-1")
	}
	if got := persisted(t, st, "open"); len(got) != 0 {
		t.Errorf("persisted = %v after the removal succeeded", got)
	}
}

// The "listed by another query" test reads the persisted set of EVERY
// configured query, even when --query runs only one.
func TestMembershipOtherQueryTestUsesPersistedSetsUnderQueryFlag(t *testing.T) {
	e, st, _ := newListEngine(t, "a", "b")
	e.Lister = queryLister{"a": {res: listing("bd-1=F1")}, "b": {res: listing("bd-1=F1")}}
	if _, _, err := runTick(t, e); err != nil {
		t.Fatal(err)
	}
	// Only query a runs now, and it no longer lists bd-1; b was not run but
	// its persisted set still holds bd-1.
	e.Lister = queryLister{"a": {res: listing()}}
	opts := Options{EntityType: "issue", Consumer: "router", Query: "a"}
	if _, _, err := runTick(t, e, opts); err != nil {
		t.Fatal(err)
	}
	if isInactive(t, st, "bd-1") {
		t.Error("bd-1 is held by b's persisted set and must not be a removal candidate under --query a")
	}
	if got := persisted(t, st, "b"); !reflect.DeepEqual(got, []string{"bd-1"}) {
		t.Errorf("b's persisted set = %v, want untouched", got)
	}
}

func TestMembershipFailedQueryContributesNothing(t *testing.T) {
	e, st, _ := newListEngine(t, "open", "mine")
	e.Lister = queryLister{"open": {res: listing("bd-1=F1")}, "mine": {res: listing("bd-1=F1")}}
	if _, _, err := runTick(t, e); err != nil {
		t.Fatal(err)
	}
	e.Lister = queryLister{"open": {res: listing()}, "mine": {err: fmt.Errorf("boom")}}
	if _, _, err := runTick(t, e); err != nil {
		t.Fatal(err)
	}
	if isInactive(t, st, "bd-1") {
		t.Error("a failed query must not let another query's drop remove the entity: mine's persisted set still holds it")
	}
}

// Every complete listing needs a succeeded backend: a listing with no sources
// at all proves nothing.
func TestMembershipListingWithNoSucceededSourceRemovesNothing(t *testing.T) {
	e, st, _ := newListEngine(t, "open")
	e.Lister = fakeLister{res: listing("bd-1=F1")}
	if _, _, err := runTick(t, e); err != nil {
		t.Fatal(err)
	}
	empty := listing()
	empty.Sources = nil
	e.Lister = fakeLister{res: empty}
	if _, _, err := runTick(t, e); err != nil {
		t.Fatal(err)
	}
	if isInactive(t, st, "bd-1") {
		t.Error("a listing with no succeeded source removed an entity")
	}
}
