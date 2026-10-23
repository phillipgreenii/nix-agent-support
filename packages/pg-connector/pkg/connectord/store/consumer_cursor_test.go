package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"testing"
)

// Ladder rung 6, consumer cursor per consumer: the consumer table is rebuilt with
// primary key (consumer) and a consumer that held several rows is collapsed to
// the one with the lowest acked_seq (HT-24, HT-36; INV-HOIST-9).

func TestLadderRung6IsNamedAndAppendedAfterRung5(t *testing.T) {
	ladder := BuiltinMigrations()
	if len(ladder) < consumerCursorVersion {
		t.Fatalf("the ladder has %d rungs, want at least %d", len(ladder), consumerCursorVersion)
	}
	if r := ladder[consumerCursorVersion-1]; r.Version != consumerCursorVersion || r.Name != "consumer cursor per consumer" {
		t.Errorf("rung at index %d = %d %q, want %d %q", consumerCursorVersion-1, r.Version, r.Name,
			consumerCursorVersion, "consumer cursor per consumer")
	}
}

// rawConsumer is a consumer row as the table stores it, read without the typed
// accessors so a test sees the bytes the rung wrote.
type rawConsumer struct {
	Consumer, View, Scope string
	AckedSeq, SeenAt      int64
}

// rawConsumers reads the consumer table through the named columns of its shape:
// viewCol and scopeCol are kind and query before rung 6, feed_view and scope
// after it.
func rawConsumers(t *testing.T, db *sql.DB, viewCol, scopeCol string) []rawConsumer {
	t.Helper()
	rows, err := db.Query(fmt.Sprintf(
		`SELECT consumer, %s, %s, acked_seq, seen_at FROM consumer ORDER BY consumer, %s, %s`,
		viewCol, scopeCol, viewCol, scopeCol,
	))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out []rawConsumer
	for rows.Next() {
		var c rawConsumer
		if err := rows.Scan(&c.Consumer, &c.View, &c.Scope, &c.AckedSeq, &c.SeenAt); err != nil {
			t.Fatal(err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// collapsedRecords reads every consumer_collapsed.<consumer> record.
func collapsedRecords(t *testing.T, ctx context.Context, s *Store) map[string][]CollapsedRow {
	t.Helper()
	rows, err := s.DB().QueryContext(ctx, `SELECT key, value FROM store_meta WHERE key LIKE ? ORDER BY key`,
		ConsumerCollapsedPrefix+"%")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string][]CollapsedRow{}
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			t.Fatal(err)
		}
		var dropped []CollapsedRow
		if err := json.Unmarshal([]byte(value), &dropped); err != nil {
			t.Fatalf("record %s = %q: %v", key, value, err)
		}
		out[key[len(ConsumerCollapsedPrefix):]] = dropped
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// logSnapshot is the change log and its head as a test compares them across a
// rung.
func logSnapshot(t *testing.T, ctx context.Context, s *Store) (head int64, rows []ChangeRow) {
	t.Helper()
	head, err := s.ChangeHead(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rows, err = s.ChangesAfter(ctx, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	return head, rows
}

// TestRung6CollapsesAPopulatedVersion5Fixture is HT-24: one-row consumers are
// copied byte for byte, a multi-row consumer keeps the lowest acked_seq with the
// greatest seen_at and its dropped row in store_meta, a consumer on the second view keeps its
// view, a second Open changes nothing, and the change head and log are untouched.
func TestRung6CollapsesAPopulatedVersion5Fixture(t *testing.T) {
	ctx := context.Background()
	fx, ok := fixtures(t)[watchedVersion]
	if !ok {
		t.Fatalf("no version %d fixture", watchedVersion)
	}
	path := dbPath(t)
	copyFile(t, fx, path)

	// What the version 5 file holds, read before any rung runs.
	pre, _, err := Open(path, Options{Migrations: BuiltinMigrations()[:watchedVersion], Synchronous: "OFF"})
	if err != nil {
		t.Fatal(err)
	}
	before := rawConsumers(t, pre.DB(), "kind", "query")
	headBefore, logBefore := logSnapshot(t, ctx, pre)
	if err := pre.Close(); err != nil {
		t.Fatal(err)
	}
	if len(before) != 5 {
		t.Fatalf("the version 5 fixture holds %d consumer rows, want 5: %+v", len(before), before)
	}

	s, res := mustOpen(t, path, fastOpts())
	if res.Outcome != OutcomeMigrated || res.FromVersion != watchedVersion || res.ToVersion != SchemaVersion {
		t.Fatalf("result = %+v, want migrated %d -> %d", res, watchedVersion, SchemaVersion)
	}
	after := rawConsumers(t, s.DB(), "feed_view", "scope")
	want := map[string]rawConsumer{}
	var multi []rawConsumer
	for _, c := range before {
		if c.Consumer == "example-multi" {
			multi = append(multi, c)
			continue
		}
		want[c.Consumer] = c // a one-row consumer is copied unchanged
	}
	if len(multi) != 2 {
		t.Fatalf("the version 5 fixture holds %d rows of example-multi, want 2", len(multi))
	}
	// The row with the lower position is kept, with the greater seen_at of the two.
	kept, dropped := multi[0], multi[1]
	if dropped.AckedSeq < kept.AckedSeq {
		kept, dropped = dropped, kept
	}
	kept.SeenAt = max(multi[0].SeenAt, multi[1].SeenAt)
	want["example-multi"] = kept
	if len(after) != len(want) {
		t.Fatalf("after rung 6: %d rows %+v, want %d", len(after), after, len(want))
	}
	for _, c := range after {
		if w, ok := want[c.Consumer]; !ok || c != w {
			t.Errorf("consumer %s = %+v, want %+v", c.Consumer, c, w)
		}
	}
	// The consumers of the fixture agree with the post-collapse rows the
	// fixture generator seeds, which is what a current fixture holds.
	for _, w := range fixtureWatchedConsumers() {
		if got := want[w.Consumer]; got.View != w.View || got.Scope != w.Scope || got.AckedSeq != w.AckedSeq || got.SeenAt != w.SeenAt.UnixMilli() {
			t.Errorf("post-collapse %s = %+v, want the generator's %+v", w.Consumer, got, w)
		}
	}

	records := collapsedRecords(t, ctx, s)
	if len(records) != 1 || !slices.Equal(records["example-multi"], []CollapsedRow{{View: dropped.View, Scope: dropped.Scope, AckedSeq: dropped.AckedSeq}}) {
		t.Errorf("collapse records = %+v, want only example-multi's dropped %+v", records, dropped)
	}

	headAfter, logAfter := logSnapshot(t, ctx, s)
	if headAfter != headBefore || len(logAfter) != len(logBefore) {
		t.Errorf("head %d -> %d, log rows %d -> %d: rung 6 must not touch the log", headBefore, headAfter, len(logBefore), len(logAfter))
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	// A second run of the same open changes nothing.
	s2, res2 := mustOpen(t, path, fastOpts())
	if res2.Outcome != OutcomeOpened || res2.FromVersion != SchemaVersion {
		t.Errorf("re-run result = %+v, want opened at version %d", res2, SchemaVersion)
	}
	if again := rawConsumers(t, s2.DB(), "feed_view", "scope"); !slices.Equal(again, after) {
		t.Errorf("second open changed the consumers: %+v -> %+v", after, again)
	}
	if again := collapsedRecords(t, ctx, s2); len(again) != 1 || !slices.Equal(again["example-multi"], records["example-multi"]) {
		t.Errorf("second open changed the collapse records: %+v", again)
	}
	if h, l := logSnapshot(t, ctx, s2); h != headBefore || len(l) != len(logBefore) {
		t.Errorf("second open moved the log: head %d, %d rows", h, len(l))
	}
}

// openVersion5With opens a fresh store at the top of the ladder BEFORE rung 6,
// seeds the given version 5 consumer rows, closes it and returns its path.
func openVersion5With(t *testing.T, seed []rawConsumer) string {
	t.Helper()
	path := dbPath(t)
	s, _, err := Open(path, Options{Migrations: BuiltinMigrations()[:watchedVersion], Synchronous: "OFF"})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range seed {
		if _, err := s.DB().Exec(`INSERT INTO consumer (consumer, kind, query, acked_seq, seen_at) VALUES (?, ?, ?, ?, ?)`,
			c.Consumer, c.View, c.Scope, c.AckedSeq, c.SeenAt); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestRung6KeepsTheLowestPositionThenTheLowestViewThenTheLowestScope is HT-36's
// store half: the kept row is the lowest acked_seq (ties: lowest kind, then lowest
// query), seen_at is the greatest of the consumer's rows, and the record names
// every row not kept with its view, scope and position, in (view, scope) order.
func TestRung6KeepsTheLowestPositionThenTheLowestViewThenTheLowestScope(t *testing.T) {
	ctx := context.Background()
	path := openVersion5With(t, []rawConsumer{
		// A three-way tie on position: (example-view-a, q9) has the lowest view.
		{Consumer: "tie", View: "example-view-b", Scope: "q2", AckedSeq: 3, SeenAt: 10},
		{Consumer: "tie", View: "example-view-a", Scope: "q9", AckedSeq: 3, SeenAt: 20},
		{Consumer: "tie", View: "example-view-b", Scope: "q1", AckedSeq: 3, SeenAt: 5},
		// A tie within one view: q1 has the lowest scope.
		{Consumer: "scoped", View: "example-view-b", Scope: "q2", AckedSeq: 8, SeenAt: 40},
		{Consumer: "scoped", View: "example-view-b", Scope: "q1", AckedSeq: 8, SeenAt: 30},
		// No tie: the lowest position wins even though it is the later row.
		{Consumer: "lowest", View: "example-view-a", Scope: "", AckedSeq: 12, SeenAt: 50},
		{Consumer: "lowest", View: "example-view-b", Scope: "q5", AckedSeq: 2, SeenAt: 45},
		// A one-row consumer, whose position and last sighting stay as they are.
		{Consumer: "single", View: "example-view-b", Scope: "q3", AckedSeq: 0, SeenAt: 60},
	})
	s, res := mustOpen(t, path, fastOpts())
	if res.Outcome != OutcomeMigrated || res.ToVersion != SchemaVersion {
		t.Fatalf("result = %+v", res)
	}
	got := rawConsumers(t, s.DB(), "feed_view", "scope")
	want := []rawConsumer{
		{Consumer: "lowest", View: "example-view-b", Scope: "q5", AckedSeq: 2, SeenAt: 50},
		{Consumer: "scoped", View: "example-view-b", Scope: "q1", AckedSeq: 8, SeenAt: 40},
		{Consumer: "single", View: "example-view-b", Scope: "q3", AckedSeq: 0, SeenAt: 60},
		{Consumer: "tie", View: "example-view-a", Scope: "q9", AckedSeq: 3, SeenAt: 20},
	}
	if !slices.Equal(got, want) {
		t.Errorf("consumers = %+v, want %+v", got, want)
	}
	// "tie" keeps seen_at 20 only because 20 is the greatest of 10, 20 and 5.
	records := collapsedRecords(t, ctx, s)
	wantRecords := map[string][]CollapsedRow{
		"tie":    {{View: "example-view-b", Scope: "q1", AckedSeq: 3}, {View: "example-view-b", Scope: "q2", AckedSeq: 3}},
		"scoped": {{View: "example-view-b", Scope: "q2", AckedSeq: 8}},
		"lowest": {{View: "example-view-a", Scope: "", AckedSeq: 12}},
	}
	if len(records) != len(wantRecords) {
		t.Fatalf("records = %+v, want exactly %v", records, []string{"tie", "scoped", "lowest"})
	}
	for name, w := range wantRecords {
		if !slices.Equal(records[name], w) {
			t.Errorf("record of %s = %+v, want %+v", name, records[name], w)
		}
	}
	if _, has := records["single"]; has {
		t.Error("a one-row consumer left a collapse record")
	}
}

// TestRung6OnAnEmptyConsumerTableLeavesTheCurrentShape covers a store with no
// cursor at all: the table is rebuilt with the one-row key and nothing is
// recorded.
func TestRung6OnAnEmptyConsumerTableLeavesTheCurrentShape(t *testing.T) {
	ctx := context.Background()
	path := openVersion5With(t, nil)
	s, _ := mustOpen(t, path, fastOpts())
	if got := columnNames(t, s, "consumer"); !slices.Equal(got, []string{"consumer", "feed_view", "scope", "acked_seq", "seen_at"}) {
		t.Errorf("consumer columns = %v", got)
	}
	if n := len(collapsedRecords(t, ctx, s)); n != 0 {
		t.Errorf("%d collapse records on an empty table", n)
	}
	// The primary key is the consumer alone: a second row of one name replaces it.
	for _, scope := range []string{"a", "b"} {
		if err := s.PutConsumer(ctx, ConsumerRow{Consumer: "c", View: "example-view-b", Scope: scope, SeenAt: ms(1)}); err != nil {
			t.Fatal(err)
		}
	}
	if list, err := s.ListConsumers(ctx); err != nil || len(list) != 1 || list[0].Scope != "b" {
		t.Errorf("consumers = %+v err=%v, want one row bound to scope b", list, err)
	}
	var idx int
	if err := s.DB().QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'index' AND name = 'consumer_seen_at'`).Scan(&idx); err != nil || idx != 1 {
		t.Errorf("index consumer_seen_at present = %d err=%v, want 1", idx, err)
	}
}
