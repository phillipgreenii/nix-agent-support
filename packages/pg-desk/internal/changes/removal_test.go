package changes

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/classify"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/gather"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/pipeline"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// These tests pin the removal confirmation read: an entity that left every
// watched query is read once (`show`, change kind removed, nil list_fp)
// before it is deactivated, so a merged or closed PR is classified instead of
// being logged only as removed.
//
// Freedom boundary [design RULED D11]: the confirmation reads do NOT count
// against hydration.max_per_poll. They run in the membership phase, before the
// hydration budget exists, and cost exactly one read per candidate. Several
// candidates in one poll are confirmed in sorted id order, and
// backendDownStreak consecutive degraded reads end the reads for the tick.

// showOutcome is what the fake source answers for one entity's show.
type showOutcome struct {
	facts    string // the facts the read returns
	notFound bool
	degraded string
	err      error
}

// confirmHydrator stands in for the pipeline: it counts reads and, for a
// healthy read, writes the facts and the kinds the REAL classifier derives
// from the stored snapshot, the way RunEntityChange does.
type confirmHydrator struct {
	st      *store.Store
	shows   map[string]showOutcome
	calls   []string
	changes map[string]gather.ChangeKind
	listFPs map[string]*string
}

func newConfirmHydrator(st *store.Store) *confirmHydrator {
	return &confirmHydrator{st: st, shows: map[string]showOutcome{}, changes: map[string]gather.ChangeKind{}, listFPs: map[string]*string{}}
}

func (h *confirmHydrator) RunEntityChange(_ context.Context, entityType, id string, change gather.ChangeKind, opts pipeline.EntityChangeOptions) (pipeline.EntityChangeResult, error) {
	h.calls = append(h.calls, id)
	h.changes[id] = change
	h.listFPs[id] = opts.ListFP
	out := h.shows[id]
	switch {
	case out.err != nil:
		return pipeline.EntityChangeResult{}, out.err
	case out.notFound:
		return pipeline.EntityChangeResult{NotFound: true}, nil
	case out.degraded != "":
		return pipeline.EntityChangeResult{Degraded: out.degraded}, nil
	}
	row, found, err := h.st.GetEntity("o/r", entityType, id)
	if err != nil {
		return pipeline.EntityChangeResult{}, err
	}
	var expected int64
	old := classify.Snapshot{Type: entityType, ID: id}
	if found {
		expected = row.Version
		old.Exists, old.Active, old.Payload = true, !row.Inactive, []byte(row.Facts)
	}
	var kinds []string
	for _, r := range classify.Classify(old, classify.Snapshot{Type: entityType, ID: id, Exists: true, Active: true, Payload: []byte(out.facts)}) {
		kinds = append(kinds, string(r.Kind))
	}
	v, err := h.st.WriteEntityStateWithLogFP(store.Entity{Repo: "o/r", EntityType: entityType, EntityID: id, Facts: out.facts, AsOf: "x"},
		expected, "2026-10-01T11:59:00Z", true, opts.ListFP, kinds, opts.Origin, "2026-10-01T12:00:00Z")
	return pipeline.EntityChangeResult{Written: err == nil, Version: v, Kinds: kinds}, err
}

const (
	prOpenFacts   = `{"pr_show":{"state":"open","head_sha":"a1"}}`
	prMergedFacts = `{"pr_show":{"state":"merged","merged":true,"head_sha":"a1"}}`
	prClosedFacts = `{"pr_show":{"state":"closed","head_sha":"a1"}}`
)

func newPREngine(t *testing.T, queries ...string) (*Engine, *store.Store, *confirmHydrator) {
	t.Helper()
	st := store.OpenNewSchemaForTest(t)
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	cfg := &config.Config{Repos: []config.RepoConfig{{Remote: "o/r"}}}
	cfg.Watch.PR.Queries = queries
	h := newConfirmHydrator(st)
	return &Engine{
		Cfg: cfg, Store: st, Hydrator: h,
		Now: func() time.Time { return time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC) },
	}, st, h
}

// seedWatchedPR stores an active PR row with open facts and a persisted
// membership of the one query "open".
func seedWatchedPR(t *testing.T, st *store.Store, ids ...string) {
	t.Helper()
	set := map[string]bool{}
	for _, id := range ids {
		fp := "F1"
		if _, err := st.WriteEntityStateWithLogFP(store.Entity{Repo: "o/r", EntityType: "pr", EntityID: id, Facts: prOpenFacts, AsOf: "x"},
			0, "2026-10-01T11:00:00Z", true, &fp, []string{"reconcile"}, "sweep", "2026-09-29T10:00:00Z"); err != nil {
			t.Fatal(err)
		}
		set[id] = true
	}
	if err := saveWatchSet(st, "pr", "open", set); err != nil {
		t.Fatal(err)
	}
}

func runPRTick(t *testing.T, e *Engine) (Envelope, Outcome, error) {
	t.Helper()
	var env Envelope
	out, err := e.Run(context.Background(), Options{EntityType: "pr", Consumer: "router"}, func(v Envelope) error { env = v; return nil })
	return env, out, err
}

// kindsLogged returns the kinds the change log holds for one entity, oldest
// first, skipping the seeded reconcile row.
func kindsLogged(t *testing.T, st *store.Store, entityType, id string) []string {
	t.Helper()
	rows, err := st.ListChangesAfter(entityType, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, r := range rows {
		if r.EntityID != id {
			continue
		}
		for _, k := range r.Kinds {
			if k != "reconcile" {
				out = append(out, k)
			}
		}
	}
	return out
}

func prInactive(t *testing.T, st *store.Store, id string) bool {
	t.Helper()
	row, found, err := st.GetEntity("o/r", "pr", id)
	if err != nil || !found {
		t.Fatalf("pr %s: found=%v err=%v", id, found, err)
	}
	return row.Inactive
}

// The removal outcomes: terminal reads classify as merged/closed, an open read
// and a not_found read log removed; every one deactivates, drops the id from
// the persisted set, and costs exactly one show per candidate.
func TestRemovalConfirmationClassifiesWhatTheReadShows(t *testing.T) {
	cases := []struct {
		name string
		show showOutcome
		want []string
	}{
		{"merged", showOutcome{facts: prMergedFacts}, []string{"merged"}},
		{"closed", showOutcome{facts: prClosedFacts}, []string{"closed"}},
		{"still open", showOutcome{facts: prOpenFacts}, []string{"removed"}},
		{"not found", showOutcome{notFound: true}, []string{"removed"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, st, h := newPREngine(t, "open")
			seedWatchedPR(t, st, "pr-1")
			h.shows["pr-1"] = tc.show
			e.Lister = fakeLister{res: listing()} // pr-1 left the only query

			_, out, err := runPRTick(t, e)
			if err != nil || out.Partial {
				t.Fatalf("tick = %+v, %v", out, err)
			}
			if got := kindsLogged(t, st, "pr", "pr-1"); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("logged %v, want %v", got, tc.want)
			}
			if !prInactive(t, st, "pr-1") {
				t.Error("the entity must be deactivated")
			}
			if got := loadWatchSet(st, "pr", "open"); len(got) != 0 {
				t.Errorf("persisted = %v, want the id dropped", got)
			}
			if !reflect.DeepEqual(h.calls, []string{"pr-1"}) {
				t.Errorf("show calls = %v, want exactly one", h.calls)
			}
			if h.changes["pr-1"] != gather.ChangeRemoved {
				t.Errorf("change kind = %q, want removed", h.changes["pr-1"])
			}
			if h.listFPs["pr-1"] != nil {
				t.Error("the confirmation read must hydrate with a nil list_fp")
			}
		})
	}
}

// A closed bead leaves the `--status open` work-bead query; the same read
// (`issue show`) classifies it.
func TestRemovalConfirmationClassifiesAClosedBead(t *testing.T) {
	e, st, _ := newListEngine(t, "open")
	ch := newConfirmHydrator(st)
	e.Hydrator = ch
	seedRow(t, st, "bd-1", "F1", "2026-10-01T11:00:00Z", true)
	if _, err := st.WriteEntityStateWithLogFP(store.Entity{Repo: "o/r", EntityType: "issue", EntityID: "bd-1", Facts: `{"issue_show":{"state":"open"}}`, AsOf: "x"},
		1, "2026-10-01T11:00:00Z", true, nil, nil, "sweep", "2026-09-29T10:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if err := saveWatchSet(st, "issue", "open", map[string]bool{"bd-1": true}); err != nil {
		t.Fatal(err)
	}
	ch.shows["bd-1"] = showOutcome{facts: `{"issue_show":{"state":"closed"}}`}
	e.Lister = fakeLister{res: listing()}
	if _, _, err := runTick(t, e); err != nil {
		t.Fatal(err)
	}
	// The issue classifier also reports the state transition it implies.
	got := kindsLogged(t, st, "issue", "bd-1")
	hasClosed, hasRemoved := false, false
	for _, k := range got {
		hasClosed = hasClosed || k == "closed"
		hasRemoved = hasRemoved || k == "removed"
	}
	if !hasClosed || hasRemoved {
		t.Errorf("logged %v, want closed and no removed", got)
	}
	if !isInactive(t, st, "bd-1") || !reflect.DeepEqual(ch.calls, []string{"bd-1"}) {
		t.Errorf("inactive=%v calls=%v", isInactive(t, st, "bd-1"), ch.calls)
	}
}

// A failed or degraded confirmation read leaves the entity active and its
// membership untouched, writes no record, and the next tick reads it again.
func TestRemovalConfirmationFailedReadLeavesEverythingAndRetriesNextTick(t *testing.T) {
	for name, bad := range map[string]showOutcome{
		"error":    {err: errors.New("connector exploded")},
		"degraded": {degraded: "rate_limited"},
	} {
		t.Run(name, func(t *testing.T) {
			e, st, h := newPREngine(t, "open")
			seedWatchedPR(t, st, "pr-1")
			h.shows["pr-1"] = bad
			e.Lister = fakeLister{res: listing()}

			_, out, err := runPRTick(t, e)
			if err != nil || !out.Partial {
				t.Fatalf("failing tick = %+v, %v (a failed read is surfaced, not fatal)", out, err)
			}
			if prInactive(t, st, "pr-1") {
				t.Fatal("a failed read must leave the entity active")
			}
			if got := loadWatchSet(st, "pr", "open"); !got["pr-1"] || len(got) != 1 {
				t.Fatalf("persisted = %v, want pr-1 kept so it is found again", got)
			}
			if got := kindsLogged(t, st, "pr", "pr-1"); len(got) != 0 {
				t.Errorf("a failed read logged %v", got)
			}

			h.shows["pr-1"] = showOutcome{facts: prMergedFacts}
			if _, _, err := runPRTick(t, e); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(h.calls, []string{"pr-1", "pr-1"}) {
				t.Errorf("show calls = %v, want one per tick", h.calls)
			}
			if got := kindsLogged(t, st, "pr", "pr-1"); !reflect.DeepEqual(got, []string{"merged"}) || !prInactive(t, st, "pr-1") {
				t.Errorf("after the retry logged %v inactive=%v", got, prInactive(t, st, "pr-1"))
			}
			if got := loadWatchSet(st, "pr", "open"); len(got) != 0 {
				t.Errorf("persisted = %v after the removal succeeded", got)
			}
		})
	}
}

// A degraded or truncated source never makes a removal candidate, so it never
// triggers a confirmation read.
func TestRemovalConfirmationNeverHappensForADegradedOrTruncatedSource(t *testing.T) {
	degraded := listing()
	degraded.Sources = []gather.ListChangesSource{{Backend: "b", Status: "degraded", Reason: "rate_limited"}}
	truncated := listing()
	truncated.Truncated = true
	noSource := listing()
	noSource.Sources = nil
	for name, res := range map[string]gather.ListFingerprintsResult{"degraded": degraded, "truncated": truncated, "no source": noSource} {
		t.Run(name, func(t *testing.T) {
			e, st, h := newPREngine(t, "open")
			seedWatchedPR(t, st, "pr-1")
			e.Lister = fakeLister{res: res}
			if _, _, err := runPRTick(t, e); err != nil {
				t.Fatal(err)
			}
			if len(h.calls) != 0 {
				t.Errorf("show calls = %v, want none", h.calls)
			}
			if prInactive(t, st, "pr-1") {
				t.Error("the entity must stay active")
			}
		})
	}
}

// An id still listed by another query is not a candidate, hence not read.
func TestRemovalConfirmationSkipsAnIdAnotherQueryStillHolds(t *testing.T) {
	e, st, h := newPREngine(t, "a", "b")
	seedWatchedPR(t, st, "pr-1")
	if err := saveWatchSet(st, "pr", "a", map[string]bool{"pr-1": true}); err != nil {
		t.Fatal(err)
	}
	if err := saveWatchSet(st, "pr", "b", map[string]bool{"pr-1": true}); err != nil {
		t.Fatal(err)
	}
	e.Lister = queryLister{"a": {res: listing()}, "b": {res: listing("pr-1=F1")}}
	if _, _, err := runPRTick(t, e); err != nil {
		t.Fatal(err)
	}
	if len(h.calls) != 0 || prInactive(t, st, "pr-1") {
		t.Errorf("calls=%v inactive=%v", h.calls, prInactive(t, st, "pr-1"))
	}
}

// Several candidates are each read exactly once, in sorted id order, and one
// failure does not stop the others.
func TestRemovalConfirmationReadsEachCandidateOnceInSortedOrder(t *testing.T) {
	e, st, h := newPREngine(t, "open")
	seedWatchedPR(t, st, "pr-3", "pr-1", "pr-2")
	h.shows["pr-1"] = showOutcome{facts: prMergedFacts}
	h.shows["pr-2"] = showOutcome{err: errors.New("boom")}
	h.shows["pr-3"] = showOutcome{facts: prClosedFacts}
	e.Lister = fakeLister{res: listing()}
	_, out, err := runPRTick(t, e)
	if err != nil || !out.Partial {
		t.Fatalf("tick = %+v, %v", out, err)
	}
	if !reflect.DeepEqual(h.calls, []string{"pr-1", "pr-2", "pr-3"}) {
		t.Errorf("show calls = %v", h.calls)
	}
	if got := loadWatchSet(st, "pr", "open"); !got["pr-2"] || len(got) != 1 {
		t.Errorf("persisted = %v, want only the failed pr-2 kept", got)
	}
	if !prInactive(t, st, "pr-1") || prInactive(t, st, "pr-2") || !prInactive(t, st, "pr-3") {
		t.Error("pr-1 and pr-3 must be deactivated, pr-2 must stay active")
	}
}

// Consecutive degraded confirmation reads end the reads for the tick; the
// rest are left untouched and found again next tick.
func TestRemovalConfirmationStopsAfterADegradedStreak(t *testing.T) {
	e, st, h := newPREngine(t, "open")
	ids := []string{"pr-1", "pr-2", "pr-3", "pr-4", "pr-5"}
	seedWatchedPR(t, st, ids...)
	for _, id := range ids {
		h.shows[id] = showOutcome{degraded: "rate_limited"}
	}
	e.Lister = fakeLister{res: listing()}
	_, out, err := runPRTick(t, e)
	if err != nil || !out.Partial {
		t.Fatalf("tick = %+v, %v", out, err)
	}
	if len(h.calls) != backendDownStreak {
		t.Errorf("show calls = %v, want %d", h.calls, backendDownStreak)
	}
	if got := loadWatchSet(st, "pr", "open"); len(got) != len(ids) {
		t.Errorf("persisted = %v, want every id kept", got)
	}
}

// An entity the store does not hold, or that is already inactive, needs no
// read and writes nothing; the id is still dropped from the persisted set.
func TestRemovalConfirmationSkipsAbsentAndInactiveRows(t *testing.T) {
	e, st, h := newPREngine(t, "open")
	seedWatchedPR(t, st, "pr-1")
	if _, err := st.WriteEntityStateWithLog(store.Entity{Repo: "o/r", EntityType: "pr", EntityID: "pr-1", Facts: prOpenFacts, AsOf: "x"},
		1, "2026-10-01T11:00:00Z", false, nil, "sweep", "2026-09-29T10:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if err := saveWatchSet(st, "pr", "open", map[string]bool{"pr-1": true, "pr-ghost": true}); err != nil {
		t.Fatal(err)
	}
	e.Lister = fakeLister{res: listing()}
	if _, out, err := runPRTick(t, e); err != nil || out.Partial {
		t.Fatalf("tick = %+v, %v", out, err)
	}
	if len(h.calls) != 0 {
		t.Errorf("show calls = %v, want none", h.calls)
	}
	if got := loadWatchSet(st, "pr", "open"); len(got) != 0 {
		t.Errorf("persisted = %v", got)
	}
	if _, found, _ := st.GetEntity("o/r", "pr", "pr-ghost"); found {
		t.Error("a ghost id must not be created by the confirmation")
	}
}

// The failure text names the id and the read, never a retry counter.
func TestRemovalConfirmationFailureIsSurfacedByName(t *testing.T) {
	e, st, h := newPREngine(t, "open")
	seedWatchedPR(t, st, "pr-1")
	h.shows["pr-1"] = showOutcome{err: errors.New("connector exploded\nstack")}
	e.Lister = fakeLister{res: listing()}
	got := e.applyMembership("pr", []queryRun{{query: "open", res: listing()}})
	if len(got) != 1 || !strings.Contains(got[0], "remove pr-1") || !strings.Contains(got[0], "confirmation read") || strings.Contains(got[0], "stack") {
		t.Errorf("failures = %q", got)
	}
}
