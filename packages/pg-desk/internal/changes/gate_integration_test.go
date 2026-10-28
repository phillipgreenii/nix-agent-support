package changes

import (
	"context"
	"reflect"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/gather"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/pipeline"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// The delivery gate at engine level (INV-ICD-16; design T-20 and T-2): the
// PRODUCTION gate, installed through pipeline.InstallIssueGate on the store the
// engine reads, over a real SQLite store. The scope is every issue entity, so
// the entities here carry no backend column at all.

const gateAt = "2026-10-01T11:30:00Z"

// gateEngine is a baselined engine over the query "open" with the production
// gate installed AFTER the seed rows are written (so seeds are rows "already in
// change_log when the gate lands").
func gateEngine(t *testing.T, seed ...string) (*Engine, *store.Store, *storeHydrator) {
	t.Helper()
	e, st, h := newListEngine(t, "open")
	h.classified = true
	h.kindsOf = map[string][]string{}
	for _, id := range seed {
		// A fixed kind, so every hydration of a seeded issue wants a row and the
		// gate is the only thing that can withhold it.
		h.kindsOf[id] = []string{"fields_changed"}
		seedRow(t, st, id, "F1", "2026-10-01T11:00:00Z", true)
	}
	pipeline.InstallIssueGate(st, nil)
	e.Lister = fakeLister{res: listing()}
	return e, st, h
}

func watch(t *testing.T, st *store.Store, id string, on bool) {
	t.Helper()
	if err := st.SetEntityColumns("o/r", "issue", id, nil, &on); err != nil {
		t.Fatal(err)
	}
}

// drain consumes everything delivered so far, so a later tick shows only what
// the operation under test appended.
func drain(t *testing.T, e *Engine) {
	t.Helper()
	if _, _, err := runTick(t, e); err != nil {
		t.Fatal(err)
	}
}

func deliveredFor(env Envelope, id string) []Record {
	var out []Record
	for _, r := range env.Records {
		if r.ID == id {
			out = append(out, r)
		}
	}
	return out
}

// ownOperations runs every append cause of an issue entity once against st:
// a refresh-style hydration, link and work propagation, an annotation write
// and a --reset replay (which hydrates only watched issues, so for an unwatched
// one it is the engine that must append nothing).
func ownOperations(t *testing.T, e *Engine, st *store.Store, id string) (annotSeq int64, annotChanged bool) {
	t.Helper()
	// Refresh hydration (origin pg-desk) through the engine's hydrator.
	if _, err := e.Hydrator.RunEntityChange(context.Background(), "issue", id, gather.ChangeChanged,
		pipeline.EntityChangeOptions{Origin: "pg-desk"}); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"link_changed", "work_changed"} {
		if _, err := st.AppendEntityChange("o/r", "issue", id, []string{kind}, "pg-desk", gateAt); err != nil {
			t.Fatal(err)
		}
	}
	seq, changed, err := st.SetAnnotationSeq(store.KVAnnotation{
		Repo: "o/r", EntityType: "issue", EntityID: id, Key: "wip", Value: "true",
		Origin: "pg-desk", SetBy: "op", SetAt: gateAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := runTick(t, e, Options{EntityType: "issue", Consumer: "other", Reset: true}); err != nil {
		t.Fatal(err)
	}
	return seq, changed
}

// T-20, unwatched: refresh, link and work propagation, an annotation write and
// a --reset replay append no row and deliver no event, while the annotation
// itself is written and still reports changed.
func TestGateUnwatchedIssueAppendsNothingForAnyCause(t *testing.T) {
	e, st, _ := gateEngine(t, "bd-u")
	watch(t, st, "bd-u", false)
	drain(t, e)
	before := len(changeRows(t, st, "bd-u"))
	versionBefore := entityOf(t, st, "bd-u").Version

	seq, changed := ownOperations(t, e, st, "bd-u")

	if seq != 0 || !changed {
		t.Errorf("annotation = (%d, %v), want (0, true): written, no row", seq, changed)
	}
	if _, found, err := st.GetKVAnnotation("o/r", "issue", "bd-u", "wip"); err != nil || !found {
		t.Errorf("annotation not written: found=%v err=%v", found, err)
	}
	if got := len(changeRows(t, st, "bd-u")); got != before {
		t.Errorf("rows = %d, want %d: an unwatched issue appended a row", got, before)
	}
	if v := entityOf(t, st, "bd-u").Version; v <= versionBefore {
		t.Errorf("version = %d, want it bumped past %d: the snapshot write itself proceeds", v, versionBefore)
	}
	env, _, err := runTick(t, e)
	if err != nil {
		t.Fatal(err)
	}
	if got := deliveredFor(env, "bd-u"); len(got) != 0 {
		t.Errorf("delivered %+v for an unwatched issue, want no event", got)
	}
}

// T-20, watched: the same operations append and deliver rows. The entity has
// no backend column, so the gate is not scoped by backend.
func TestGateWatchedIssueAppendsForEveryCause(t *testing.T) {
	e, st, _ := gateEngine(t, "bd-w")
	watch(t, st, "bd-w", true)
	drain(t, e)
	before := len(changeRows(t, st, "bd-w"))

	seq, changed := ownOperations(t, e, st, "bd-w")

	if seq == 0 || !changed {
		t.Errorf("annotation = (%d, %v), want (>0, true)", seq, changed)
	}
	// refresh + link + work + annotation + the reset replay's row (the poll's
	// own orphan read of this watched issue appends at origin pg-connector and
	// is not one of the operations under test).
	own := func(rows []store.ChangeRecord) (n int) {
		for _, r := range rows {
			if r.Origin != OriginPGConnector {
				n++
			}
		}
		return n
	}
	if got := own(changeRows(t, st, "bd-w")); got != own(changeRows(t, st, "bd-w")[:before])+5 {
		t.Errorf("own rows = %d, want %d", got, own(changeRows(t, st, "bd-w")[:before])+5)
	}
	env, _, err := runTick(t, e)
	if err != nil {
		t.Fatal(err)
	}
	delivered := 0
	for _, r := range deliveredFor(env, "bd-w") {
		if r.Origin != OriginPGConnector {
			delivered++
		}
	}
	if delivered != 5 {
		t.Errorf("delivered %d own records for a watched issue, want 5: %+v", delivered, deliveredFor(env, "bd-w"))
	}
}

// T-2: an issue that is only tracked delivers no row; a member of a watched
// query delivers its first row; a PR change linked to an unwatched issue yields
// no issue row (T-18).
func TestGateTrackedIssueDeliversNothingMemberDelivers(t *testing.T) {
	e, st, h := gateEngine(t, "bd-t")
	watch(t, st, "bd-t", false)
	drain(t, e)
	e.Lister = fakeLister{res: listing("bd-m=F1")}

	env, _, err := runTick(t, e)
	if err != nil {
		t.Fatal(err)
	}
	if got := deliveredFor(env, "bd-t"); len(got) != 0 {
		t.Errorf("tracked-only issue delivered %+v", got)
	}
	got := deliveredFor(env, "bd-m")
	if len(got) != 1 || !reflect.DeepEqual(got[0].Kinds, []string{"added"}) {
		t.Fatalf("member delivered %+v, want exactly one added row", got)
	}
	if w, _ := watchedOf(t, st, "bd-m"); !w {
		t.Error("the member was not marked watched by its first hydration")
	}
	if len(h.calls) == 0 || h.calls[len(h.calls)-1] != "bd-m" {
		t.Errorf("hydrated %v, want the member read", h.calls)
	}

	// A PR change propagates link_changed to every linked issue: only the
	// watched member receives it.
	for _, id := range []string{"bd-m", "bd-t"} {
		if _, err := st.AppendEntityChange("o/r", "issue", id, []string{"link_changed"}, "pg-desk", gateAt); err != nil {
			t.Fatal(err)
		}
	}
	env, _, err = runTick(t, e)
	if err != nil {
		t.Fatal(err)
	}
	if got := deliveredFor(env, "bd-t"); len(got) != 0 {
		t.Errorf("propagation to an unwatched issue delivered %+v", got)
	}
	if got := deliveredFor(env, "bd-m"); len(got) != 1 || !reflect.DeepEqual(got[0].Kinds, []string{"link_changed"}) {
		t.Errorf("propagation to the watched issue delivered %+v", got)
	}
}

// T-20 / T-2 stickiness: a watched issue that left every query keeps its mark
// (an orphan is read by id), and the closed row that ends the watch is
// appended and delivered although the same write clears the mark; read back
// AFTER the clear it is still there.
func TestGateClosedRowOfAWatchedIssueIsDeliveredThenTheMarkIsCleared(t *testing.T) {
	e, st, h := newOrphanEngine(t)
	seedOrphan(t, st, "bd-o1")
	pipeline.InstallIssueGate(st, nil)
	e.Lister = fakeLister{res: listing()}

	// Left the query, still open: the mark survives and nothing is delivered.
	env, _, err := runTick(t, e)
	if err != nil {
		t.Fatal(err)
	}
	if w, _ := watchedOf(t, st, "bd-o1"); !w {
		t.Fatal("the mark was dropped when the issue left its query")
	}
	for _, r := range deliveredFor(env, "bd-o1") {
		if len(r.Kinds) == 1 && r.Kinds[0] == "closed" {
			t.Fatalf("closed delivered before the issue closed: %+v", r)
		}
	}

	h.facts = map[string]string{"bd-o1": `{"issue_show":{"id":"bd-o1","state":"closed","title":"t"}}`}
	h.kindsOf = map[string][]string{"bd-o1": {"closed"}}
	env, _, err = runTick(t, e)
	if err != nil {
		t.Fatal(err)
	}
	if got := deliveredFor(env, "bd-o1"); len(got) != 1 || !reflect.DeepEqual(got[0].Kinds, []string{"closed"}) {
		t.Fatalf("delivered %+v, want exactly the closed row", got)
	}
	if w, _ := watchedOf(t, st, "bd-o1"); w {
		t.Error("the mark survived the closure")
	}
	var closed int
	for _, r := range changeRows(t, st, "bd-o1") {
		if reflect.DeepEqual(r.Kinds, []string{"closed"}) {
			closed++
		}
	}
	if closed != 1 {
		t.Errorf("closed rows read back after the clear = %d, want 1 (a gate on read would hide it)", closed)
	}
	// Once cleared, the next write is withheld.
	before := len(changeRows(t, st, "bd-o1"))
	if _, err := st.AppendEntityChange("o/r", "issue", "bd-o1", []string{"link_changed"}, "pg-desk", gateAt); err != nil {
		t.Fatal(err)
	}
	if got := len(changeRows(t, st, "bd-o1")); got != before {
		t.Errorf("a row was appended after the clear: %d, want %d", got, before)
	}
}

// The removed row of a watched orphan (not_found) is appended and delivered
// although the same write clears the mark.
func TestGateRemovedRowOfAWatchedOrphanIsDeliveredThenTheMarkIsCleared(t *testing.T) {
	e, st, h := newOrphanEngine(t)
	seedOrphan(t, st, "bd-o2")
	pipeline.InstallIssueGate(st, nil)
	h.notFound["bd-o2"] = true
	e.Lister = fakeLister{res: listing()}

	env, _, err := runTick(t, e)
	if err != nil {
		t.Fatal(err)
	}
	var removed []Record
	for _, r := range deliveredFor(env, "bd-o2") {
		if reflect.DeepEqual(r.Kinds, []string{"removed"}) {
			removed = append(removed, r)
		}
	}
	if len(removed) != 1 {
		t.Fatalf("delivered %+v, want exactly one removed row", deliveredFor(env, "bd-o2"))
	}
	if w, _ := watchedOf(t, st, "bd-o2"); w {
		t.Error("the mark survived the removal")
	}
	if n := removedRows(t, st, "bd-o2"); n != 1 {
		t.Errorf("removed rows read back after the clear = %d, want 1", n)
	}
}
