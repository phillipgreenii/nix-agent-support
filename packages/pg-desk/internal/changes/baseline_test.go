package changes

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// watchedOf reads an issue entity's watched column; found=false when the row
// is absent.
func watchedOf(t *testing.T, st *store.Store, id string) (watched, found bool) {
	t.Helper()
	row, ok, err := st.GetEntity("o/r", "issue", id)
	if err != nil {
		t.Fatal(err)
	}
	return row.Watched != nil && *row.Watched, ok
}

func markerDone(t *testing.T, st *store.Store, query string) bool {
	t.Helper()
	b, err := ReadBaseline(st, query)
	if err != nil {
		t.Fatal(err)
	}
	return b.Done
}

// (1) A query's first complete run over N members appends no row, marks all N
// watched and writes the marker.
func TestBaselineFirstRunIsSilentMarksAllAndWritesMarker(t *testing.T) {
	e, st, h := newUnbaselinedEngine(t, "open")
	e.Lister = fakeLister{res: listing("bd-1=F1", "bd-2=F2", "bd-3=F3")}
	env, _, err := runTick(t, e)
	if err != nil {
		t.Fatal(err)
	}
	if len(env.Records) != 0 {
		t.Fatalf("baseline appended records: %+v", env.Records)
	}
	for _, id := range []string{"bd-1", "bd-2", "bd-3"} {
		if w, found := watchedOf(t, st, id); !found || !w {
			t.Errorf("%s: found=%v watched=%v, want held and watched", id, found, w)
		}
		if got := listFPOf(t, st, id); got == "" {
			t.Errorf("%s: list_fp not stored", id)
		}
		if h.origins[id] != OriginPGConnector {
			t.Errorf("%s hydrated with origin %q", id, h.origins[id])
		}
	}
	b, err := ReadBaseline(st, "open")
	if err != nil {
		t.Fatal(err)
	}
	if !b.Done || b.At.IsZero() || !b.HasOpenSince || b.OpenSince.IsZero() {
		t.Errorf("ReadBaseline = %+v, want Done with At and OpenSince", b)
	}
}

// (2) A query added later runs a baseline of its own: zero rows for its
// members, while an already baselined query keeps working normally.
func TestBaselineQueryAddedLaterIsSilent(t *testing.T) {
	e, st, _ := newListEngine(t, "open")
	e.Lister = queryLister{
		"open": {res: listing("bd-1=F1")},
		"late": {res: listing("bd-9=F9", "bd-10=F10")},
	}
	first, _, err := runTick(t, e)
	if err != nil || len(first.Records) != 1 { // bd-1 is a first observation of a baselined query
		t.Fatalf("first tick = %+v, %v", first, err)
	}
	e.Cfg.Watch.Issue.Queries = []string{"open", "late"}
	env, _, err := runTick(t, e)
	if err != nil {
		t.Fatal(err)
	}
	if n := hydrationRows(env); n != 0 {
		t.Fatalf("records = %+v, want no hydration row for the late query", env.Records)
	}
	for _, id := range []string{"bd-9", "bd-10"} {
		if w, found := watchedOf(t, st, id); !found || !w {
			t.Errorf("%s: found=%v watched=%v", id, found, w)
		}
	}
	if !markerDone(t, st, "late") {
		t.Error("the added query has no marker")
	}
}

// (3) A baseline of more members than hydration.max_per_poll stays silent
// across polls; the marker is written only in the poll that hydrated the last.
func TestBaselineAcrossPollsWritesMarkerOnlyWhenLastMemberHydrated(t *testing.T) {
	e, st, h := newUnbaselinedEngine(t, "open")
	two := 2
	e.Cfg.Hydration.MaxPerPoll = &two
	e.Lister = fakeLister{res: listing("bd-1=F1", "bd-2=F2", "bd-3=F3", "bd-4=F4", "bd-5=F5")}
	wantCalls := []int{2, 4, 5}
	for poll, want := range wantCalls {
		env, out, err := runTick(t, e)
		if err != nil {
			t.Fatalf("poll %d: %v", poll+1, err)
		}
		if len(env.Records) != 0 {
			t.Fatalf("poll %d appended %+v", poll+1, env.Records)
		}
		if len(h.calls) != want {
			t.Fatalf("poll %d: %d hydrations in total, want %d (%v)", poll+1, len(h.calls), want, h.calls)
		}
		last := poll == len(wantCalls)-1
		if markerDone(t, st, "open") != last {
			t.Fatalf("poll %d: marker done = %v, want %v", poll+1, !last, last)
		}
		if out.Partial == last {
			t.Errorf("poll %d: partial = %v", poll+1, out.Partial)
		}
	}
	for i := 1; i <= 5; i++ {
		if w, _ := watchedOf(t, st, fmt.Sprintf("bd-%d", i)); !w {
			t.Errorf("bd-%d is not watched", i)
		}
	}
}

// (4) A held member whose list_fp differs at baseline is hydrated in the same
// poll with the no-row write.
func TestBaselineHeldMemberWithDifferingFPHydratesWithNoRow(t *testing.T) {
	e, st, h := newUnbaselinedEngine(t, "open")
	seedRow(t, st, "bd-1", "OLD", "2026-10-01T11:00:00Z", true)
	seedRow(t, st, "bd-2", "F2", "2026-10-01T11:00:00Z", true)
	e.Lister = fakeLister{res: listing("bd-1=NEW", "bd-2=F2")}
	env, _, err := runTick(t, e)
	if err != nil {
		t.Fatal(err)
	}
	if len(h.calls) != 1 || h.calls[0] != "bd-1" {
		t.Fatalf("hydrations = %v, want only the differing bd-1", h.calls)
	}
	if got := listFPOf(t, st, "bd-1"); got != "NEW" {
		t.Errorf("bd-1 list_fp = %q", got)
	}
	if n := hydrationRows(env); n != 0 {
		t.Errorf("records = %+v, want no hydration row", env.Records)
	}
	for _, id := range []string{"bd-1", "bd-2"} {
		if w, _ := watchedOf(t, st, id); !w {
			t.Errorf("%s is not watched", id)
		}
	}
	if !markerDone(t, st, "open") {
		t.Error("marker not written")
	}
}

// (5) A member whose hydration always fails keeps the marker unwritten and is
// retried at every poll; the others are still held and watched.
func TestBaselineFailingMemberKeepsMarkerUnwrittenAndIsRetried(t *testing.T) {
	e, st, h := newUnbaselinedEngine(t, "open")
	e.Lister = fakeLister{res: listing("bd-1=F1", "bd-2=F2")}
	h.fail["bd-2"] = errors.New("boom")
	for poll := 1; poll <= 4; poll++ {
		before := len(h.calls)
		env, out, err := runTick(t, e)
		if err != nil {
			t.Fatal(err)
		}
		if len(env.Records) != 0 || !out.Partial {
			t.Fatalf("poll %d: records=%v partial=%v", poll, env.Records, out.Partial)
		}
		if markerDone(t, st, "open") {
			t.Fatalf("poll %d: marker written with a failing member", poll)
		}
		retried := 0
		for _, id := range h.calls[before:] {
			if id == "bd-2" {
				retried++
			}
		}
		if retried != 1 {
			t.Fatalf("poll %d: bd-2 hydrated %d times, want once", poll, retried)
		}
	}
	if w, _ := watchedOf(t, st, "bd-1"); !w {
		t.Error("the healthy member is not watched")
	}
	delete(h.fail, "bd-2")
	if _, _, err := runTick(t, e); err != nil {
		t.Fatal(err)
	}
	if !markerDone(t, st, "open") {
		t.Error("marker not written once the member hydrates")
	}
}

// A degraded hydration is a failure of the member too.
func TestBaselineDegradedMemberKeepsMarkerUnwritten(t *testing.T) {
	e, st, _ := newUnbaselinedEngine(t, "open")
	e.Hydrator = degradedHydrator{}
	e.Lister = fakeLister{res: listing("bd-1=F1")}
	if _, out, err := runTick(t, e); err != nil || !out.Partial {
		t.Fatalf("tick = %+v, %v", out, err)
	}
	if markerDone(t, st, "open") {
		t.Error("marker written with a degraded member")
	}
}

// (6) Deleting the marker (a moved-aside store) makes the next poll a silent
// baseline run again; the open key is not rewritten.
func TestBaselineDeletedMarkerRunsASilentBaselineAgain(t *testing.T) {
	e, st, _ := newUnbaselinedEngine(t, "open")
	e.Lister = fakeLister{res: listing("bd-1=F1")}
	if _, _, err := runTick(t, e); err != nil {
		t.Fatal(err)
	}
	first, _ := ReadBaseline(st, "open")
	if err := st.DeleteMeta(MetaBaselinePrefix + "open"); err != nil {
		t.Fatal(err)
	}
	e.Lister = fakeLister{res: listing("bd-1=F1", "bd-2=F2")}
	e.Now = func() time.Time { return time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC) }
	env, _, err := runTick(t, e)
	if err != nil {
		t.Fatal(err)
	}
	if n := hydrationRows(env); n != 0 {
		t.Fatalf("re-baseline appended %+v", env.Records)
	}
	if w, found := watchedOf(t, st, "bd-2"); !found || !w {
		t.Errorf("bd-2 found=%v watched=%v", found, w)
	}
	b, _ := ReadBaseline(st, "open")
	if !b.Done || !b.At.After(first.At) {
		t.Errorf("marker not rewritten: %+v then %+v", first, b)
	}
	if !b.OpenSince.Equal(first.OpenSince) {
		t.Errorf("open key rewritten: %v then %v", first.OpenSince, b.OpenSince)
	}
}

// (7) A member present at the baseline never appends added at a later poll,
// while a member that first appears after the marker does hydrate (with a row).
func TestBaselineMemberNeverAppendsAddedAtALaterPoll(t *testing.T) {
	e, st, h := newUnbaselinedEngine(t, "open")
	e.Lister = fakeLister{res: listing("bd-1=F1")}
	if _, _, err := runTick(t, e); err != nil {
		t.Fatal(err)
	}
	h.calls = nil
	env, _, err := runTick(t, e)
	if err != nil {
		t.Fatal(err)
	}
	if len(env.Records) != 0 || len(h.calls) != 0 {
		t.Fatalf("second poll: records=%v hydrations=%v", env.Records, h.calls)
	}
	e.Lister = fakeLister{res: listing("bd-1=F1", "bd-2=F2")}
	env, _, err = runTick(t, e)
	if err != nil {
		t.Fatal(err)
	}
	if len(h.calls) != 1 || h.calls[0] != "bd-2" || len(env.Records) != 1 || env.Records[0].ID != "bd-2" {
		t.Fatalf("third poll: hydrations=%v records=%+v, want one row for the new bd-2", h.calls, env.Records)
	}
	_ = st
}

// A member listed by a query in baseline and by an already baselined query is
// owned by the baseline this poll: it is hydrated silently.
func TestBaselineMemberAlsoListedByABaselinedQueryStaysSilent(t *testing.T) {
	e, st, _ := newUnbaselinedEngine(t, "open", "late")
	if err := st.SetMeta(MetaBaselinePrefix+"open", "2026-09-30T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	e.Lister = queryLister{
		"open": {res: listing("bd-1=F1")},
		"late": {res: listing("bd-1=F1")},
	}
	env, _, err := runTick(t, e)
	if err != nil {
		t.Fatal(err)
	}
	if len(env.Records) != 0 {
		t.Fatalf("records = %+v, want none", env.Records)
	}
	if w, _ := watchedOf(t, st, "bd-1"); !w {
		t.Error("bd-1 is not watched")
	}
}

// A listing that is not complete may still mark and silently hydrate what it
// listed but withholds the marker.
func TestBaselineTruncatedListingWithholdsMarker(t *testing.T) {
	e, st, _ := newUnbaselinedEngine(t, "open")
	res := listing("bd-1=F1")
	res.Truncated = true
	e.Lister = fakeLister{res: res}
	env, _, err := runTick(t, e)
	if err != nil {
		t.Fatal(err)
	}
	if len(env.Records) != 0 {
		t.Fatalf("records = %+v", env.Records)
	}
	if w, _ := watchedOf(t, st, "bd-1"); !w {
		t.Error("bd-1 is not watched")
	}
	if markerDone(t, st, "open") {
		t.Error("marker written for a truncated listing")
	}
	e.Lister = fakeLister{res: listing("bd-1=F1")}
	if _, _, err := runTick(t, e); err != nil {
		t.Fatal(err)
	}
	if !markerDone(t, st, "open") {
		t.Error("marker not written by the next complete listing")
	}
}

// A failed listing does nothing: no keys, no hydration.
func TestBaselineFailedListingWritesNothing(t *testing.T) {
	e, st, h := newUnbaselinedEngine(t, "open", "other")
	e.Lister = queryLister{
		"open":  {res: listing("bd-1=F1")},
		"other": {err: errors.New("down")},
	}
	if _, _, err := runTick(t, e); err != nil {
		t.Fatal(err)
	}
	b, _ := ReadBaseline(st, "other")
	if b.Done || b.HasOpenSince {
		t.Errorf("failed query has baseline state %+v", b)
	}
	if len(h.calls) != 1 {
		t.Errorf("hydrations = %v", h.calls)
	}
}

func TestReadBaselineAbsentAndUnparseable(t *testing.T) {
	st := store.OpenNewSchemaForTest(t)
	b, err := ReadBaseline(st, "q")
	if err != nil || b.Done || b.HasOpenSince || !b.At.IsZero() || !b.OpenSince.IsZero() {
		t.Fatalf("absent = %+v, %v", b, err)
	}
	if err := st.SetMeta(MetaBaselinePrefix+"q", "not a time"); err != nil {
		t.Fatal(err)
	}
	if b, _ := ReadBaseline(st, "q"); b.Done {
		t.Errorf("unparseable marker read as done: %+v", b)
	}
	if err := st.SetMeta(MetaBaselineOpenPrefix+"q", "2026-10-01T10:00:00Z"); err != nil {
		t.Fatal(err)
	}
	b, _ = ReadBaseline(st, "q")
	if !b.HasOpenSince || b.Done || b.OpenSince.Hour() != 10 {
		t.Errorf("open only = %+v", b)
	}
}

// hydrationRows counts the records a pg-connector hydration appended. The
// local reconcile tier (origin local-reconcile) re-emits for entities whose
// last row is old, which is not what a baseline run is about.
func hydrationRows(env Envelope) int {
	n := 0
	for _, r := range env.Records {
		if r.Origin == OriginPGConnector {
			n++
		}
	}
	return n
}
