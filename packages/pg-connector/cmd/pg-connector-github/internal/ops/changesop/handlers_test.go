package changesop

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/classify"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/ops/status"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

func TestHandlersProvideTheTwoPROpsAtThePRSchemaVersion(t *testing.T) {
	r := newRig(t)
	hs := Handlers(Deps{})
	if len(hs) != 2 || len(hs["pr"]) != 2 || len(hs["ci"]) != 2 {
		t.Fatalf("handlers = %v, want exactly pr and ci, each with changes and changes_ack", hs)
	}
	for _, op := range []string{OpChanges, OpChangesAck} {
		h := hs["pr"][op]
		if h.Handle == nil || h.SchemaVersion != schema.PRSchemaVersion {
			t.Errorf("%s: handle nil %t, schema version %d (want %d)", op, h.Handle == nil, h.SchemaVersion, schema.PRSchemaVersion)
		}
	}
	if resp := r.call(OpChangesAck, ackArgs("c", "mine", 0)); resp.SchemaVersion != schema.PRSchemaVersion {
		t.Errorf("answer schemaVersion = %d, want %d", resp.SchemaVersion, schema.PRSchemaVersion)
	}
}

func TestFirstPollOfANewKeyReturnsNoRowsAtTheTailAndAckAdvancesIt(t *testing.T) {
	r := newRig(t)
	r.putPR(refA, "PR_a", "Add the widget", "head-1", 3)
	r.add(refA, 1, "head-0", []string{classify.KindHeadChanged})
	tail := r.add(refA, 2, "head-1", []string{classify.KindHeadChanged})

	res, _ := r.changes(pollArgs("pg-router", "mine"))
	if len(res.Changes) != 0 || len(res.Sources) != 1 {
		t.Fatalf("first poll = %+v, want one source and no rows", res)
	}
	src := res.Sources[0]
	if src.Backend != status.RegistryNamePR || src.Status != "succeeded" || src.Truncated || src.NextSeq != tail || src.Version != tail {
		t.Errorf("source = %+v, want %s succeeded at the tail %d", src, status.RegistryNamePR, tail)
	}
	if p, ok := r.pos("pg-router", "mine"); !ok || p != tail {
		t.Fatalf("key = %d, %v; want created at the tail %d", p, ok, tail)
	}

	next := r.add(refA, 3, "head-2", []string{classify.KindHeadChanged})
	if got := r.ack("pg-router", "mine", next); got.AckedSeq != next || got.Consumer != "pg-router" || got.Query != "mine" {
		t.Errorf("ack = %+v, want position %d", got, next)
	}
	if p, _ := r.pos("pg-router", "mine"); p != next {
		t.Errorf("stored position = %d, want %d", p, next)
	}
	// A lower acknowledgement is a success and reports the unchanged position.
	if got := r.ack("pg-router", "mine", tail); got.AckedSeq != next {
		t.Errorf("ack below the position = %+v, want the position %d unchanged", got, next)
	}
}

func TestAckCreatesAKeyWhoseBaselineWasMissing(t *testing.T) {
	r := newRig(t)
	r.setQuery("fresh", false)
	head := r.add(refA, 1, "head-1", []string{classify.KindHeadChanged})

	res, _ := r.changes(pollArgs("c", "fresh"))
	if len(res.Changes) != 0 || res.Sources[0].NextSeq != head || res.Sources[0].Version != 0 {
		t.Fatalf("poll = %+v, want no rows, next_seq %d, version 0", res, head)
	}
	if _, ok := r.pos("c", "fresh"); ok {
		t.Fatal("the poll created a key although the query had no baseline")
	}
	if got := r.ack("c", "fresh", res.Sources[0].NextSeq); got.AckedSeq != head {
		t.Errorf("ack = %+v, want position %d", got, head)
	}
	if p, ok := r.pos("c", "fresh"); !ok || p != head {
		t.Errorf("key = %d, %v; want created at %d", p, ok, head)
	}
}

func TestRowsCarrySeqKindsFieldsTheChangeWordAndTheRowsOwnVersionAndHead(t *testing.T) {
	r := newRig(t)
	r.putPR(refA, "PR_a", "Add the widget", "head-9", 9)
	first := r.add(refA, 1, "head-1", []string{classify.KindHeadChanged}) // before the key
	r.changes(pollArgs("c", "mine"))

	title := classify.Field{Name: "title", Before: jsonOf("old"), After: jsonOf("Add the widget")}
	changed := r.add(refA, 4, "head-4", []string{classify.KindFeedbackChanged}, title)
	second := r.add(refA, 5, "head-5", []string{classify.KindHeadChanged})
	entered := r.add(refB, 2, "head-b", []string{classify.KindEnteredQuery}, enteredField("mine"))
	left := r.add(refA, 6, "head-6", []string{classify.KindLeftQuery}, leftField("mine"))
	removed := r.add(refA, 7, "head-7", []string{classify.KindRemoved}, leftField("mine"))
	_ = first

	res, _ := r.changes(pollArgs("c", "mine"))
	if len(res.Changes) != 5 {
		t.Fatalf("changes = %+v, want 5", res.Changes)
	}
	want := []struct {
		seq     int64
		change  string
		version int64
		head    string
		kinds   []string
	}{
		{changed, "changed", 4, "head-4", []string{classify.KindFeedbackChanged}},
		{second, "changed", 5, "head-5", []string{classify.KindHeadChanged}},
		{entered, "added", 2, "head-b", []string{classify.KindEnteredQuery}},
		{left, "removed", 6, "head-6", []string{classify.KindLeftQuery}},
		{removed, "removed", 7, "head-7", []string{classify.KindRemoved}},
	}
	for i, w := range want {
		c := res.Changes[i]
		if c.Seq != w.seq || c.Change != w.change || c.Source != status.RegistryNamePR ||
			c.Entity.Version != w.version || c.Entity.HeadSHA != w.head || strings.Join(c.Kinds, ",") != strings.Join(w.kinds, ",") {
			t.Errorf("change %d = seq %d %s %s version %d head %s kinds %v; want %+v", i, c.Seq, c.Change, c.Source, c.Entity.Version, c.Entity.HeadSHA, c.Kinds, w)
		}
	}
	// Two rows of one PR keep their own values although the entity is now at version 9 and head-9.
	if a, b := res.Changes[0].Entity, res.Changes[1].Entity; a.ID != refA || b.ID != refA || a.Version == b.Version || a.HeadSHA == b.HeadSHA {
		t.Errorf("two rows of one PR share values: %+v / %+v", a, b)
	}
	// The entity is the summary-level PR of the current stored summary.
	if e := res.Changes[0].Entity; e.Title != "Add the widget" || e.Repo != "acme-org/widget" || e.Number != 1 || e.State != "open" || e.Author != "phillipgreenii" {
		t.Errorf("entity = %+v, want the stored summary's fields", e)
	}
	// An entity the store holds no summary for carries only id, version and head_sha.
	if e := res.Changes[2].Entity; e.ID != refB || e.Title != "" || e.Repo != "" {
		t.Errorf("entity without a summary = %+v, want id, version and head only", e)
	}
	var fields []classify.Field
	if err := json.Unmarshal(res.Changes[0].Fields, &fields); err != nil || len(fields) != 1 || fields[0].Name != "title" {
		t.Errorf("fields = %s (%v), want the row's title field", res.Changes[0].Fields, err)
	}
	if res.Sources[0].NextSeq != removed {
		t.Errorf("next_seq = %d, want %d", res.Sources[0].NextSeq, removed)
	}
}

func TestAnUnacknowledgedPollIsRedeliveredWithTheSameSeqValues(t *testing.T) {
	r := newRig(t)
	r.changes(pollArgs("c", "mine"))
	s1 := r.add(refA, 1, "h1", []string{classify.KindHeadChanged})
	s2 := r.add(refA, 2, "h2", []string{classify.KindHeadChanged})

	first, _ := r.changes(pollArgs("c", "mine"))
	again, _ := r.changes(pollArgs("c", "mine"))
	seqs := func(res schema.PRChangesResult) []int64 {
		var out []int64
		for _, c := range res.Changes {
			out = append(out, c.Seq)
		}
		return out
	}
	if a, b := seqs(first), seqs(again); len(a) != 2 || len(b) != 2 || a[0] != s1 || a[1] != s2 || b[0] != s1 || b[1] != s2 {
		t.Fatalf("seqs = %v then %v, want %d,%d twice", a, b, s1, s2)
	}
	r.ack("c", "mine", first.Sources[0].NextSeq)
	if after, _ := r.changes(pollArgs("c", "mine")); len(after.Changes) != 0 {
		t.Errorf("after the ack, changes = %+v, want none", after.Changes)
	}
}

func TestNextSeqIsPastTheLastReturnedRowWhenNonMemberRowsWereSkipped(t *testing.T) {
	r := newRig(t)
	r.changes(pollArgs("c", "mine"))
	member := r.add(refA, 1, "h1", []string{classify.KindHeadChanged})
	r.add(other, 1, "h2", []string{classify.KindHeadChanged})
	skipped := r.add(other, 2, "h3", []string{classify.KindHeadChanged})

	res, _ := r.changes(pollArgs("c", "mine"))
	if len(res.Changes) != 1 || res.Changes[0].Seq != member {
		t.Fatalf("changes = %+v, want only the member row %d", res.Changes, member)
	}
	if got := res.Sources[0].NextSeq; got != skipped || got <= member {
		t.Errorf("next_seq = %d, want %d (past the last returned row %d)", got, skipped, member)
	}
	if res.Sources[0].Version != r.head()-3 {
		t.Errorf("version = %d, want the key's position %d", res.Sources[0].Version, r.head()-3)
	}
}

func TestResetReturnsTheCurrentMembersAsAddedWithSeqZeroAndMovesThePosition(t *testing.T) {
	r := newRig(t)
	r.putPR(refA, "PR_a", "Add the widget", "head-a", 6)
	r.changes(pollArgs("c", "team"))
	r.add(refA, 1, "h1", []string{classify.KindHeadChanged})
	head := r.add(refB, 1, "h2", []string{classify.KindHeadChanged})

	res, resp := r.changes(`{"consumer":"c","query":"team","reset":true}`)
	if len(res.Changes) != 2 {
		t.Fatalf("reset changes = %+v, want the two members", res.Changes)
	}
	for i, ref := range []string{refA, refB} {
		c := res.Changes[i]
		if c.Entity.ID != ref || c.Change != "added" || c.Seq != 0 || c.Source != status.RegistryNamePR {
			t.Errorf("replay %d = %+v, want %s added with seq 0", i, c, ref)
		}
	}
	if e := res.Changes[0].Entity; e.Version != 6 || e.HeadSHA != "head-a" || e.Title != "Add the widget" {
		t.Errorf("replay entity = %+v, want the entity as of now (version 6, head-a)", e)
	}
	if !strings.Contains(string(resp.Result), `"kinds":[]`) || !strings.Contains(string(resp.Result), `"fields":[]`) {
		t.Errorf("a replay entry has empty kinds and fields arrays: %s", resp.Result)
	}
	if res.Sources[0].NextSeq != head || res.Sources[0].Version != head {
		t.Errorf("source = %+v, want next_seq and version %d", res.Sources[0], head)
	}
	if p, _ := r.pos("c", "team"); p != head {
		t.Errorf("position = %d, want the head %d", p, head)
	}
	r.ack("c", "team", res.Sources[0].NextSeq)
	if after, _ := r.changes(pollArgs("c", "team")); len(after.Changes) != 0 {
		t.Errorf("after reset and ack, changes = %+v, want none", after.Changes)
	}
}

func TestCachedNeverRunsTheQueryAndAnUncachedPollDoes(t *testing.T) {
	r := newRig(t)
	r.changes(`{"consumer":"c","query":"mine","cached":true}`)
	r.changes(`{"consumer":"c","query":"mine","cached":true}`)
	if n := r.q.runCount("mine"); n != 0 {
		t.Fatalf("cached polls ran the query %d times", n)
	}
	r.changes(pollArgs("c", "mine"))
	if n := r.q.runCount("mine"); n != 1 {
		t.Errorf("an uncached poll ran the query %d times, want 1", n)
	}
}

func TestSourcesFreshnessListsEveryConfiguredQuery(t *testing.T) {
	r := newRig(t)
	r.putQueryRow(store.QueryRow{Key: "mine", Configured: true, IDs: []string{refA}, BaselineAt: t0, LastSuccessAt: t0.Add(time.Hour), LastError: ""})
	r.putQueryRow(store.QueryRow{Key: "team", Configured: true, IDs: []string{refA, refB}, BaselineAt: t0, LastError: "rate limited"})
	// An ad hoc key is not a configured query.
	r.putQueryRow(store.QueryRow{Key: "mine?author=x", Configured: false})

	res, _ := r.changes(pollArgs("c", "mine"))
	want := []schema.SourceFreshness{
		{Query: "mine", LastSuccessAt: "2026-10-12T10:00:00Z"},
		{Query: "team", LastError: "rate limited"},
	}
	if len(res.SourcesFreshness) != len(want) {
		t.Fatalf("sources_freshness = %+v, want %+v", res.SourcesFreshness, want)
	}
	for i := range want {
		if res.SourcesFreshness[i] != want[i] {
			t.Errorf("sources_freshness[%d] = %+v, want %+v", i, res.SourcesFreshness[i], want[i])
		}
	}
}

func TestResultArraysAreEmptyArraysNeverNull(t *testing.T) {
	r := newRig(t)
	if err := r.st.DeleteQuery(context.Background(), "mine"); err != nil {
		t.Fatal(err)
	}
	if err := r.st.DeleteQuery(context.Background(), "team"); err != nil {
		t.Fatal(err)
	}
	_, resp := r.changes(pollArgs("c", "mine"))
	for _, key := range []string{`"changes":[]`, `"sources_freshness":[]`} {
		if !strings.Contains(string(resp.Result), key) {
			t.Errorf("answer %s lacks %s", resp.Result, key)
		}
	}
	if strings.Contains(string(resp.Result), "null") {
		t.Errorf("answer contains null: %s", resp.Result)
	}
}

func TestMemberFlagIgnoresTheCaseOfOwnerAndRepo(t *testing.T) {
	r := newRig(t)
	r.setQuery("cased", true, "Acme-Org/Widget#1")
	r.changes(pollArgs("c", "cased"))
	seq := r.add("acme-org/widget#1", 1, "h", []string{classify.KindHeadChanged})
	res, _ := r.changes(pollArgs("c", "cased"))
	if len(res.Changes) != 1 || res.Changes[0].Seq != seq || res.Changes[0].Change != "changed" {
		t.Errorf("changes = %+v, want the one member row as changed", res.Changes)
	}
}

func TestArgumentErrorsAreInvalidArgument(t *testing.T) {
	r := newRig(t)
	for _, args := range []string{
		`{"query":"mine"}`,
		`{"consumer":"  ","query":"mine"}`,
		`{"consumer":"c"}`,
		``,
		`{"consumer":"c","query":"mine","since":"2026-10-01T00:00:00Z"}`,
		`{"consumer":"c","query":"mine","before":"2026-10-01T00:00:00Z"}`,
		`{"consumer":"c","query":"mine","cached":"yes"}`,
		`{"consumer":"c","query":"mine","reset":1}`,
		`{"consumer":7,"query":"mine"}`,
		`[1]`,
	} {
		wantError(t, r.call(OpChanges, args), "invalid_argument", "")
	}
	wantError(t, r.call(OpChanges, `{"consumer":"c","query":"mine","since":"x"}`), "invalid_argument", `"since"`)
	for name, args := range map[string]string{
		"no next_seq":        `{"consumer":"c","query":"mine"}`,
		"negative next_seq":  `{"consumer":"c","query":"mine","next_seq":-1}`,
		"fractional":         `{"consumer":"c","query":"mine","next_seq":1.5}`,
		"textual":            `{"consumer":"c","query":"mine","next_seq":"3"}`,
		"null next_seq":      `{"consumer":"c","query":"mine","next_seq":null}`,
		"no consumer":        `{"query":"mine","next_seq":0}`,
		"no query":           `{"consumer":"c","next_seq":0}`,
		"blank consumer ack": `{"consumer":"","query":"mine","next_seq":0}`,
	} {
		resp := r.call(OpChangesAck, args)
		if resp.Error == nil || resp.Error.Code != "invalid_argument" {
			t.Errorf("ack %s (%s): %+v, want invalid_argument", name, args, resp)
		}
	}
	for _, q := range []string{"mine"} {
		if _, ok := r.pos("c", q); ok {
			t.Errorf("a refused request created a key for %s", q)
		}
	}
}

func TestAnUnknownQueryIsQueryNotRecognized(t *testing.T) {
	r := newRig(t)
	wantError(t, r.call(OpChanges, pollArgs("c", "nope")), "query_not_recognized", `"nope"`)
	wantError(t, r.call(OpChangesAck, ackArgs("c", "nope", 0)), "query_not_recognized", `"nope"`)
}

func TestAckAboveTheHeadIsInvalidArgumentAndChangesNothing(t *testing.T) {
	r := newRig(t)
	r.changes(pollArgs("c", "mine"))
	head := r.add(refA, 1, "h", []string{classify.KindHeadChanged})
	wantError(t, r.call(OpChangesAck, ackArgs("c", "mine", head+5)), "invalid_argument", "ack beyond the head")
	if p, _ := r.pos("c", "mine"); p == head+5 {
		t.Errorf("position moved to %d", p)
	}
}

func TestCursorExpiredIsInvalidArgumentWithTheFeedsMessage(t *testing.T) {
	r := newRig(t)
	r.add(refA, 1, "h", []string{classify.KindHeadChanged})
	if err := r.f.NoteMovedAside(context.Background(), r.clk.Now()); err != nil {
		t.Fatal(err)
	}
	resp := r.call(OpChanges, pollArgs("c", "mine"))
	wantError(t, resp, "invalid_argument", "cursor_expired:")
	if resp.Error != nil && !strings.HasPrefix(resp.Error.Message, "cursor_expired:") {
		t.Errorf("message = %q, want it to begin cursor_expired:", resp.Error.Message)
	}
	// A reset recovers, and the answer carries the members.
	res, _ := r.changes(`{"consumer":"c","query":"mine","reset":true}`)
	if len(res.Changes) != 1 {
		t.Errorf("reset after cursor_expired = %+v, want the member", res.Changes)
	}
}

func TestAKeyConflictIsInvalidArgumentNamingTheExistingKey(t *testing.T) {
	r := newRig(t)
	if _, err := r.f.Poll(context.Background(), feedPoll("ci", "c", "mine")); err != nil {
		t.Fatal(err)
	}
	resp := r.call(OpChanges, pollArgs("c", "mine"))
	wantError(t, resp, "invalid_argument", "(c, ci, mine)")
	wantError(t, r.call(OpChangesAck, ackArgs("c", "mine", 0)), "invalid_argument", "(c, ci, mine)")
	// Another consumer is not in conflict.
	r.changes(pollArgs("other-consumer", "mine"))
}

func TestAFailedQueryRunIsClassifiedLikeAnOriginFailure(t *testing.T) {
	r := newRig(t)
	r.q.runErr = context.DeadlineExceeded
	wantError(t, r.call(OpChanges, pollArgs("c", "mine")), "unavailable", "changes:")
	r.q.runErr = scriptout.WrapError(scriptout.ErrNotFound, "gone")
	wantError(t, r.call(OpChanges, pollArgs("c", "mine")), "not_found", "")
	// With cached the run is not attempted, so the failure is not seen.
	r.changes(`{"consumer":"c","query":"mine","cached":true}`)
}
