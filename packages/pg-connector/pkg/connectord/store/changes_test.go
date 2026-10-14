package store

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"testing"
	"time"
)

// Tests of the change engine's tables (schema version 3) and their accessors:
// the change log, pending rows, consumers and the meta table.

func ms(n int64) time.Time { return time.UnixMilli(n).UTC() }

func appendRow(t *testing.T, s *Store, row ChangeRow) int64 {
	t.Helper()
	var seq int64
	if err := s.InTx(context.Background(), func(tx *Tx) error {
		var err error
		seq, err = tx.AppendChange(context.Background(), row)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return seq
}

// ---- migration to version 3 --------------------------------------------

func TestVersion1And2FixturesMigrateToVersion3KeepingTheirData(t *testing.T) {
	if SchemaVersion != 3 {
		t.Fatalf("SchemaVersion = %d, this test describes the step to version 3", SchemaVersion)
	}
	ctx := context.Background()
	for _, from := range []int{1, 2} {
		t.Run(fmt.Sprintf("from_v%d", from), func(t *testing.T) {
			fx, ok := fixtures(t)[from]
			if !ok {
				t.Fatalf("no version %d fixture", from)
			}
			path := dbPath(t)
			copyFile(t, fx, path)
			s, res := mustOpen(t, path, fastOpts())
			if res.Outcome != OutcomeMigrated || res.FromVersion != from || res.ToVersion != 3 {
				t.Fatalf("result = %+v, want migrated %d -> 3", res, from)
			}
			assertFixtureData(t, ctx, s)
			assertFixtureJobs(t, ctx, s, from)
			assertFixtureChanges(t, ctx, s, from)

			// The new tables are usable at once, and the first sequence number is 1.
			if seq := appendRow(t, s, ChangeRow{Kind: "pr", ID: "org/repo#1", At: ms(1)}); seq != 1 {
				t.Errorf("first seq = %d, want 1", seq)
			}
		})
	}
}

func TestVersion3FixtureIsAPlainOpenAndReopenIsIdempotent(t *testing.T) {
	ctx := context.Background()
	fx, ok := fixtures(t)[3]
	if !ok {
		t.Fatal("no version 3 fixture")
	}
	path := dbPath(t)
	copyFile(t, fx, path)
	s, res := mustOpen(t, path, fastOpts())
	if res.Outcome != OutcomeOpened || res.FromVersion != 3 || res.ToVersion != 3 {
		t.Fatalf("result = %+v, want opened at version 3", res)
	}
	assertFixtureData(t, ctx, s)
	assertFixtureJobs(t, ctx, s, 3)
	assertFixtureChanges(t, ctx, s, 3)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	// A second run of the same open changes nothing.
	s2, res2 := mustOpen(t, path, fastOpts())
	if res2.Outcome != OutcomeOpened || res2.FromVersion != 3 {
		t.Errorf("re-run result = %+v, want opened at version 3", res2)
	}
	assertFixtureChanges(t, ctx, s2, 3)
}

// ---- change log ---------------------------------------------------------

func TestAppendChangeAssignsDenseIncreasingSeqAndRoundTrips(t *testing.T) {
	ctx := context.Background()
	s, _ := mustOpen(t, dbPath(t), fastOpts())

	if head, err := s.ChangeHead(ctx); err != nil || head != 0 {
		t.Fatalf("head of an empty log = %d err=%v, want 0", head, err)
	}
	if _, ok, err := s.ChangeOldest(ctx); err != nil || ok {
		t.Fatalf("oldest of an empty log: ok=%v err=%v, want ok false", ok, err)
	}

	row := ChangeRow{
		Seq:  99, // ignored
		Kind: "pr", ID: "org/repo#1", Version: 4, HeadSHA: "abc",
		Kinds:  []string{"title_changed", "label_added"},
		Fields: json.RawMessage(`[{"name":"title","before":"A","after":"B"}]`), Origin: "refresh",
		OriginUpdatedAt: ms(500), At: ms(1000),
	}
	var seqs []int64
	if err := s.InTx(ctx, func(tx *Tx) error {
		for range 3 {
			seq, err := tx.AppendChange(ctx, row)
			if err != nil {
				return err
			}
			seqs = append(seqs, seq)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(seqs, []int64{1, 2, 3}) {
		t.Fatalf("seqs = %v, want 1 2 3 (Seq in the row is ignored)", seqs)
	}
	got, err := s.ChangesAfter(ctx, 0, 0)
	if err != nil || len(got) != 3 {
		t.Fatalf("rows = %+v err=%v", got, err)
	}
	row.Seq = 1
	if g := got[0]; g.Seq != 1 || g.Kind != row.Kind || g.ID != row.ID || g.Version != 4 || g.HeadSHA != "abc" ||
		!slices.Equal(g.Kinds, row.Kinds) || string(g.Fields) != string(row.Fields) || g.Origin != "refresh" ||
		!g.OriginUpdatedAt.Equal(row.OriginUpdatedAt) || !g.At.Equal(row.At) {
		t.Errorf("round trip = %+v, want %+v", g, row)
	}
	if head, _ := s.ChangeHead(ctx); head != 3 {
		t.Errorf("head = %d, want 3", head)
	}
	if oldest, ok, _ := s.ChangeOldest(ctx); !ok || oldest != 1 {
		t.Errorf("oldest = %d ok=%v, want 1", oldest, ok)
	}
}

func TestAppendChangeOfSparseFieldsRoundTripsEmpty(t *testing.T) {
	ctx := context.Background()
	s, _ := mustOpen(t, dbPath(t), fastOpts())
	appendRow(t, s, ChangeRow{Kind: "ci", ID: "org/repo#2", At: ms(5)})
	got, err := s.ChangesAfter(ctx, 0, 0)
	if err != nil || len(got) != 1 {
		t.Fatalf("rows = %+v err=%v", got, err)
	}
	if g := got[0]; len(g.Kinds) != 0 || g.Fields != nil || !g.OriginUpdatedAt.IsZero() || g.Origin != "" {
		t.Errorf("row = %+v, want no kinds, no fields, no origin time", g)
	}
}

func TestSequenceNumbersSurvivePruningOfTheNewestRows(t *testing.T) {
	ctx := context.Background()
	s, _ := mustOpen(t, dbPath(t), fastOpts())
	for i := range 5 {
		appendRow(t, s, ChangeRow{Kind: "pr", ID: "org/repo#1", At: ms(int64(1000 + i))})
	}
	// Prune EVERYTHING, newest rows included: the head must not move back and the
	// next number must not reuse one.
	n, err := s.PruneChangesBefore(ctx, ms(9999))
	if err != nil || n != 5 {
		t.Fatalf("pruned = %d err=%v, want 5", n, err)
	}
	if head, _ := s.ChangeHead(ctx); head != 5 {
		t.Errorf("head after pruning everything = %d, want 5 (unaffected by pruning)", head)
	}
	if _, ok, _ := s.ChangeOldest(ctx); ok {
		t.Error("oldest reports a row of an empty log")
	}
	if seq := appendRow(t, s, ChangeRow{Kind: "pr", ID: "org/repo#1", At: ms(20000)}); seq != 6 {
		t.Errorf("next seq after pruning = %d, want 6 (never reused)", seq)
	}
	if head, _ := s.ChangeHead(ctx); head != 6 {
		t.Errorf("head = %d, want 6", head)
	}
	if oldest, ok, _ := s.ChangeOldest(ctx); !ok || oldest != 6 {
		t.Errorf("oldest = %d ok=%v, want 6: a reader sees the pruned gap by comparing with it", oldest, ok)
	}
}

func TestPruneChangesBeforeDeletesOnlyOlderRowsAndCountsThem(t *testing.T) {
	ctx := context.Background()
	s, _ := mustOpen(t, dbPath(t), fastOpts())
	for _, at := range []int64{100, 200, 300, 400} {
		appendRow(t, s, ChangeRow{Kind: "pr", ID: "org/repo#1", At: ms(at)})
	}
	n, err := s.PruneChangesBefore(ctx, ms(300))
	if err != nil || n != 2 {
		t.Fatalf("pruned = %d err=%v, want 2 (the rows before 300, not the row AT 300)", n, err)
	}
	left, _ := s.ChangesAfter(ctx, 0, 0)
	if len(left) != 2 || left[0].Seq != 3 || left[1].Seq != 4 {
		t.Errorf("left = %+v, want seqs 3 and 4", left)
	}
	if oldest, ok, _ := s.ChangeOldest(ctx); !ok || oldest != 3 {
		t.Errorf("oldest = %d ok=%v, want 3", oldest, ok)
	}
	if n, err := s.PruneChangesBefore(ctx, ms(300)); err != nil || n != 0 {
		t.Errorf("second prune = %d err=%v, want 0", n, err)
	}
}

func TestARolledBackAppendConsumesNoNumber(t *testing.T) {
	ctx := context.Background()
	s, _ := mustOpen(t, dbPath(t), fastOpts())
	appendRow(t, s, ChangeRow{Kind: "pr", ID: "org/repo#1", At: ms(1)})

	err := s.InTx(ctx, func(tx *Tx) error {
		for range 3 {
			if _, err := tx.AppendChange(ctx, ChangeRow{Kind: "pr", ID: "org/repo#1", At: ms(2)}); err != nil {
				return err
			}
		}
		return fmt.Errorf("abandon")
	})
	if err == nil {
		t.Fatal("the abandoning transaction reported success")
	}
	if head, _ := s.ChangeHead(ctx); head != 1 {
		t.Errorf("head = %d after a rollback, want 1", head)
	}
	if seq := appendRow(t, s, ChangeRow{Kind: "pr", ID: "org/repo#1", At: ms(3)}); seq != 2 {
		t.Errorf("next seq = %d, want 2 (dense: the rolled-back numbers were not consumed)", seq)
	}
}

func TestChangesAfterHonorsAfterSeqAndLimit(t *testing.T) {
	ctx := context.Background()
	s, _ := mustOpen(t, dbPath(t), fastOpts())
	for i := range 6 {
		appendRow(t, s, ChangeRow{Kind: "pr", ID: "org/repo#1", Version: int64(i), At: ms(int64(i))})
	}
	seqsOf := func(rows []ChangeRow) []int64 {
		var out []int64
		for _, r := range rows {
			out = append(out, r.Seq)
		}
		return out
	}
	for _, tc := range []struct {
		after int64
		limit int
		want  []int64
	}{
		{0, 0, []int64{1, 2, 3, 4, 5, 6}},
		{0, -1, []int64{1, 2, 3, 4, 5, 6}},
		{2, 0, []int64{3, 4, 5, 6}},
		{2, 2, []int64{3, 4}},
		{5, 10, []int64{6}},
		{6, 0, nil},
		{50, 0, nil},
	} {
		got, err := s.ChangesAfter(ctx, tc.after, tc.limit)
		if err != nil || !slices.Equal(seqsOf(got), tc.want) {
			t.Errorf("ChangesAfter(%d, %d) = %v err=%v, want %v", tc.after, tc.limit, seqsOf(got), err, tc.want)
		}
	}
}

func TestLastChangesIsNewestFirstPerEntity(t *testing.T) {
	ctx := context.Background()
	s, _ := mustOpen(t, dbPath(t), fastOpts())
	appendRow(t, s, ChangeRow{Kind: "pr", ID: "org/repo#1", Version: 1, At: ms(1)}) // seq 1
	appendRow(t, s, ChangeRow{Kind: "pr", ID: "org/repo#2", Version: 1, At: ms(2)}) // seq 2
	appendRow(t, s, ChangeRow{Kind: "ci", ID: "org/repo#1", Version: 1, At: ms(3)}) // seq 3: another log kind
	appendRow(t, s, ChangeRow{Kind: "pr", ID: "org/repo#1", Version: 2, At: ms(4)}) // seq 4
	appendRow(t, s, ChangeRow{Kind: "pr", ID: "org/repo#1", Version: 3, At: ms(5)}) // seq 5

	got, err := s.LastChanges(ctx, "pr", "org/repo#1", 2)
	if err != nil || len(got) != 2 || got[0].Seq != 5 || got[1].Seq != 4 {
		t.Errorf("last 2 = %+v err=%v, want seqs 5 then 4", got, err)
	}
	got, _ = s.LastChanges(ctx, "pr", "org/repo#1", 10)
	if len(got) != 3 || got[2].Seq != 1 {
		t.Errorf("last 10 = %+v, want the entity's three rows", got)
	}
	if got, _ := s.LastChanges(ctx, "pr", "org/repo#9", 3); len(got) != 0 {
		t.Errorf("unknown entity = %+v, want none", got)
	}
	if got, _ := s.LastChanges(ctx, "pr", "org/repo#1", 0); len(got) != 0 {
		t.Errorf("n = 0 returned %+v, want none", got)
	}
}

// ---- pending rows -------------------------------------------------------

func TestPendingRowsRoundTripReplaceAndDelete(t *testing.T) {
	ctx := context.Background()
	s, _ := mustOpen(t, dbPath(t), fastOpts())
	p := PendingChange{
		Kind: "pr", ID: "PR_1", Kinds: []string{"title_changed"},
		Fields:  json.RawMessage(`[{"name":"title","before":"A","after":"B"}]`),
		Awaited: json.RawMessage(`[{"group":"detail"}]`),
		HeadSHA: "h1", BaseSHA: "b1", Origin: "query",
		OriginUpdatedAt: ms(10), DetectedAt: ms(20), Deadline: ms(30),
	}
	if err := s.InTx(ctx, func(tx *Tx) error { return tx.PutPending(ctx, p) }); err != nil {
		t.Fatal(err)
	}
	var got PendingChange
	if err := s.InTx(ctx, func(tx *Tx) error {
		var ok bool
		var err error
		got, ok, err = tx.GetPending(ctx, "pr", "PR_1")
		if err == nil && !ok {
			t.Error("pending row missing")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if got.Kind != p.Kind || got.ID != p.ID || !slices.Equal(got.Kinds, p.Kinds) || string(got.Fields) != string(p.Fields) ||
		string(got.Awaited) != string(p.Awaited) || got.HeadSHA != "h1" || got.BaseSHA != "b1" || got.Origin != "query" ||
		!got.OriginUpdatedAt.Equal(ms(10)) || !got.DetectedAt.Equal(ms(20)) || !got.Deadline.Equal(ms(30)) {
		t.Errorf("round trip = %+v, want %+v", got, p)
	}

	// PutPending replaces the row of the same key.
	p.Kinds, p.HeadSHA, p.Deadline = []string{"title_changed", "label_added"}, "h2", ms(99)
	p.Awaited = nil
	if err := s.InTx(ctx, func(tx *Tx) error { return tx.PutPending(ctx, p) }); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListPending(ctx)
	if err != nil || len(list) != 1 || list[0].HeadSHA != "h2" || len(list[0].Kinds) != 2 ||
		list[0].Awaited != nil || !list[0].Deadline.Equal(ms(99)) {
		t.Fatalf("after replace: %+v err=%v, want the one replaced row", list, err)
	}

	// A missing row is reported through the boolean; deleting one is a no-op.
	if err := s.InTx(ctx, func(tx *Tx) error {
		if _, ok, err := tx.GetPending(ctx, "pr", "PR_none"); err != nil || ok {
			t.Errorf("missing pending: ok=%v err=%v", ok, err)
		}
		if err := tx.DeletePending(ctx, "pr", "PR_none"); err != nil {
			t.Errorf("delete of a missing row: %v", err)
		}
		return tx.DeletePending(ctx, "pr", "PR_1")
	}); err != nil {
		t.Fatal(err)
	}
	if n, err := s.CountPending(ctx); err != nil || n != 0 {
		t.Errorf("count after delete = %d err=%v, want 0", n, err)
	}
}

func TestListPendingIsOrderedByDeadlineAndCountMatches(t *testing.T) {
	ctx := context.Background()
	s, _ := mustOpen(t, dbPath(t), fastOpts())
	if err := s.InTx(ctx, func(tx *Tx) error {
		for _, p := range []PendingChange{
			{Kind: "pr", ID: "c", Deadline: ms(300)},
			{Kind: "pr", ID: "a", Deadline: ms(100)},
			{Kind: "ci", ID: "b", Deadline: ms(200)},
		} {
			if err := tx.PutPending(ctx, p); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListPending(ctx)
	if err != nil || len(list) != 3 || list[0].ID != "a" || list[1].ID != "b" || list[2].ID != "c" {
		t.Errorf("list = %+v err=%v, want ascending deadline a, b, c", list, err)
	}
	if n, err := s.CountPending(ctx); err != nil || n != 3 {
		t.Errorf("count = %d err=%v, want 3", n, err)
	}
}

// ---- consumers ----------------------------------------------------------

func TestConsumerRowsRoundTripAndAreOrdered(t *testing.T) {
	ctx := context.Background()
	s, _ := mustOpen(t, dbPath(t), fastOpts())
	rows := []ConsumerRow{
		{Consumer: "zeta", Kind: "pr", Query: "", AckedSeq: 3, SeenAt: ms(100)},
		{Consumer: "alpha", Kind: "pr", Query: "q2", AckedSeq: 1, SeenAt: ms(200)},
		{Consumer: "alpha", Kind: "ci", Query: "", AckedSeq: 2, SeenAt: ms(300)},
		{Consumer: "alpha", Kind: "pr", Query: "q1", AckedSeq: 5, SeenAt: ms(400)},
	}
	for _, r := range rows {
		if err := s.PutConsumer(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	got, ok, err := s.GetConsumer(ctx, "alpha", "pr", "q1")
	if err != nil || !ok || got.AckedSeq != 5 || !got.SeenAt.Equal(ms(400)) {
		t.Errorf("get = %+v ok=%v err=%v", got, ok, err)
	}
	if _, ok, err := s.GetConsumer(ctx, "alpha", "pr", "nope"); err != nil || ok {
		t.Errorf("missing consumer: ok=%v err=%v", ok, err)
	}

	list, err := s.ListConsumers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, r := range list {
		order = append(order, r.Consumer+"/"+r.Kind+"/"+r.Query)
	}
	want := []string{"alpha/ci/", "alpha/pr/q1", "alpha/pr/q2", "zeta/pr/"}
	if !slices.Equal(order, want) {
		t.Errorf("order = %v, want consumer, kind, query: %v", order, want)
	}

	// Put replaces; delete removes one key and is a no-op for an absent one.
	if err := s.PutConsumer(ctx, ConsumerRow{Consumer: "alpha", Kind: "pr", Query: "q1", AckedSeq: 9, SeenAt: ms(500)}); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := s.GetConsumer(ctx, "alpha", "pr", "q1"); got.AckedSeq != 9 || !got.SeenAt.Equal(ms(500)) {
		t.Errorf("after replace = %+v", got)
	}
	if err := s.DeleteConsumer(ctx, "alpha", "pr", "q1"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteConsumer(ctx, "alpha", "pr", "q1"); err != nil {
		t.Errorf("second delete: %v", err)
	}
	if list, _ := s.ListConsumers(ctx); len(list) != 3 {
		t.Errorf("after delete: %d rows, want 3", len(list))
	}
}

func TestDeleteConsumersSeenBeforeRemovesOnlyOlderOnesAndCountsThem(t *testing.T) {
	ctx := context.Background()
	s, _ := mustOpen(t, dbPath(t), fastOpts())
	for i, at := range []int64{100, 200, 300, 400} {
		if err := s.PutConsumer(ctx, ConsumerRow{Consumer: fmt.Sprintf("c%d", i), Kind: "pr", SeenAt: ms(at)}); err != nil {
			t.Fatal(err)
		}
	}
	n, err := s.DeleteConsumersSeenBefore(ctx, ms(300))
	if err != nil || n != 2 {
		t.Fatalf("deleted = %d err=%v, want 2 (seen before 300, not AT 300)", n, err)
	}
	list, _ := s.ListConsumers(ctx)
	if len(list) != 2 || list[0].Consumer != "c2" || list[1].Consumer != "c3" {
		t.Errorf("left = %+v, want c2 and c3", list)
	}
	if n, err := s.DeleteConsumersSeenBefore(ctx, ms(300)); err != nil || n != 0 {
		t.Errorf("second delete = %d err=%v, want 0", n, err)
	}
}

func TestConsumerTxFormsMatchTheStoreForms(t *testing.T) {
	ctx := context.Background()
	s, _ := mustOpen(t, dbPath(t), fastOpts())
	if err := s.InTx(ctx, func(tx *Tx) error {
		for i, at := range []int64{100, 200, 300} {
			if err := tx.PutConsumer(ctx, ConsumerRow{Consumer: fmt.Sprintf("c%d", i), Kind: "pr", Query: "q", SeenAt: ms(at)}); err != nil {
				return err
			}
		}
		if c, ok, err := tx.GetConsumer(ctx, "c1", "pr", "q"); err != nil || !ok || !c.SeenAt.Equal(ms(200)) {
			t.Errorf("tx get = %+v ok=%v err=%v", c, ok, err)
		}
		if err := tx.DeleteConsumer(ctx, "c0", "pr", "q"); err != nil {
			return err
		}
		if n, err := tx.DeleteConsumersSeenBefore(ctx, ms(250)); err != nil || n != 1 {
			t.Errorf("tx delete stale = %d err=%v, want 1", n, err)
		}
		list, err := tx.ListConsumers(ctx)
		if err != nil || len(list) != 1 || list[0].Consumer != "c2" {
			t.Errorf("tx list = %+v err=%v", list, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if list, _ := s.ListConsumers(ctx); len(list) != 1 {
		t.Errorf("committed rows = %+v, want one", list)
	}
}

// ---- store_meta ---------------------------------------------------------

func TestMetaRoundTripAndMissingKey(t *testing.T) {
	ctx := context.Background()
	s, _ := mustOpen(t, dbPath(t), fastOpts())
	if v, ok, err := s.GetMeta(ctx, "k"); err != nil || ok || v != "" {
		t.Fatalf("missing key = %q ok=%v err=%v, want (\"\", false, nil)", v, ok, err)
	}
	if err := s.PutMeta(ctx, "k", "v1"); err != nil {
		t.Fatal(err)
	}
	if v, ok, _ := s.GetMeta(ctx, "k"); !ok || v != "v1" {
		t.Errorf("meta = %q ok=%v", v, ok)
	}
	if err := s.PutMeta(ctx, "k", "v2"); err != nil {
		t.Fatal(err)
	}
	if err := s.PutMeta(ctx, "empty", ""); err != nil {
		t.Fatal(err)
	}
	if v, ok, _ := s.GetMeta(ctx, "k"); !ok || v != "v2" {
		t.Errorf("meta after replace = %q ok=%v", v, ok)
	}
	if v, ok, _ := s.GetMeta(ctx, "empty"); !ok || v != "" {
		t.Errorf("an empty value is present: %q ok=%v", v, ok)
	}

	if err := s.InTx(ctx, func(tx *Tx) error {
		if v, ok, err := tx.GetMeta(ctx, "k"); err != nil || !ok || v != "v2" {
			t.Errorf("tx get = %q ok=%v err=%v", v, ok, err)
		}
		return tx.PutMeta(ctx, "k", "v3")
	}); err != nil {
		t.Fatal(err)
	}
	if v, _, _ := s.GetMeta(ctx, "k"); v != "v3" {
		t.Errorf("meta after tx put = %q, want v3", v)
	}
}
