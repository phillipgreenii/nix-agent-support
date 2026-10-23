package feedop

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

func TestHandlersProvideTheTwoOpsAtTheSchemaVersionOfTheDeps(t *testing.T) {
	r := newRig(t, viewAll, nil)
	if len(r.ops) != 2 || r.ops["changes"] == nil || r.ops["changes_ack"] == nil {
		t.Fatalf("ops = %v", r.ops)
	}
}

func TestTheFirstPollOfANewCursorAnswersNoRowsAtTheTailAndAckAdvancesIt(t *testing.T) {
	r := newRig(t, viewAll, nil)
	r.add(refA, 1, "s1", []string{kindContent})
	res := r.changes(`{"consumer":"c","query":"mine"}`)
	if len(res.Changes) != 0 || len(res.Sources) != 1 {
		t.Fatalf("first poll = %+v", res)
	}
	src := res.Sources[0]
	if src.Backend != sourceName || src.Status != schema.ChangeSourceSucceeded || src.NextSeq != 1 || src.Version != 1 {
		t.Fatalf("source = %+v", src)
	}
	second := r.add(refA, 2, "s2", []string{kindContent})
	res = r.changes(`{"consumer":"c","query":"mine"}`)
	if len(res.Changes) != 1 || res.Changes[0].Seq != second {
		t.Fatalf("second poll = %+v", res)
	}
	if got := r.ack(`{"consumer":"c","query":"mine","next_seq":2}`); got.Consumer != "c" || got.Query != "mine" || got.AckedSeq != 2 {
		t.Fatalf("ack = %+v", got)
	}
	if got := r.ack(`{"consumer":"c","query":"mine","next_seq":1}`); got.AckedSeq != 2 {
		t.Fatalf("a backward ack answers the stored position: %+v", got)
	}
}

func TestRowsCarrySeqKindsFieldsTheWordAndTheEntityOfTheRenderer(t *testing.T) {
	r := newRig(t, viewAll, nil)
	r.changes(`{"consumer":"c","query":"mine"}`)
	title := enteredField("mine")
	changed := r.add(refA, 4, "s4", []string{kindContent})
	entered := r.add(refB, 2, "sb", []string{kindEntered}, title)
	left := r.add(refA, 6, "s6", []string{kindLeft}, leftField("mine"))
	r.add(refB, 3, "sc", []string{kindContent}) // not a member

	res := r.changes(`{"consumer":"c","query":"mine"}`)
	if len(res.Changes) != 3 {
		t.Fatalf("changes = %+v", res.Changes)
	}
	want := []struct {
		seq     int64
		word    string
		ref     string
		version int64
		stamp   string
		kinds   string
	}{
		{changed, "changed", refA, 4, "s4", kindContent},
		{entered, "added", refB, 2, "sb", kindEntered},
		{left, "removed", refA, 6, "s6", kindLeft},
	}
	for i, w := range want {
		c := res.Changes[i]
		e := decodeEntity(t, c.Entity)
		if c.Seq != w.seq || c.Change != w.word || c.Source != sourceName || strings.Join(c.Kinds, ",") != w.kinds ||
			e.Ref != w.ref || e.Version != w.version || e.Stamp != w.stamp || e.Replay {
			t.Errorf("change %d = %+v entity %+v; want %+v", i, c, e, w)
		}
	}
	if string(res.Changes[0].Fields) != "[]" {
		t.Errorf("a row without fields answers %s, want []", res.Changes[0].Fields)
	}
	if string(res.Changes[1].Fields) == "[]" {
		t.Errorf("a row with fields answers %s", res.Changes[1].Fields)
	}
}

func TestTheFilteredViewDeliversOnlyItsKindsAndTheMembershipRows(t *testing.T) {
	r := newRig(t, viewFlagged, nil)
	r.changes(`{"consumer":"c","query":"mine"}`)
	r.add(refA, 1, "s1", []string{kindContent})
	flagged := r.add(refA, 2, "s2", []string{kindFlag})
	left := r.add(refB, 3, "s3", []string{kindLeft}, leftField("mine"))
	res := r.changes(`{"consumer":"c","query":"mine"}`)
	if len(res.Changes) != 2 || res.Changes[0].Seq != flagged || res.Changes[1].Seq != left {
		t.Fatalf("changes = %+v", res.Changes)
	}
}

func TestAResetReplaysTheMembersAsAddedWithSeqZeroAndMovesThePosition(t *testing.T) {
	r := newRig(t, viewAll, nil)
	r.add(refA, 1, "s1", []string{kindContent})
	head := r.add(refB, 1, "s2", []string{kindContent})
	res := r.changes(`{"consumer":"c","query":"team","reset":true}`)
	if len(res.Changes) != 2 || res.Sources[0].NextSeq != head || res.Sources[0].Version != head {
		t.Fatalf("reset = %+v", res)
	}
	for i, ref := range []string{refA, refB} {
		c := res.Changes[i]
		e := decodeEntity(t, c.Entity)
		if c.Change != "added" || c.Seq != 0 || string(c.Fields) != "[]" || len(c.Kinds) != 0 || c.Kinds == nil || e.Ref != ref || !e.Replay {
			t.Errorf("replay %d = %+v entity %+v", i, c, e)
		}
	}
	if res := r.changes(`{"consumer":"c","query":"team"}`); len(res.Changes) != 0 {
		t.Fatalf("after a reset = %+v", res)
	}
}

func TestMembershipIsJudgedByTheFoldOfTheSpec(t *testing.T) {
	r := newRig(t, viewAll, nil)
	r.changes(`{"consumer":"c","query":"mine"}`)
	r.add("Example-Org/Widget#1", 1, "s1", []string{kindContent})
	if res := r.changes(`{"consumer":"c","query":"mine"}`); len(res.Changes) != 1 {
		t.Fatalf("a member that differs in case = %+v", res.Changes)
	}
}

func TestSourcesFreshnessListsEveryConfiguredQuery(t *testing.T) {
	r := newRig(t, viewAll, nil)
	ok := t0.Add(time.Hour)
	if err := r.st.PutQuery(t.Context(), store.QueryRow{Key: "mine", Configured: true, IDs: []string{refA}, BaselineAt: t0, LastSuccessAt: ok}); err != nil {
		t.Fatal(err)
	}
	if err := r.st.PutQuery(t.Context(), store.QueryRow{Key: "team", Configured: true, LastError: "example error"}); err != nil {
		t.Fatal(err)
	}
	res := r.changes(`{"consumer":"c","query":"mine"}`)
	if len(res.SourcesFreshness) != 2 {
		t.Fatalf("freshness = %+v", res.SourcesFreshness)
	}
	by := map[string]schema.SourceFreshness{}
	for _, f := range res.SourcesFreshness {
		by[f.Query] = f
	}
	if by["mine"].LastSuccessAt != "2026-10-12T10:00:00Z" || by["mine"].LastError != "" || by["team"].LastError != "example error" || by["team"].LastSuccessAt != "" {
		t.Fatalf("freshness = %+v", by)
	}
}

func TestResultArraysAreEmptyArraysNeverNull(t *testing.T) {
	r := newRig(t, viewAll, nil)
	raw, err := r.call("changes", `{"consumer":"c","query":"mine"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"changes":[]`) || strings.Contains(string(raw), "null") {
		t.Fatalf("answer = %s", raw)
	}
	if strings.Contains(string(raw), "store_identity") {
		t.Fatalf("the answer carries a store identity before the packet that adds it: %s", raw)
	}
}

func TestArgumentErrorsAreInvalidArgument(t *testing.T) {
	r := newRig(t, viewAll, nil)
	for name, tc := range map[string]struct{ op, args string }{
		"changes: no arguments":          {"changes", ``},
		"changes: not an object":         {"changes", `[1]`},
		"changes: no consumer":           {"changes", `{"query":"mine"}`},
		"changes: no query":              {"changes", `{"consumer":"c"}`},
		"changes: consumer not a string": {"changes", `{"consumer":3,"query":"mine"}`},
		"changes: cached not a boolean":  {"changes", `{"consumer":"c","query":"mine","cached":"yes"}`},
		"changes: reset not a boolean":   {"changes", `{"consumer":"c","query":"mine","reset":1}`},
		"changes: a since bound":         {"changes", `{"consumer":"c","query":"mine","since":"x"}`},
		"changes: a before bound":        {"changes", `{"consumer":"c","query":"mine","before":"x"}`},
		"ack: no next_seq":               {"changes_ack", `{"consumer":"c","query":"mine"}`},
		"ack: next_seq a string":         {"changes_ack", `{"consumer":"c","query":"mine","next_seq":"1"}`},
		"ack: next_seq null":             {"changes_ack", `{"consumer":"c","query":"mine","next_seq":null}`},
		"ack: next_seq negative":         {"changes_ack", `{"consumer":"c","query":"mine","next_seq":-1}`},
		"ack: no consumer":               {"changes_ack", `{"query":"mine","next_seq":0}`},
	} {
		if _, err := r.call(tc.op, tc.args); !errors.Is(err, scriptout.ErrInvalidArgument) {
			t.Errorf("%s: err = %v, want invalid_argument", name, err)
		}
	}
}

func TestTheErrorsOfTheFeedAreOnTheWireTaxonomy(t *testing.T) {
	r := newRig(t, viewAll, nil)
	if _, err := r.call("changes", `{"consumer":"c","query":"unknown"}`); !errors.Is(err, scriptout.ErrQueryNotRecognized) {
		t.Errorf("unknown query = %v", err)
	}
	if _, err := r.call("changes_ack", `{"consumer":"c","query":"mine","next_seq":9}`); !errors.Is(err, scriptout.ErrInvalidArgument) || !strings.Contains(err.Error(), "beyond the head") {
		t.Errorf("ack above the head = %v", err)
	}
	r.q.runErr = errors.New("boom")
	if _, err := r.call("changes", `{"consumer":"d","query":"mine"}`); !errors.Is(err, scriptout.ErrUnavailable) || !strings.Contains(err.Error(), "changes: boom") {
		t.Errorf("a failed run = %v", err)
	}
	r.q.runErr = errAuth
	if _, err := r.call("changes", `{"consumer":"d","query":"mine"}`); !errors.Is(err, scriptout.ErrUnauthenticated) {
		t.Errorf("an authentication failure = %v", err)
	}
}

func TestACursorOfAnotherViewConflictsNamingTheExistingCursor(t *testing.T) {
	r := newRig(t, viewAll, nil)
	r.changes(`{"consumer":"c","query":"mine"}`)
	_, err := r.forView(viewFlagged).call("changes", `{"consumer":"c","query":"mine"}`)
	if !errors.Is(err, scriptout.ErrInvalidArgument) || !strings.Contains(err.Error(), "(c, all, mine)") {
		t.Fatalf("conflict = %v", err)
	}
}

func TestARendererFailureIsUnavailable(t *testing.T) {
	r := newRig(t, viewAll, fakeRenderer{err: errors.New("render boom")})
	r.changes(`{"consumer":"c","query":"mine"}`)
	r.add(refA, 1, "s1", []string{kindContent})
	if _, err := r.call("changes", `{"consumer":"c","query":"mine"}`); !errors.Is(err, scriptout.ErrUnavailable) || !strings.Contains(err.Error(), "render boom") {
		t.Fatalf("row render failure = %v", err)
	}
	if _, err := r.call("changes", `{"consumer":"c","query":"team","reset":true}`); !errors.Is(err, scriptout.ErrUnavailable) {
		t.Fatalf("replay render failure = %v", err)
	}
}
