package changes

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/gather"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/pipeline"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// These tests pin the orphan selection: a watched, active issue that no watched
// query lists any longer is not removed or deactivated; at most
// hydration.orphan_max_per_poll of them are read by id per poll, FIRST and out
// of hydration.max_per_poll, oldest last-attempt first; an unchanged read writes
// nothing but the two read times.

// orphanHydrator is the storeHydrator that also honours SkipUnchangedWrite the
// way the pipeline does (equal normalised snapshot: nothing written) and can
// answer not_found or degraded for an orphan read.
type orphanHydrator struct {
	*storeHydrator
	notFound map[string]bool
	degraded map[string]string
	skipSeen map[string]bool
}

func newOrphanHydrator(st *store.Store) *orphanHydrator {
	return &orphanHydrator{storeHydrator: newStoreHydrator(st), notFound: map[string]bool{}, degraded: map[string]string{}, skipSeen: map[string]bool{}}
}

func (h *orphanHydrator) RunEntityChange(ctx context.Context, entityType, id string, change gather.ChangeKind, opts pipeline.EntityChangeOptions) (pipeline.EntityChangeResult, error) {
	if !opts.SkipUnchangedWrite {
		return h.storeHydrator.RunEntityChange(ctx, entityType, id, change, opts)
	}
	h.calls = append(h.calls, id)
	h.listFPs[id] = opts.ListFP
	h.changes[id] = change
	h.skipSeen[id] = true
	if err := h.fail[id]; err != nil {
		return pipeline.EntityChangeResult{}, err
	}
	if h.notFound[id] {
		return pipeline.EntityChangeResult{NotFound: true}, nil
	}
	if d := h.degraded[id]; d != "" {
		return pipeline.EntityChangeResult{Degraded: d}, nil
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
	v, err := h.st.WriteEntityStateWithLogFP(store.Entity{Repo: "o/r", EntityType: entityType, EntityID: id, Facts: facts, AsOf: "x"},
		expected, "2026-10-01T11:59:00Z", true, opts.ListFP, []string{"changed"}, opts.Origin, "2026-10-01T12:00:00Z")
	return pipeline.EntityChangeResult{Written: err == nil, Version: v, Kinds: []string{"changed"}}, err
}

// newOrphanEngine is a baselined engine over the one query "open" with the
// orphan-aware hydrator and a clock that advances one second per reading, so
// successive read times differ.
func newOrphanEngine(t *testing.T) (*Engine, *store.Store, *orphanHydrator) {
	t.Helper()
	e, st, _ := newListEngine(t, "open")
	h := newOrphanHydrator(st)
	e.Hydrator = h
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	n := 0
	e.Now = func() time.Time {
		n++
		return base.Add(time.Duration(n) * time.Second)
	}
	return e, st, h
}

// seedOrphan stores an active, watched issue with facts "{}" and list_fp F1
// that no query's persisted set holds.
func seedOrphan(t *testing.T, st *store.Store, id string) {
	t.Helper()
	seedRow(t, st, id, "F1", "2026-10-01T11:00:00Z", true)
	yes := true
	if err := st.SetEntityColumns("o/r", "issue", id, nil, &yes); err != nil {
		t.Fatal(err)
	}
}

func seedAttempt(t *testing.T, st *store.Store, id, at string) {
	t.Helper()
	if err := st.SetEntityReadTimes("o/r", "issue", id, &at, nil); err != nil {
		t.Fatal(err)
	}
}

func entityOf(t *testing.T, st *store.Store, id string) store.Entity {
	t.Helper()
	row, found, err := st.GetEntity("o/r", "issue", id)
	if err != nil || !found {
		t.Fatalf("entity %s: found=%v err=%v", id, found, err)
	}
	return row
}

func removedRows(t *testing.T, st *store.Store, id string) int {
	t.Helper()
	n := 0
	for _, r := range historyOf(t, st, id) {
		for _, k := range r.Kinds {
			if k == "removed" {
				n++
			}
		}
	}
	return n
}

func orphanCalls(calls []string) []string {
	var out []string
	for _, c := range calls {
		if len(c) >= 3 && c[:3] == "bd-" && len(c) > 3 && c[3] == 'o' {
			out = append(out, c)
		}
	}
	return out
}

// (1) A watched entity that left every query is not deactivated, appends no
// removed row, and is read by id at this poll and the next.
func TestWatchedIssueThatLeftEveryQueryIsReadByIDNotRemoved(t *testing.T) {
	e, st, h := newOrphanEngine(t)
	seedOrphan(t, st, "bd-1")
	if err := saveWatchSet(st, "issue", "open", map[string]bool{"bd-1": true}); err != nil {
		t.Fatal(err)
	}
	e.Lister = fakeLister{res: listing()} // bd-1 left the only query

	for poll := 1; poll <= 2; poll++ {
		if _, out, err := runTick(t, e); err != nil || out.Partial {
			t.Fatalf("poll %d = %+v, %v", poll, out, err)
		}
		if isInactive(t, st, "bd-1") {
			t.Fatalf("poll %d: the orphan was deactivated", poll)
		}
		if w, _ := watchedOf(t, st, "bd-1"); !w {
			t.Fatalf("poll %d: the orphan lost its watched mark", poll)
		}
		if n := removedRows(t, st, "bd-1"); n != 0 {
			t.Fatalf("poll %d: %d removed rows, want none", poll, n)
		}
		if got := len(h.calls); got != poll {
			t.Fatalf("poll %d: reads = %v, want one read per poll", poll, h.calls)
		}
	}
	if got := persisted(t, st, "open"); len(got) != 0 {
		t.Errorf("persisted = %v, want the id dropped from the set", got)
	}
	if h.changes["bd-1"] != gather.ChangeRemoved || h.listFPs["bd-1"] != nil || !h.skipSeen["bd-1"] {
		t.Errorf("read shape: change=%q listFP=%v skipUnchanged=%v", h.changes["bd-1"], h.listFPs["bd-1"], h.skipSeen["bd-1"])
	}
}

// An unwatched issue that left its query keeps the removal confirmation.
func TestUnwatchedIssueThatLeftEveryQueryIsStillRemovalConfirmed(t *testing.T) {
	e, st, h := newOrphanEngine(t)
	seedRow(t, st, "bd-1", "F1", "2026-10-01T11:00:00Z", true) // watched NULL
	if err := saveWatchSet(st, "issue", "open", map[string]bool{"bd-1": true}); err != nil {
		t.Fatal(err)
	}
	e.Lister = fakeLister{res: listing()}
	if _, _, err := runTick(t, e); err != nil {
		t.Fatal(err)
	}
	if !isInactive(t, st, "bd-1") || removedRows(t, st, "bd-1") != 1 {
		t.Errorf("inactive=%v removed=%d, want the unchanged removal", isInactive(t, st, "bd-1"), removedRows(t, st, "bd-1"))
	}
	if len(h.calls) != 1 {
		t.Errorf("reads = %v, want the one confirmation read", h.calls)
	}
}

// (2) With 25 orphans, orphan_max_per_poll 10 and hydration.max_per_poll 50,
// each poll reads at most 10 orphans, still leaves 40 hydrations to changed
// members, and each orphan is attempted within ceil(25/10) = 3 polls.
func TestOrphanReadsAreCappedLeaveTheRestToMembersAndRotate(t *testing.T) {
	e, st, h := newOrphanEngine(t)
	var orphans []string
	for i := 0; i < 25; i++ {
		id := fmt.Sprintf("bd-o%02d", i)
		seedOrphan(t, st, id)
		orphans = append(orphans, id)
	}
	var pairs []string
	for i := 0; i < 60; i++ {
		pairs = append(pairs, fmt.Sprintf("bd-m%02d=F1", i))
	}
	e.Lister = fakeLister{res: listing(pairs...)}

	attempted := map[string]bool{}
	for poll := 1; poll <= 3; poll++ {
		h.calls = nil
		if _, _, err := runTick(t, e); err != nil {
			t.Fatal(err)
		}
		reads := orphanCalls(h.calls)
		if len(reads) > 10 {
			t.Fatalf("poll %d read %d orphans, want at most 10", poll, len(reads))
		}
		// 25 orphans always fill the cap: poll 3 reads the 5 never attempted
		// plus the 5 attempted longest ago.
		if len(reads) != 10 {
			t.Errorf("poll %d read %d orphans, want 10", poll, len(reads))
		}
		if poll == 1 {
			if got := len(h.calls) - len(reads); got != 40 {
				t.Errorf("poll 1: %d member hydrations, want 40 (50 - 10)", got)
			}
			// The orphans come first, before any member.
			if !reflect.DeepEqual(h.calls[:10], reads) {
				t.Errorf("poll 1: orphans were not read first: %v", h.calls[:12])
			}
		}
		if len(h.calls) > 50 {
			t.Errorf("poll %d: %d hydrations, want at most the budget 50", poll, len(h.calls))
		}
		for _, id := range reads {
			attempted[id] = true
		}
	}
	for _, id := range orphans {
		if !attempted[id] {
			t.Errorf("%s was not attempted within 3 polls", id)
		}
		if entityOf(t, st, id).ReadAttemptAt == "" {
			t.Errorf("%s has no read_attempt_at", id)
		}
	}
}

// (3) A read with the same normalised snapshot writes only the two read times:
// the version, facts, list_fp and change log are unchanged.
func TestUnchangedOrphanReadWritesOnlyTheTwoReadTimes(t *testing.T) {
	e, st, h := newOrphanEngine(t)
	seedOrphan(t, st, "bd-o1")
	before := entityOf(t, st, "bd-o1")
	histBefore := len(historyOf(t, st, "bd-o1"))
	e.Lister = fakeLister{res: listing()}

	if _, out, err := runTick(t, e); err != nil || out.Partial {
		t.Fatalf("tick = %+v, %v", out, err)
	}
	if !reflect.DeepEqual(h.calls, []string{"bd-o1"}) {
		t.Fatalf("reads = %v", h.calls)
	}
	after := entityOf(t, st, "bd-o1")
	if after.Version != before.Version || after.Facts != before.Facts || after.ListFP != before.ListFP || after.HydratedAt != before.HydratedAt {
		t.Errorf("snapshot columns moved: before=%+v after=%+v", before, after)
	}
	if got := len(historyOf(t, st, "bd-o1")); got != histBefore {
		t.Errorf("change log grew from %d to %d rows", histBefore, got)
	}
	if after.ListFP != "F1" {
		t.Errorf("list_fp = %q, want F1 untouched", after.ListFP)
	}
	if after.ReadAttemptAt == "" || after.ReadOKAt == "" {
		t.Errorf("read times = %q / %q, want both set", after.ReadAttemptAt, after.ReadOKAt)
	}
}

// (4) A read whose snapshot differs appends the classified row and leaves
// list_fp alone (a nil list fingerprint); a read that differs only in the list
// shape (an unchanged snapshot) appends nothing.
func TestChangedOrphanReadAppendsARowAndNeverTouchesListFP(t *testing.T) {
	e, st, h := newOrphanEngine(t)
	seedOrphan(t, st, "bd-o1")
	seedOrphan(t, st, "bd-o2")
	h.facts = map[string]string{}
	h.facts["bd-o2"] = `{"issue_show":{"state":"open","title":"new"}}`
	e.Lister = fakeLister{res: listing()}
	histBefore := len(historyOf(t, st, "bd-o1"))
	if _, _, err := runTick(t, e); err != nil {
		t.Fatal(err)
	}
	if got := kindsOfRows(t, st, "bd-o2"); len(got) < 2 || !reflect.DeepEqual(got[0], []string{"changed"}) {
		t.Errorf("bd-o2 rows = %v, want the changed row on top", got)
	}
	if got := len(historyOf(t, st, "bd-o1")); got != histBefore {
		t.Errorf("bd-o1 (unchanged snapshot) appended %d rows", got-histBefore)
	}
	for _, id := range []string{"bd-o1", "bd-o2"} {
		if fp := listFPOf(t, st, id); fp != "F1" {
			t.Errorf("%s list_fp = %q, want F1 untouched", id, fp)
		}
		if h.listFPs[id] != nil {
			t.Errorf("%s was read with a list fingerprint %v", id, *h.listFPs[id])
		}
	}
}

// (5) The orphan with the oldest read_attempt_at is read first and a
// never-attempted one before any attempted; ties are by id.
func TestOrphansAreReadOldestAttemptFirst(t *testing.T) {
	e, st, h := newOrphanEngine(t)
	one := 1
	e.Cfg.Hydration.OrphanMaxPerPoll = &one
	for _, id := range []string{"bd-a", "bd-b", "bd-c", "bd-d"} {
		seedOrphan(t, st, id)
	}
	seedAttempt(t, st, "bd-a", "2026-10-01T10:00:02Z")
	seedAttempt(t, st, "bd-b", "2026-10-01T10:00:01.5Z") // older than bd-a only when the times are parsed
	e.Lister = fakeLister{res: listing()}

	var got []string
	for poll := 0; poll < 4; poll++ {
		h.calls = nil
		if _, _, err := runTick(t, e); err != nil {
			t.Fatal(err)
		}
		got = append(got, h.calls...)
	}
	want := []string{"bd-c", "bd-d", "bd-b", "bd-a"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("read order = %v, want %v (never-attempted by id, then oldest attempt)", got, want)
	}
}

func TestSortOrphansComparesParsedTimesNotText(t *testing.T) {
	orphans := []store.Entity{
		{EntityID: "x", ReadAttemptAt: "2026-10-01T10:00:02Z"},
		{EntityID: "y", ReadAttemptAt: "2026-10-01T10:00:01.5Z"},
		{EntityID: "z"},
	}
	sortOrphans(orphans)
	if got := []string{orphans[0].EntityID, orphans[1].EntityID, orphans[2].EntityID}; !reflect.DeepEqual(got, []string{"z", "y", "x"}) {
		t.Errorf("order = %v, want [z y x]", got)
	}
}

// A failed, degraded or not_found read stamps only the attempt time and leaves
// the entity watched and active; a later answering read sets the ok time.
func TestFailedDegradedOrNotFoundOrphanReadStampsOnlyTheAttempt(t *testing.T) {
	for name, setup := range map[string]func(h *orphanHydrator){
		"error":     func(h *orphanHydrator) { h.fail["bd-o1"] = errors.New("connector exploded") },
		"degraded":  func(h *orphanHydrator) { h.degraded["bd-o1"] = "rate_limited" },
		"not found": func(h *orphanHydrator) { h.notFound["bd-o1"] = true },
	} {
		t.Run(name, func(t *testing.T) {
			e, st, h := newOrphanEngine(t)
			seedOrphan(t, st, "bd-o1")
			setup(h)
			e.Lister = fakeLister{res: listing()}
			if _, _, err := runTick(t, e); err != nil {
				t.Fatal(err)
			}
			row := entityOf(t, st, "bd-o1")
			if row.ReadAttemptAt == "" || row.ReadOKAt != "" {
				t.Errorf("read times = %q / %q, want only the attempt", row.ReadAttemptAt, row.ReadOKAt)
			}
			if row.Inactive || row.Watched == nil || !*row.Watched {
				t.Errorf("inactive=%v watched=%v, want active and watched", row.Inactive, row.Watched)
			}
			if n := removedRows(t, st, "bd-o1"); n != 0 {
				t.Errorf("%d removed rows, want none from this selection", n)
			}
		})
	}
}

// An orphan the budget stops is not attempted and nothing is written for it.
func TestOrphanStoppedByTheBudgetIsNotAttempted(t *testing.T) {
	e, st, h := newOrphanEngine(t)
	zeroLeft := 1
	e.Cfg.Hydration.MaxPerPoll = &zeroLeft
	seedOrphan(t, st, "bd-o1")
	seedOrphan(t, st, "bd-o2")
	e.Lister = fakeLister{res: listing()}
	if _, _, err := runTick(t, e); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(h.calls, []string{"bd-o1"}) {
		t.Fatalf("reads = %v, want one (budget 1)", h.calls)
	}
	if entityOf(t, st, "bd-o2").ReadAttemptAt != "" {
		t.Error("the budget-stopped orphan was stamped")
	}
}

// OrphanEntities lists only watched, active entities of the listed path that
// no persisted watch set holds.
func TestOrphanEntitiesSelection(t *testing.T) {
	st := store.OpenNewSchemaForTest(t)
	cfg := &config.Config{Repos: []config.RepoConfig{{Remote: "o/r"}}}
	cfg.Watch.Issue.Queries = []string{"open"}
	cfg.Watch.Issue.Backends = map[string]config.WatchBackend{"jira-q": {Instance: "jira", OwnsChanges: true}}
	cfg.Watch.Issue.Queries = append(cfg.Watch.Issue.Queries, "jira-q")

	seed := func(id string, active bool, watched *bool, backend string) {
		seedRow(t, st, id, "F1", "2026-10-01T11:00:00Z", active)
		var b *string
		if backend != "" {
			b = &backend
		}
		if err := st.SetEntityColumns("o/r", "issue", id, b, watched); err != nil {
			t.Fatal(err)
		}
	}
	yes, no := true, false
	seed("orphan", true, &yes, "")
	seed("held", true, &yes, "")
	seed("unwatched-null", true, nil, "")
	seed("unwatched-false", true, &no, "")
	seed("inactive", false, &yes, "")
	seed("owned", true, &yes, "jira")
	seed("other-backend", true, &yes, "beads-x")
	if err := saveWatchSet(st, "issue", "open", map[string]bool{"held": true}); err != nil {
		t.Fatal(err)
	}

	got, err := OrphanEntities(st, cfg)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, e := range got {
		ids = append(ids, e.EntityID)
	}
	if !sameStrings(ids, []string{"orphan", "other-backend"}) {
		t.Errorf("orphans = %v, want [orphan other-backend]", ids)
	}
}

func sameStrings(a, b []string) bool {
	x, y := idSet(a), idSet(b)
	return sameSet(x, y) && len(a) == len(b)
}
