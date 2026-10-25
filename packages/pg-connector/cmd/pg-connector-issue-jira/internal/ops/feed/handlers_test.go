package feed

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/changes"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/groups"
	libfeed "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/feed"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

func TestHandlers_AreExactlyChangesAndAckForTheIssueView(t *testing.T) {
	r := newRig(t)
	if len(r.handlers) != len(Ops) {
		t.Errorf("handlers = %d, want exactly %v", len(r.handlers), Ops)
	}
	for _, op := range Ops {
		h, ok := r.handlers[op]
		if !ok {
			t.Errorf("no handler for %q", op)
			continue
		}
		if h.SchemaVersion != schema.IssueSchemaVersion {
			t.Errorf("%s schema version = %d, want %d", op, h.SchemaVersion, schema.IssueSchemaVersion)
		}
	}
	if !slices.Equal(Ops, []string{"changes", "changes_ack"}) {
		t.Errorf("Ops = %v", Ops)
	}
}

func TestChanges_AWatchedIssuesClosedRowIsDeliveredAfterItLeftEveryQuery(t *testing.T) {
	r := newRig(t)
	r.seed()

	first := r.changesOf("c1", nil)
	if first.StoreIdentity == "" || len(first.Changes) != 0 || len(first.Sources) != 1 {
		t.Fatalf("first poll = %+v, want an empty answer with the store identity", first)
	}
	if got := first.Sources[0]; got.Backend != instance || got.Status != schema.ChangeSourceSucceeded || got.NextSeq != 0 {
		t.Errorf("source = %+v", got)
	}
	ack, err := r.ack("c1", first.Sources[0].NextSeq, nil)
	if err != nil || ack.StoreIdentity != first.StoreIdentity {
		t.Fatalf("ack = %+v, %v, want the store identity %q", ack, err, first.StoreIdentity)
	}

	// A-1 closes and no longer matches the query; T-1 (never watched) closes too.
	r.fake.setIssue("A-1", "done")
	r.fake.setIssue("T-1", "done")
	r.fake.list(jqlMine, "A-2")
	r.fetch("mine")
	r.fetch("team")
	r.settle()
	if words := r.rowWords(); !eq(words, []string{"A-1:closed"}) {
		t.Fatalf("log = %v, want only the closed row of the watched A-1 (an unwatched issue gets no row)", words)
	}

	res := r.changesOf("c1", nil)
	if len(res.Changes) != 1 {
		t.Fatalf("changes = %+v, want exactly the closed row", res.Changes)
	}
	c := res.Changes[0]
	row := r.logRows()[0]
	ent := entityOf(t, c)
	if c.Change != "changed" || c.Source != instance || c.Seq != row.Seq || !slices.Equal(c.Kinds, []string{"closed"}) {
		t.Errorf("change = %+v", c)
	}
	if ent.ID != "A-1" || ent.StatusCategory != "done" || ent.Version != row.Version || ent.Version == 0 {
		t.Errorf("entity = %+v, want A-1 closed at the row's version %d", ent, row.Version)
	}
	if res.StoreIdentity != first.StoreIdentity || res.Sources[0].NextSeq != row.Seq || res.Sources[0].Version != 0 {
		t.Errorf("answer = %+v", res.Sources)
	}

	// Not acknowledged: the same row is handed out again and the observer is told.
	again := r.changesOf("c1", nil)
	if len(again.Changes) != 1 || again.Changes[0].Seq != row.Seq {
		t.Fatalf("redelivery = %+v", again.Changes)
	}
	r.obs.mu.Lock()
	redelivered := r.obs.rows
	r.obs.mu.Unlock()
	if redelivered != 1 {
		t.Errorf("observer was told of %d redelivered rows, want 1", redelivered)
	}

	// Acknowledged: a quiet poll still carries next_seq and the store identity.
	ack, err = r.ack("c1", res.Sources[0].NextSeq, nil)
	if err != nil || ack.AckedSeq != row.Seq || ack.StoreIdentity != first.StoreIdentity {
		t.Fatalf("ack = %+v, %v", ack, err)
	}
	quiet := r.changesOf("c1", nil)
	if quiet.Changes == nil || len(quiet.Changes) != 0 || quiet.Sources[0].NextSeq != row.Seq || quiet.Sources[0].Version != row.Seq || quiet.StoreIdentity != first.StoreIdentity {
		t.Errorf("quiet poll = %+v, want no rows, next_seq %d and the store identity", quiet, row.Seq)
	}
}

func TestChanges_ANewConsumerStartsAtTheTailTakenAfterTheBaselinesExist(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()

	// No query has its baseline yet: the cursor is not created.
	early := r.changesOf("c1", nil)
	if len(early.Changes) != 0 || early.StoreIdentity == "" {
		t.Fatalf("early poll = %+v", early)
	}
	if _, ok, _ := r.st.GetConsumer(ctx, "c1"); ok {
		t.Fatal("a cursor was created before the baselines existed")
	}

	r.seed()
	r.fake.setIssue("A-1", "done")
	r.fetch("mine")
	r.settle()
	if words := r.rowWords(); !eq(words, []string{"A-1:closed"}) {
		t.Fatalf("log = %v", words)
	}

	// The cursor starts at the tail: the closed row that exists is not delivered.
	res := r.changesOf("c1", nil)
	if len(res.Changes) != 0 || res.Sources[0].NextSeq != 1 {
		t.Errorf("first poll after the baselines = %+v, want no rows and next_seq 1", res)
	}
	if row, ok, _ := r.st.GetConsumer(ctx, "c1"); !ok || row.AckedSeq != 1 {
		t.Errorf("consumer row = %+v %v, want a cursor at the tail", row, ok)
	}
}

func TestChanges_ADepartedIssueIsAddedAsAFirstObservationAfterTheBaseline(t *testing.T) {
	r := newRig(t)
	r.seed()
	r.changesOf("c1", nil)

	r.fake.setIssue("A-3", "new")
	r.fake.list(jqlMine, "A-1", "A-2", "A-3")
	r.fetch("mine")
	r.settle()
	res := r.changesOf("c1", nil)
	if len(res.Changes) != 1 || res.Changes[0].Change != "added" || !slices.Equal(res.Changes[0].Kinds, []string{"added"}) {
		t.Fatalf("changes = %+v, want one added change", res.Changes)
	}
	if got := entityOf(t, res.Changes[0]); got.ID != "A-3" {
		t.Errorf("entity = %+v", got)
	}
}

func TestChanges_OneCursorPerConsumerAndTheQueryOnlyFilters(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	r.seed()
	r.changesOf("everyone", nil)
	r.changesOf("narrow", map[string]any{"query": "mine"})

	// A-1 closes and leaves mine; A-2 closes and stays.
	r.fake.setIssue("A-1", "done")
	r.fake.setIssue("A-2", "done")
	r.fake.list(jqlMine, "A-2")
	r.fetch("mine")
	r.settle()

	wide := r.changesOf("everyone", nil)
	if len(wide.Changes) != 2 {
		t.Fatalf("unscoped consumer got %d changes, want both closed rows", len(wide.Changes))
	}
	narrow := r.changesOf("narrow", map[string]any{"query": "mine"})
	if len(narrow.Changes) != 1 || entityOf(t, narrow.Changes[0]).ID != "A-2" {
		t.Errorf("scoped consumer = %+v, want only the row of the current member A-2", narrow.Changes)
	}

	// Naming another query does not make a second cursor.
	_, err := r.pollErr("narrow", map[string]any{"query": "team"})
	if err == nil || !errors.Is(err, scriptout.ErrInvalidArgument) {
		t.Errorf("rebinding without reset = %v, want invalid_argument", err)
	}
	_, err = r.pollErr("narrow", nil)
	if err == nil || !errors.Is(err, scriptout.ErrInvalidArgument) {
		t.Errorf("dropping the query without reset = %v, want invalid_argument", err)
	}
	rows, err := r.st.ListConsumers(ctx)
	if err != nil || len(rows) != 2 {
		t.Errorf("consumers = %+v, %v, want exactly one cursor per consumer", rows, err)
	}

	if _, err := r.pollErr("c9", map[string]any{"query": "nope"}); !errors.Is(err, scriptout.ErrQueryNotRecognized) {
		t.Errorf("unknown query = %v, want query_not_recognized", err)
	}
}

func TestChanges_APositionOutsideRetentionAnswersCursorExpired(t *testing.T) {
	r := newRig(t)
	r.seed()
	r.changesOf("c1", nil)

	r.fake.setIssue("A-1", "done")
	r.fake.setIssue("A-2", "done")
	r.fetch("mine")
	r.settle()
	if len(r.logRows()) != 2 {
		t.Fatalf("log = %v", r.rowWords())
	}
	r.clk.Advance(48 * time.Hour)
	r.fake.setIssue("A-3", "new")
	r.fake.list(jqlMine, "A-1", "A-2", "A-3")
	r.fetch("mine")
	r.settle()
	if rows, _, err := r.feed.Prune(context.Background()); err != nil || rows != 2 {
		t.Fatalf("Prune = %d, %v, want the two old rows gone", rows, err)
	}

	_, err := r.pollErr("c1", nil)
	if err == nil || !errors.Is(err, scriptout.ErrInvalidArgument) || !strings.HasPrefix(err.Error(), "cursor_expired:") {
		t.Fatalf("poll = %v, want invalid_argument starting cursor_expired:", err)
	}
	// A reset recovers, and the cursor is served again.
	if _, err := r.pollErr("c1", map[string]any{"reset": true}); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if _, err := r.pollErr("c1", nil); err != nil {
		t.Errorf("poll after reset: %v", err)
	}
}

func TestChanges_AfterARecordedMoveAsideTheFirstPollOfEveryConsumerIsExpired(t *testing.T) {
	r := newRig(t)
	r.seed()
	r.changesOf("old", nil)
	r.clk.Advance(time.Hour)
	if err := r.feed.NoteMovedAside(context.Background(), r.clk.Now()); err != nil {
		t.Fatal(err)
	}

	for _, consumer := range []string{"old", "new"} {
		_, err := r.pollErr(consumer, nil)
		if err == nil || !errors.Is(err, scriptout.ErrInvalidArgument) || !strings.HasPrefix(err.Error(), "cursor_expired:") {
			t.Errorf("first poll of %s = %v, want cursor_expired:", consumer, err)
		}
	}
	if _, err := r.ack("new", 0, nil); err == nil || !strings.HasPrefix(err.Error(), "cursor_expired:") {
		t.Errorf("ack of a new cursor = %v, want cursor_expired:", err)
	}
	if _, err := r.pollErr("old", map[string]any{"reset": true}); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if _, err := r.pollErr("old", nil); err != nil {
		t.Errorf("poll after the reset: %v", err)
	}
}

func TestChanges_ResetReplaysOnlyWatchedActiveIssuesAsAdded(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	r.seed()

	res := r.changesOf("c1", map[string]any{"reset": true})
	var ids []string
	for _, c := range res.Changes {
		ids = append(ids, entityOf(t, c).ID)
		if c.Change != "added" || c.Seq != 0 || len(c.Kinds) != 0 || string(c.Fields) != "[]" || c.Source != instance {
			t.Errorf("replay change = %+v, want added with seq 0 and no kinds or fields", c)
		}
	}
	if !slices.Equal(ids, []string{"A-1", "A-2"}) {
		t.Errorf("replayed = %v, want the watched issues and not the unwatched T-1", ids)
	}

	if err := r.pers.MarkGone(ctx, "A-2", governor.PurposeRefresh); err != nil {
		t.Fatal(err)
	}
	res = r.changesOf("c1", map[string]any{"reset": true})
	if len(res.Changes) != 1 || entityOf(t, res.Changes[0]).ID != "A-1" {
		t.Errorf("replay after A-2 is gone = %+v, want only A-1", res.Changes)
	}
	if words := r.rowWords(); !eq(words, []string{"A-2:removed"}) {
		t.Errorf("log = %v, want only the removed row of the gone A-2 (a reset appends none)", words)
	}
}

func TestChanges_SourcesFreshnessHasOneEntryPerConfiguredQuery(t *testing.T) {
	r := newRig(t)
	r.seed()

	res := r.changesOf("c1", nil)
	if len(res.SourcesFreshness) != 2 {
		t.Fatalf("sources_freshness = %+v, want one entry per configured query", res.SourcesFreshness)
	}
	want := t0.Format(time.RFC3339)
	for i, name := range []string{"mine", "team"} {
		f := res.SourcesFreshness[i]
		if f.Query != name || f.LastSuccessAt != want || f.LastError != "" {
			t.Errorf("entry %d = %+v, want %s succeeded at %s", i, f, name, want)
		}
	}

	// A failed run of mine keeps its last success and records the error.
	ctx := context.Background()
	row, ok, err := r.st.GetQuery(ctx, "mine")
	if err != nil || !ok {
		t.Fatal(err)
	}
	row.LastError = "search failed"
	if err := r.st.PutQuery(ctx, row); err != nil {
		t.Fatal(err)
	}
	res = r.changesOf("c1", nil)
	if f := res.SourcesFreshness[0]; f.Query != "mine" || f.LastSuccessAt != want || f.LastError != "search failed" {
		t.Errorf("mine = %+v", f)
	}
	if f := res.SourcesFreshness[1]; f.LastError != "" {
		t.Errorf("team = %+v, want no error", f)
	}
}

func TestChanges_EachRowRendersItsOwnVersionOverTheCurrentSummary(t *testing.T) {
	r := newRig(t)
	r.seed()
	r.changesOf("c1", nil)

	r.fake.setIssue("A-1", "done")
	r.fetch("mine")
	r.settle()
	r.fake.setIssue("A-1", "indeterminate")
	r.fetch("mine")
	r.settle()

	rows := r.logRows()
	if len(rows) != 2 {
		t.Fatalf("log = %v, want a closed and a reopened row", r.rowWords())
	}
	res := r.changesOf("c1", nil)
	if len(res.Changes) != 2 {
		t.Fatalf("changes = %+v", res.Changes)
	}
	for i, c := range res.Changes {
		ent := entityOf(t, c)
		if ent.Version != rows[i].Version || ent.StatusCategory != "indeterminate" {
			t.Errorf("change %d entity = %+v, want version %d over the current summary", i, ent, rows[i].Version)
		}
	}
	if rows[0].Version == rows[1].Version {
		t.Errorf("both rows carry version %d", rows[0].Version)
	}
}

func TestChanges_MembershipWordsAreNeverStoredAndNeverBumpAVersion(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	r.seed()
	r.changesOf("c1", nil)
	before, _, err := r.st.GetEntity(ctx, groups.EntityKind, "A-2")
	if err != nil {
		t.Fatal(err)
	}

	// A-2 leaves mine unchanged and comes back.
	r.fake.list(jqlMine, "A-1")
	r.fetch("mine")
	r.fake.list(jqlMine, "A-1", "A-2")
	r.fetch("mine")
	r.settle()

	for _, row := range r.logRows() {
		for _, k := range row.Kinds {
			if k == "entered_query" || k == "left_query" {
				t.Errorf("row %d stores the membership word %q", row.Seq, k)
			}
		}
	}
	if words := r.rowWords(); len(words) != 0 {
		t.Errorf("log = %v, want no rows for a membership move", words)
	}
	after, _, err := r.st.GetEntity(ctx, groups.EntityKind, "A-2")
	if err != nil || after.Version != before.Version {
		t.Errorf("version %d -> %d, want a membership move to leave it alone (%v)", before.Version, after.Version, err)
	}
	if res := r.changesOf("c1", nil); len(res.Changes) != 0 {
		t.Errorf("changes = %+v", res.Changes)
	}
	for _, k := range changes.Spec().Changes {
		if k == "entered_query" || k == "left_query" {
			t.Errorf("registration lists %q as a stored change", k)
		}
	}
}

// failingRun is the real queries with a Run that fails.
type failingRun struct {
	libfeed.Queries
	err error
}

func (f failingRun) Run(context.Context, string) error { return f.err }

func TestChanges_AnUnauthenticatedRunAnswersUnauthenticated(t *testing.T) {
	r := newRig(t)
	r.seed()
	f, err := New(Config{
		Store: r.st, Queries: failingRun{Queries: r.queries, err: scriptout.WrapError(scriptout.ErrUnauthenticated, "no login")},
		Clock: r.clk, Retention: retention, ConsumerStaleAfter: 7 * 24 * time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	h := Handlers(Deps{Feed: f, Store: r.st, Renderer: NewRenderer(r.st), Instance: instance, Clock: r.clk})
	_, err = h["changes"].Handle(context.Background(), []byte(`{"consumer":"c1"}`))
	if !errors.Is(err, scriptout.ErrUnauthenticated) {
		t.Errorf("err = %v, want unauthenticated", err)
	}
}

func TestChanges_ANonCachedPollRunsAStaleQueryBeforeReading(t *testing.T) {
	r := newRig(t)
	r.seed()
	r.changesOf("c1", nil)
	r.fake.setIssue("A-1", "done")
	r.clk.Advance(48 * time.Hour)

	if _, err := r.call("changes", map[string]any{"consumer": "c1"}); err != nil {
		t.Fatal(err)
	}
	r.settle()
	if words := r.rowWords(); !eq(words, []string{"A-1:closed"}) {
		t.Errorf("log = %v, want the poll to have re-run the stale watched query", words)
	}
}
