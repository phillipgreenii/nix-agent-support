package changes

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

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
