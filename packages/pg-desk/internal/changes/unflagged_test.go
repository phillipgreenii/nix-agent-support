package changes

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/gather"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// fakeMembers answers ListQueryMembers per query and BaselineOpenAges from a
// fixed map, and records the queries it was asked to list.
type fakeMembers struct {
	mu      sync.Mutex
	lists   map[string]gather.MemberList
	listErr map[string]error
	ages    map[string]int64
	agesErr error
	asked   []string
}

func (f *fakeMembers) ListQueryMembers(_ context.Context, query, backend string) (gather.MemberList, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, query+"@"+backend)
	if err := f.listErr[query]; err != nil {
		return gather.MemberList{}, err
	}
	return f.lists[query], nil
}

func (f *fakeMembers) BaselineOpenAges(context.Context, string, string) (map[string]int64, error) {
	return f.ages, f.agesErr
}

func members(ids ...string) gather.MemberList {
	var l gather.MemberList
	for _, id := range ids {
		// The element's own entity_meta.watched is ignored: state it true to prove it.
		tr := true
		l.Members = append(l.Members, gather.MemberRef{ID: id, Watched: &tr})
	}
	return l
}

// seedHeld stores one held issue entity of feedInst with the given watched flag
// (nil = NULL).
func seedHeld(t *testing.T, st *store.Store, id string, backend string, watched *bool) {
	t.Helper()
	b := backend
	opts := store.WriteOptions{Backend: &b, Watched: watched}
	if _, err := st.WriteEntityStateWithLogOpts(
		store.Entity{Repo: "o/r", EntityType: "issue", EntityID: id, Facts: `{}`, AsOf: "x"},
		0, "2026-10-01T11:59:00Z", true, nil, []string{"added"}, "pg-connector", "2026-10-01T11:59:00Z", opts,
	); err != nil {
		t.Fatal(err)
	}
}

func boolp(b bool) *bool { return &b }

func unflaggedCount(t *testing.T, st *store.Store, inst string) (UnflaggedRecord, bool) {
	t.Helper()
	rec, found, err := ReadUnflagged(st, inst)
	if err != nil {
		t.Fatal(err)
	}
	return rec, found
}

func newUnflaggedFixture(t *testing.T, m *fakeMembers, queries ...string) *feedFixture {
	t.Helper()
	f := newFeedFixture(t, queries, nil, feedAnswer(feedIdentity, 1))
	f.eng.Members = m
	return f
}

func TestUnflaggedCountsHeldMembersWithoutAFlagOnceAndStoresOnlyCountAndTime(t *testing.T) {
	m := &fakeMembers{lists: map[string]gather.MemberList{
		"mine": members("J-1", "J-2", "J-3", "J-9"),
		"team": members("J-2", "J-4"),
	}}
	f := newUnflaggedFixture(t, m, "mine", "team")
	seedHeld(t, f.st, "J-1", feedInst, boolp(true))  // flagged
	seedHeld(t, f.st, "J-2", feedInst, boolp(false)) // not flagged, listed by two queries: counts once
	seedHeld(t, f.st, "J-3", feedInst, nil)          // NULL flag counts
	seedHeld(t, f.st, "J-4", "other-inst", nil)      // another backend's entity: not this instance's
	// J-9 is a member pg-desk does not hold: never counted.
	if _, out, err := f.run("router"); err != nil || out.Partial {
		t.Fatalf("run = %+v, %v", out, err)
	}
	rec, found := unflaggedCount(t, f.st, feedInst)
	if !found || rec.Count != 2 || rec.At != "2026-10-01T12:00:00Z" {
		t.Fatalf("record = %+v found=%v, want count 2 at the poll time", rec, found)
	}
	keys, err := f.st.ListMetaPrefix("change_flow.")
	if err != nil {
		t.Fatal(err)
	}
	for k := range keys {
		if strings.HasPrefix(k, "change_flow.watchset.") {
			t.Errorf("a member set was stored: %s", k)
		}
	}
	if !reflect.DeepEqual(m.asked, []string{"mine@" + feedInst, "team@" + feedInst}) {
		t.Errorf("lists = %v", m.asked)
	}
}

func TestUnflaggedSkipsQueryWithBaselineOpenUnderADayAndCountsAfter(t *testing.T) {
	m := &fakeMembers{
		lists: map[string]gather.MemberList{"mine": members("J-1"), "team": members("J-2")},
		ages:  map[string]int64{"team": 86399},
	}
	f := newUnflaggedFixture(t, m, "mine", "team")
	seedHeld(t, f.st, "J-1", feedInst, nil)
	seedHeld(t, f.st, "J-2", feedInst, nil)
	if _, _, err := f.run("router"); err != nil {
		t.Fatal(err)
	}
	if rec, _ := unflaggedCount(t, f.st, feedInst); rec.Count != 1 {
		t.Errorf("a baseline open 86399 s is excluded: count = %d, want 1", rec.Count)
	}
	if !reflect.DeepEqual(m.asked, []string{"mine@" + feedInst}) {
		t.Errorf("the excluded query must not be listed: %v", m.asked)
	}
	m.ages["team"] = 86400
	if _, _, err := f.run("router"); err != nil {
		t.Fatal(err)
	}
	if rec, _ := unflaggedCount(t, f.st, feedInst); rec.Count != 2 {
		t.Errorf("a baseline open a full day counts: count = %d, want 2", rec.Count)
	}
}

func TestUnflaggedFailedListKeepsThePreviousCountAndDegradesThePoll(t *testing.T) {
	m := &fakeMembers{lists: map[string]gather.MemberList{"mine": members("J-1")}}
	f := newUnflaggedFixture(t, m, "mine")
	seedHeld(t, f.st, "J-1", feedInst, nil)
	var warn strings.Builder
	f.eng.Warn = &warn
	if _, out, err := f.run("router"); err != nil || out.Partial {
		t.Fatalf("first run = %+v, %v", out, err)
	}
	m.listErr = map[string]error{"mine": errors.New("boom")}
	seedHeld(t, f.st, "J-5", feedInst, nil)
	m.lists["mine"] = members("J-1", "J-5")
	_, out, err := f.run("router")
	if err != nil {
		t.Fatal(err)
	}
	if !out.Partial {
		t.Error("a failed list read must make the poll partial")
	}
	if !strings.Contains(warn.String(), "unflagged read degraded: mine") {
		t.Errorf("warning missing: %q", warn.String())
	}
	if rec, _ := unflaggedCount(t, f.st, feedInst); rec.Count != 1 {
		t.Errorf("the previous count must be kept: %d, want 1", rec.Count)
	}
}

func TestUnflaggedTruncatedListAndFailedBaselineKeepTheCount(t *testing.T) {
	tr := members("J-1")
	tr.Truncated = true
	m := &fakeMembers{lists: map[string]gather.MemberList{"mine": tr}}
	f := newUnflaggedFixture(t, m, "mine")
	seedHeld(t, f.st, "J-1", feedInst, nil)
	if _, _, err := f.run("router"); err != nil {
		t.Fatal(err)
	}
	if _, found := unflaggedCount(t, f.st, feedInst); found {
		t.Error("a truncated first poll must store nothing")
	}
	m.lists["mine"] = members("J-1")
	m.agesErr = errors.New("health down")
	_, out, err := f.run("router")
	if err != nil || !out.Partial {
		t.Fatalf("a failed baseline read: out %+v err %v, want partial", out, err)
	}
	if _, found := unflaggedCount(t, f.st, feedInst); found {
		t.Error("a failed baseline read must store nothing")
	}
}

func TestUnflaggedIsSkippedWithoutAMemberReaderOrAFailedFeed(t *testing.T) {
	m := &fakeMembers{lists: map[string]gather.MemberList{"mine": members("J-1")}}
	f := newUnflaggedFixture(t, m, "mine")
	f.eng.Members = nil
	if _, _, err := f.run("router"); err != nil {
		t.Fatal(err)
	}
	if _, found := unflaggedCount(t, f.st, feedInst); found {
		t.Error("no reader: nothing may be stored")
	}
	f.eng.Members = m
	f.feeder.listErr = errors.New("feed down")
	_, _, _ = f.run("router")
	if len(m.asked) != 0 {
		t.Errorf("a failed feed must not list members: %v", m.asked)
	}
}

// newListedUnflaggedEngine is an issue engine whose queries are all served by
// the beads instance "beads-a" (a non-owning backends entry).
func newListedUnflaggedEngine(t *testing.T, queries ...string) (*Engine, *store.Store) {
	t.Helper()
	e, st, _ := newListEngine(t, queries...)
	e.Cfg.Watch.Issue.Backends = map[string]config.WatchBackend{}
	for _, q := range queries {
		e.Cfg.Watch.Issue.Backends[q] = config.WatchBackend{Instance: "beads-a"}
	}
	return e, st
}

func seedListedSet(t *testing.T, st *store.Store, query string, ids ...string) {
	t.Helper()
	set := map[string]bool{}
	for _, id := range ids {
		set[id] = true
	}
	if err := saveWatchSet(st, "issue", query, set); err != nil {
		t.Fatal(err)
	}
}

func recordListed(t *testing.T, e *Engine, queries ...string) *pollState {
	t.Helper()
	st := &pollState{hydrated: map[string]error{}, notes: map[string][]string{}}
	e.recordListedUnflagged("issue", queries, st)
	return st
}

// Three members of the last complete listing without a watched flag count (a
// NULL flag and a NULL backend included, one listed by two queries once); a
// flagged member, an inactive one and one pg-desk does not hold do not.
func TestListedUnflaggedCountsMembersOfTheLastListingWithoutAFlag(t *testing.T) {
	e, st := newListedUnflaggedEngine(t, "open", "mine")
	seedListedSet(t, st, "open", "bd-1", "bd-2", "bd-3", "bd-4", "bd-5", "bd-gone")
	seedListedSet(t, st, "mine", "bd-1")
	seedHeld(t, st, "bd-1", "beads-a", boolp(false)) // listed by two queries: once
	seedHeld(t, st, "bd-2", "beads-a", nil)
	seedRow(t, st, "bd-3", "F", "2026-10-01T11:00:00Z", true) // NULL backend still matched by membership
	seedHeld(t, st, "bd-4", "beads-a", boolp(true))           // flagged
	seedRow(t, st, "bd-5", "F", "2026-10-01T11:00:00Z", false)
	seedHeld(t, st, "bd-out", "beads-a", nil) // held but in no listing
	if sp := recordListed(t, e, "open", "mine"); len(sp.side) != 0 {
		t.Fatalf("side = %v", sp.side)
	}
	rec, found := unflaggedCount(t, st, "beads-a")
	if !found || rec.Count != 3 || rec.At != "2026-10-01T12:00:00Z" {
		t.Fatalf("record = %+v found=%v, want count 3 at the poll time", rec, found)
	}
	// Flagging the members clears the count at the next read.
	for _, id := range []string{"bd-1", "bd-2", "bd-3"} {
		if err := st.SetEntityColumns("o/r", "issue", id, nil, boolp(true)); err != nil {
			t.Fatal(err)
		}
	}
	recordListed(t, e, "open", "mine")
	if rec, _ := unflaggedCount(t, st, "beads-a"); rec.Count != 0 {
		t.Errorf("after flagging, count = %d, want 0", rec.Count)
	}
}

// A query whose baseline has been open under one day and has no marker is
// excluded; once open for over a day its members count; a marker ends the hold.
func TestListedUnflaggedExcludesABaselineOpenUnderADay(t *testing.T) {
	e, st := newListedUnflaggedEngine(t, "fresh", "old")
	seedListedSet(t, st, "fresh", "bd-1", "bd-2")
	seedListedSet(t, st, "old", "bd-3")
	for _, id := range []string{"bd-1", "bd-2", "bd-3"} {
		seedHeld(t, st, id, "beads-a", nil)
	}
	set := func(k, v string) {
		t.Helper()
		if err := st.SetMeta(k, v); err != nil {
			t.Fatal(err)
		}
	}
	for _, q := range []string{"fresh", "old"} { // newListEngine wrote markers
		if err := st.DeleteMeta(MetaBaselinePrefix + q); err != nil {
			t.Fatal(err)
		}
	}
	set(MetaBaselineOpenPrefix+"fresh", "2026-10-01T11:00:00Z") // one hour old
	set(MetaBaselineOpenPrefix+"old", "2026-09-29T11:00:00Z")   // three days old
	recordListed(t, e, "fresh", "old")
	if rec, _ := unflaggedCount(t, st, "beads-a"); rec.Count != 1 {
		t.Fatalf("count = %d, want 1 (only the old baseline's member)", rec.Count)
	}
	set(MetaBaselineOpenPrefix+"fresh", "2026-09-30T11:00:00Z") // 25 hours old
	recordListed(t, e, "fresh", "old")
	if rec, _ := unflaggedCount(t, st, "beads-a"); rec.Count != 3 {
		t.Fatalf("count = %d, want 3 once the baseline has been open over a day", rec.Count)
	}
	// A baseline begun but with no readable open time stays excluded; a marker
	// ends the hold whatever the open time was.
	if err := st.DeleteMeta(MetaBaselineOpenPrefix + "fresh"); err != nil {
		t.Fatal(err)
	}
	recordListed(t, e, "fresh", "old")
	if rec, _ := unflaggedCount(t, st, "beads-a"); rec.Count != 1 {
		t.Fatalf("count = %d, want 1 with no open time", rec.Count)
	}
	set(MetaBaselinePrefix+"fresh", "2026-10-01T11:30:00Z")
	recordListed(t, e, "fresh", "old")
	if rec, _ := unflaggedCount(t, st, "beads-a"); rec.Count != 3 {
		t.Fatalf("count = %d, want 3 with the marker written", rec.Count)
	}
}

// An instance is read only when this call consulted all of its queries, and a
// non-owning query without a backends entry names no instance.
func TestListedUnflaggedNeedsEveryQueryOfTheInstanceAndABackendsEntry(t *testing.T) {
	e, st := newListedUnflaggedEngine(t, "open", "mine")
	seedListedSet(t, st, "open", "bd-1")
	seedHeld(t, st, "bd-1", "beads-a", nil)
	recordListed(t, e, "open") // --query open skipped "mine"
	if _, found := unflaggedCount(t, st, "beads-a"); found {
		t.Error("a partial call stored a count")
	}
	recordListed(t, e, "open", "mine")
	if rec, found := unflaggedCount(t, st, "beads-a"); !found || rec.Count != 1 {
		t.Errorf("record = %+v found=%v, want 1", rec, found)
	}
	e2, st2, _ := newListEngine(t, "open")
	seedListedSet(t, st2, "open", "bd-1")
	seedHeld(t, st2, "bd-1", "beads-a", nil)
	recordListed(t, e2, "open")
	if keys, _ := st2.ListMetaPrefix(MetaUnflaggedPrefix); len(keys) != 0 {
		t.Errorf("a query with no backends entry stored %v", keys)
	}
}

// Run stores the count once a poll has hydrated and flagged the baseline's
// members (zero), through the real poll.
func TestRunStoresTheListedUnflaggedCount(t *testing.T) {
	e, st, _ := newUnbaselinedEngine(t, "open")
	e.Cfg.Watch.Issue.Backends = map[string]config.WatchBackend{"open": {Instance: "beads-a"}}
	e.Lister = fakeLister{res: listing("bd-1=F1", "bd-2=F2")}
	if _, _, err := runTick(t, e); err != nil {
		t.Fatal(err)
	}
	if rec, found := unflaggedCount(t, st, "beads-a"); !found || rec.Count != 0 {
		t.Fatalf("record = %+v found=%v, want a stored zero", rec, found)
	}
	// A member whose flag is lost afterwards is counted by the next poll.
	if err := st.SetEntityColumns("o/r", "issue", "bd-1", nil, boolp(false)); err != nil {
		t.Fatal(err)
	}
	e.Lister = fakeLister{res: listing("bd-1=F1", "bd-2=F2")}
	if _, _, err := runTick(t, e); err != nil {
		t.Fatal(err)
	}
	if rec, _ := unflaggedCount(t, st, "beads-a"); rec.Count != 1 {
		t.Errorf("count = %d, want 1", rec.Count)
	}
}
