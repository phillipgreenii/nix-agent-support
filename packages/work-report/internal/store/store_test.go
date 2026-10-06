package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"
	_ "time/tzdata" // the DST test loads a named zone

	_ "modernc.org/sqlite"
)

var ctx = context.Background()

func openTemp(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "state", "store.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func utc(y int, m time.Month, d, h, mi, sec int) time.Time {
	return time.Date(y, m, d, h, mi, sec, 0, time.UTC)
}

func entry(id string) Entry {
	return Entry{
		ID: id, ExternalID: "ext-" + id, SourceID: "example-backend", Type: "change",
		OccurredAt: utc(2026, 3, 1, 12, 0, 0), Summary: "summary of " + id,
		URL: "https://example.invalid/" + id, Labels: []string{"alpha"},
		Fields: json.RawMessage(`{"k":"v"}`),
	}
}

func mustAppend(t *testing.T, s *Store, e Entry, now time.Time) AppendResult {
	t.Helper()
	r, err := s.Append(ctx, e, now)
	if err != nil {
		t.Fatalf("Append(%s): %v", e.ID, err)
	}
	return r
}

func rowCount(t *testing.T, s *Store, table string) int {
	t.Helper()
	var n int
	if err := s.sql.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func ids(es []Entry) []string {
	out := make([]string, len(es))
	for i, e := range es {
		out[i] = e.ID
	}
	return out
}

func TestOpenCreatesSchemaWALAndVersion(t *testing.T) {
	s := openTemp(t)

	var mode string
	if err := s.sql.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("journal_mode = %q, %v; want wal", mode, err)
	}
	for _, tbl := range []string{"entry", "entry_label", "pull", "report", "meta"} {
		if rowCount(t, s, tbl) != 0 && tbl != "meta" {
			t.Errorf("table %s not empty on a fresh store", tbl)
		}
	}
	for _, idx := range []string{"entry_id", "entry_occurred_at", "entry_type", "entry_source_id", "entry_label_label"} {
		var n int
		if err := s.sql.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name=?`, idx).Scan(&n); err != nil || n != 1 {
			t.Errorf("index %s missing (n=%d, err=%v)", idx, n, err)
		}
	}
	var v string
	if err := s.sql.QueryRow(`SELECT value FROM meta WHERE key='schema_version'`).Scan(&v); err != nil || v != "1" {
		t.Errorf("meta schema_version = %q, %v; want 1", v, err)
	}
	var uv int
	if err := s.sql.QueryRow("PRAGMA user_version").Scan(&uv); err != nil || uv != schemaVersion {
		t.Errorf("user_version = %d, %v; want %d", uv, err, schemaVersion)
	}
}

func TestReopenIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	mustAppend(t, s, entry("a"), utc(2026, 3, 2, 0, 0, 0))
	_ = s.Close()
	s2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = s2.Close() }()
	got, err := s2.Query(ctx, Filter{})
	if err != nil || len(got) != 1 {
		t.Fatalf("after reopen got %d entries, %v; want 1", len(got), err)
	}
}

func TestOpenRefusesNewerSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("PRAGMA user_version = 99"); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	if _, err := Open(path); err == nil || !strings.Contains(err.Error(), "newer") {
		t.Fatalf("Open of a newer schema: err = %v; want a 'newer' refusal", err)
	}
}

func TestAppendRoundTrip(t *testing.T) {
	s := openTemp(t)
	e := entry("a")
	e.Labels = []string{"zeta", "alpha", "alpha"}
	now := utc(2026, 3, 2, 9, 30, 0)
	if r := mustAppend(t, s, e, now); r != Appended {
		t.Fatalf("first Append = %v; want Appended", r)
	}
	got, err := s.Query(ctx, Filter{})
	if err != nil || len(got) != 1 {
		t.Fatalf("Query = %v, %v", got, err)
	}
	g := got[0]
	if g.Seq == 0 || g.ContentHash == "" {
		t.Errorf("Seq/ContentHash not set: %+v", g)
	}
	if !g.IngestedAt.Equal(now) || !g.OccurredAt.Equal(e.OccurredAt) {
		t.Errorf("times: ingested %v occurred %v", g.IngestedAt, g.OccurredAt)
	}
	if !reflect.DeepEqual(g.Labels, []string{"alpha", "zeta"}) {
		t.Errorf("Labels = %v; want sorted, de-duplicated", g.Labels)
	}
	if g.ID != "a" || g.ExternalID != "ext-a" || g.SourceID != "example-backend" || g.Type != "change" ||
		g.Summary != e.Summary || g.URL != e.URL || string(g.Fields) != `{"k":"v"}` {
		t.Errorf("round trip mismatch: %+v", g)
	}
	if rowCount(t, s, "entry_label") != 2 {
		t.Errorf("entry_label rows = %d; want 2 (de-duplicated)", rowCount(t, s, "entry_label"))
	}
}

func TestAppendRejectsEmptyIDAndBadFields(t *testing.T) {
	s := openTemp(t)
	if _, err := s.Append(ctx, Entry{OccurredAt: utc(2026, 3, 1, 0, 0, 0)}, time.Now()); err == nil {
		t.Error("empty id: want error")
	}
	e := entry("a")
	e.Fields = json.RawMessage(`{not json`)
	if _, err := s.Append(ctx, e, time.Now()); err == nil {
		t.Error("invalid fields JSON: want error")
	}
	if rowCount(t, s, "entry") != 0 {
		t.Error("rejected appends wrote rows")
	}
}

func TestIdenticalReobservationIsUnchanged(t *testing.T) {
	s := openTemp(t)
	e := entry("a")
	mustAppend(t, s, e, utc(2026, 3, 2, 0, 0, 0))
	if r := mustAppend(t, s, e, utc(2026, 3, 2, 1, 0, 0)); r != Unchanged {
		t.Fatalf("re-observation = %v; want Unchanged", r)
	}
	if n := rowCount(t, s, "entry"); n != 1 {
		t.Fatalf("entry rows = %d; want 1", n)
	}
	if n := rowCount(t, s, "entry_label"); n != 1 {
		t.Fatalf("entry_label rows = %d; want 1", n)
	}
}

func TestReobservationIgnoresOrderingOfLabelsAndKeys(t *testing.T) {
	s := openTemp(t)
	a := entry("a")
	a.Labels = []string{"x", "y"}
	a.Fields = json.RawMessage(`{"b":1,"a":{"d":2,"c":3}}`)
	mustAppend(t, s, a, utc(2026, 3, 2, 0, 0, 0))
	b := a
	b.Labels = []string{"y", "x", "x"}
	b.Fields = json.RawMessage(` { "a" : {"c":3,"d":2}, "b":1 } `)
	if r := mustAppend(t, s, b, utc(2026, 3, 2, 1, 0, 0)); r != Unchanged {
		t.Fatalf("reordered labels/keys = %v; want Unchanged", r)
	}
}

func TestOnlyAsOfOrStaleChangeIsUnchanged(t *testing.T) {
	s := openTemp(t)
	e := entry("a")
	e.Fields = json.RawMessage(`{"k":"v","as_of":"2026-03-01T00:00:00Z","stale":false}`)
	mustAppend(t, s, e, utc(2026, 3, 2, 0, 0, 0))

	e.Fields = json.RawMessage(`{"k":"v","as_of":"2026-03-02T00:00:00Z","stale":false}`)
	if r := mustAppend(t, s, e, utc(2026, 3, 2, 1, 0, 0)); r != Unchanged {
		t.Errorf("only as_of changed = %v; want Unchanged", r)
	}
	e.Fields = json.RawMessage(`{"k":"v","as_of":"2026-03-02T00:00:00Z","stale":true}`)
	if r := mustAppend(t, s, e, utc(2026, 3, 2, 2, 0, 0)); r != Unchanged {
		t.Errorf("only stale changed = %v; want Unchanged", r)
	}
	e.Fields = json.RawMessage(`{"k":"v"}`)
	if r := mustAppend(t, s, e, utc(2026, 3, 2, 3, 0, 0)); r != Unchanged {
		t.Errorf("as_of and stale absent = %v; want Unchanged", r)
	}
	e.Fields = json.RawMessage(`{"k":"w","as_of":"2026-03-02T00:00:00Z"}`)
	if r := mustAppend(t, s, e, utc(2026, 3, 2, 4, 0, 0)); r != Appended {
		t.Errorf("another field changed = %v; want Appended", r)
	}
	if n := rowCount(t, s, "entry"); n != 2 {
		t.Errorf("entry rows = %d; want 2", n)
	}
}

func TestEachHashedPropertyChangeAppends(t *testing.T) {
	changes := map[string]func(*Entry){
		"type":        func(e *Entry) { e.Type = "other" },
		"occurred_at": func(e *Entry) { e.OccurredAt = e.OccurredAt.Add(time.Second) },
		"labels":      func(e *Entry) { e.Labels = append(e.Labels, "beta") },
		"summary":     func(e *Entry) { e.Summary = "changed" },
		"url":         func(e *Entry) { e.URL = "https://example.invalid/other" },
		"fields":      func(e *Entry) { e.Fields = json.RawMessage(`{"k":"other"}`) },
	}
	for name, mutate := range changes {
		t.Run(name, func(t *testing.T) {
			s := openTemp(t)
			e := entry("a")
			mustAppend(t, s, e, utc(2026, 3, 2, 0, 0, 0))
			mutate(&e)
			if r := mustAppend(t, s, e, utc(2026, 3, 2, 1, 0, 0)); r != Appended {
				t.Fatalf("changing %s = %v; want Appended", name, r)
			}
			if n := rowCount(t, s, "entry"); n != 2 {
				t.Fatalf("entry rows = %d; want 2 (history kept)", n)
			}
		})
	}
}

func TestChangedSummaryAppendsAndWins(t *testing.T) {
	s := openTemp(t)
	e := entry("a")
	mustAppend(t, s, e, utc(2026, 3, 2, 0, 0, 0))
	e.Summary = "a new summary"
	if r := mustAppend(t, s, e, utc(2026, 3, 2, 1, 0, 0)); r != Appended {
		t.Fatalf("changed summary = %v; want Appended", r)
	}
	got, err := s.Query(ctx, Filter{})
	if err != nil || len(got) != 1 {
		t.Fatalf("Query = %d entries, %v; want exactly 1 (latest-wins)", len(got), err)
	}
	if got[0].Summary != "a new summary" {
		t.Errorf("latest summary = %q; want the new one", got[0].Summary)
	}
	if n := rowCount(t, s, "entry"); n != 2 {
		t.Errorf("entry rows = %d; want 2 (append-only)", n)
	}
}

func TestLatestWinsOrdering(t *testing.T) {
	t.Run("greater occurred_at wins over later ingestion", func(t *testing.T) {
		s := openTemp(t)
		e := entry("a")
		e.OccurredAt, e.Summary = utc(2026, 3, 5, 0, 0, 0), "newer occurrence"
		mustAppend(t, s, e, utc(2026, 3, 6, 0, 0, 0))
		e.OccurredAt, e.Summary = utc(2026, 3, 4, 0, 0, 0), "older occurrence"
		mustAppend(t, s, e, utc(2026, 3, 9, 0, 0, 0))
		got, _ := s.Query(ctx, Filter{})
		if len(got) != 1 || got[0].Summary != "newer occurrence" {
			t.Fatalf("got %+v; want the greater occurred_at to win", got)
		}
	})
	t.Run("tie on occurred_at is broken by ingested_at", func(t *testing.T) {
		s := openTemp(t)
		e := entry("a")
		// The later-ingested row is inserted FIRST, so seq cannot be what wins.
		e.Summary = "ingested later"
		mustAppend(t, s, e, utc(2026, 3, 9, 0, 0, 0))
		e.Summary = "ingested earlier"
		mustAppend(t, s, e, utc(2026, 3, 3, 0, 0, 0))
		got, _ := s.Query(ctx, Filter{})
		if len(got) != 1 || got[0].Summary != "ingested later" {
			t.Fatalf("got %+v; want the greater ingested_at to win", got)
		}
	})
	t.Run("tie on both is broken by seq", func(t *testing.T) {
		s := openTemp(t)
		e := entry("a")
		now := utc(2026, 3, 3, 0, 0, 0)
		e.Summary = "first"
		mustAppend(t, s, e, now)
		e.Summary = "second"
		mustAppend(t, s, e, now)
		got, _ := s.Query(ctx, Filter{})
		if len(got) != 1 || got[0].Summary != "second" {
			t.Fatalf("got %+v; want the greater seq to win", got)
		}
	})
	t.Run("one row per id", func(t *testing.T) {
		s := openTemp(t)
		for _, id := range []string{"a", "b", "c"} {
			for i := 0; i < 3; i++ {
				e := entry(id)
				e.Summary = id + string(rune('0'+i))
				mustAppend(t, s, e, utc(2026, 3, 3, i, 0, 0))
			}
		}
		got, _ := s.Query(ctx, Filter{})
		if !reflect.DeepEqual(ids(got), []string{"a", "b", "c"}) {
			t.Fatalf("ids = %v; want one per id", ids(got))
		}
	})
}

func TestFilterIsAppliedToTheLatestRowOnly(t *testing.T) {
	s := openTemp(t)
	e := entry("a")
	e.Labels = []string{"old"}
	mustAppend(t, s, e, utc(2026, 3, 2, 0, 0, 0))
	e.Labels = []string{"new"}
	mustAppend(t, s, e, utc(2026, 3, 3, 0, 0, 0))

	if got, _ := s.Query(ctx, Filter{Labels: []string{"old"}}); len(got) != 0 {
		t.Errorf("superseded label matched: %v", ids(got))
	}
	if got, _ := s.Query(ctx, Filter{Labels: []string{"new"}}); len(got) != 1 {
		t.Errorf("current label did not match: %v", ids(got))
	}
}

func seedNarrowing(t *testing.T) *Store {
	t.Helper()
	s := openTemp(t)
	mk := func(id, typ, src string, labels ...string) {
		e := entry(id)
		e.Type, e.SourceID, e.Labels = typ, src, labels
		mustAppend(t, s, e, utc(2026, 3, 2, 0, 0, 0))
	}
	mk("1", "change", "src-a", "red", "blue")
	mk("2", "change", "src-b", "red")
	mk("3", "review", "src-a", "blue")
	mk("4", "review", "src-b")
	return s
}

func TestLabelIndexQueries(t *testing.T) {
	s := seedNarrowing(t)
	cases := []struct {
		name   string
		labels []string
		want   []string
	}{
		{"one label", []string{"red"}, []string{"1", "2"}},
		{"another label", []string{"blue"}, []string{"1", "3"}},
		{"OR within labels", []string{"red", "blue"}, []string{"1", "2", "3"}},
		{"unknown label", []string{"green"}, []string{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := s.Query(ctx, Filter{Labels: c.labels})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(ids(got), c.want) {
				t.Fatalf("ids = %v; want %v", ids(got), c.want)
			}
		})
	}
}

func TestNarrowingAndAcrossOrWithin(t *testing.T) {
	s := seedNarrowing(t)
	cases := []struct {
		name string
		f    Filter
		want []string
	}{
		{"no filter", Filter{}, []string{"1", "2", "3", "4"}},
		{"by id", Filter{ID: "3"}, []string{"3"}},
		{"OR within types", Filter{Types: []string{"change", "review"}}, []string{"1", "2", "3", "4"}},
		{"OR within sources", Filter{Sources: []string{"src-a", "src-b"}}, []string{"1", "2", "3", "4"}},
		{"AND types x sources", Filter{Types: []string{"change"}, Sources: []string{"src-a"}}, []string{"1"}},
		{"AND types x labels", Filter{Types: []string{"review"}, Labels: []string{"red", "blue"}}, []string{"3"}},
		{"AND all three", Filter{Types: []string{"change"}, Sources: []string{"src-b"}, Labels: []string{"red", "blue"}}, []string{"2"}},
		{"AND id x type mismatch", Filter{ID: "1", Types: []string{"review"}}, []string{}},
		{"AND to nothing", Filter{Sources: []string{"src-a"}, Labels: []string{"red"}, Types: []string{"review"}}, []string{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := s.Query(ctx, c.f)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(ids(got), c.want) {
				t.Fatalf("ids = %v; want %v", ids(got), c.want)
			}
		})
	}
}

func TestRangeBoundariesAcrossDST(t *testing.T) {
	la, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Skipf("zone unavailable: %v", err)
	}
	// 2026-03-08 is the US spring-forward day: the local day is 23 hours long.
	since := time.Date(2026, 3, 8, 0, 0, 0, 0, la)  // 08:00Z
	before := time.Date(2026, 3, 9, 0, 0, 0, 0, la) // 07:00Z
	if got := before.Sub(since); got != 23*time.Hour {
		t.Fatalf("fixture: day length = %v; want 23h", got)
	}

	s := openTemp(t)
	at := map[string]time.Time{
		"just-before-since": since.Add(-time.Nanosecond),
		"at-since":          since,
		"mid-day":           utc(2026, 3, 8, 20, 0, 0),
		"last-instant":      before.Add(-time.Nanosecond),
		"at-before":         before,
	}
	for id, ts := range at {
		e := entry(id)
		e.OccurredAt = ts
		mustAppend(t, s, e, utc(2026, 3, 10, 0, 0, 0))
	}
	got, err := s.Query(ctx, Filter{Since: since, Before: before})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"at-since", "mid-day", "last-instant"}
	if !reflect.DeepEqual(ids(got), want) {
		t.Fatalf("ids = %v; want %v (Since inclusive, Before exclusive)", ids(got), want)
	}
	// Instants come back as UTC regardless of the zone they were supplied in.
	for _, e := range got {
		if e.OccurredAt.Location() != time.UTC {
			t.Errorf("%s OccurredAt zone = %v; want UTC", e.ID, e.OccurredAt.Location())
		}
	}
	// A day after the transition is 24h and uses the other offset: 07:00Z .. 07:00Z.
	next := time.Date(2026, 3, 9, 0, 0, 0, 0, la)
	after := time.Date(2026, 3, 10, 0, 0, 0, 0, la)
	got, _ = s.Query(ctx, Filter{Since: next, Before: after})
	if !reflect.DeepEqual(ids(got), []string{"at-before"}) {
		t.Fatalf("next day ids = %v; want [at-before]", ids(got))
	}
}

func TestOpenStartAndOpenEnd(t *testing.T) {
	s := openTemp(t)
	for i, id := range []string{"a", "b", "c"} {
		e := entry(id)
		e.OccurredAt = utc(2026, 3, 1+i, 0, 0, 0)
		mustAppend(t, s, e, utc(2026, 3, 10, 0, 0, 0))
	}
	got, _ := s.Query(ctx, Filter{OpenStart: true, Before: utc(2026, 3, 3, 0, 0, 0)})
	if !reflect.DeepEqual(ids(got), []string{"a", "b"}) {
		t.Errorf("open start ids = %v; want [a b]", ids(got))
	}
	got, _ = s.Query(ctx, Filter{Since: utc(2026, 3, 2, 0, 0, 0)})
	if !reflect.DeepEqual(ids(got), []string{"b", "c"}) {
		t.Errorf("open end ids = %v; want [b c]", ids(got))
	}
}

// holdWriteLock opens a second connection to path and keeps a write
// transaction open until the test ends.
func holdWriteLock(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?_txlock=immediate&_pragma=journal_mode(WAL)&_pragma=busy_timeout(100)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("take write lock: %v", err)
	}
	if _, err := tx.Exec(`CREATE TABLE IF NOT EXISTS lock_holder (x INTEGER)`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(); _ = db.Close() })
}

func TestLockedSurfacesAsErrLocked(t *testing.T) {
	old := busyTimeoutMillis
	busyTimeoutMillis = 100
	t.Cleanup(func() { busyTimeoutMillis = old })

	path := filepath.Join(t.TempDir(), "s.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	holdWriteLock(t, path)

	if _, err := s.Append(ctx, entry("a"), time.Now()); !errors.Is(err, ErrLocked) {
		t.Errorf("Append: err = %v; want ErrLocked", err)
	}
	if err := s.RecordPull(ctx, PullRow{Source: "x", Status: "succeeded", StartedAt: time.Now(), EndedAt: time.Now()}); !errors.Is(err, ErrLocked) {
		t.Errorf("RecordPull: err = %v; want ErrLocked", err)
	}
	if err := s.RecordReport(ctx, ReportRow{Kind: "k", GeneratedAt: time.Now()}); !errors.Is(err, ErrLocked) {
		t.Errorf("RecordReport: err = %v; want ErrLocked", err)
	}
}

func TestOpenLockedSurfacesAsErrLocked(t *testing.T) {
	old := busyTimeoutMillis
	busyTimeoutMillis = 100
	t.Cleanup(func() { busyTimeoutMillis = old })

	// A database that still needs its migration, with a writer holding the lock.
	path := filepath.Join(t.TempDir(), "s.db")
	holdWriteLock(t, path)
	if _, err := Open(path); !errors.Is(err, ErrLocked) {
		t.Fatalf("Open: err = %v; want ErrLocked", err)
	}
}

func TestRecordPullAndStatus(t *testing.T) {
	s := openTemp(t)
	st, err := s.Status(ctx)
	if err != nil || len(st.Sources) != 0 || st.LastReport != nil {
		t.Fatalf("empty Status = %+v, %v", st, err)
	}

	pull := func(src, status, reason string, end time.Time) {
		t.Helper()
		err := s.RecordPull(ctx, PullRow{
			Source: src, Since: utc(2026, 3, 1, 0, 0, 0), Before: utc(2026, 3, 2, 0, 0, 0),
			StartedAt: end.Add(-time.Minute), EndedAt: end, Status: status,
			Count: 3, Unchanged: 1, Rejected: 2, Truncated: true, Reason: reason,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	pull("src-a", "succeeded", "", utc(2026, 3, 2, 10, 0, 0))
	pull("src-a", "degraded", "backend timed out", utc(2026, 3, 2, 11, 0, 0))
	pull("src-b", "succeeded", "", utc(2026, 3, 2, 9, 0, 0))
	pull("src-c", "disabled", "disabled by config", utc(2026, 3, 2, 8, 0, 0))

	mk := func(id, src string, at time.Time) {
		e := entry(id)
		e.SourceID, e.OccurredAt = src, at
		mustAppend(t, s, e, utc(2026, 3, 2, 12, 0, 0))
	}
	mk("1", "src-a", utc(2026, 3, 1, 1, 0, 0))
	mk("2", "src-a", utc(2026, 3, 1, 5, 0, 0))
	mk("3", "src-b", utc(2026, 3, 1, 3, 0, 0))
	// A superseded row must not inflate count or the newest occurred_at.
	e := entry("2")
	e.SourceID, e.OccurredAt, e.Summary = "src-a", utc(2026, 3, 1, 5, 0, 0), "revised"
	mustAppend(t, s, e, utc(2026, 3, 2, 13, 0, 0))

	rep := ReportRow{
		Since: utc(2026, 3, 1, 0, 0, 0), Before: utc(2026, 3, 2, 0, 0, 0), Kind: "activity",
		NarrowingJSON: `{"labels":["x"]}`, GeneratedAt: utc(2026, 3, 2, 14, 0, 0), Generator: "test:1", Content: "hello",
	}
	if err := s.RecordReport(ctx, ReportRow{Kind: "activity", GeneratedAt: utc(2026, 3, 2, 13, 0, 0), Content: "older"}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordReport(ctx, rep); err != nil {
		t.Fatal(err)
	}

	st, err = s.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []SourceStatus{
		{
			Source: "src-a", LastStatus: "degraded", LastReason: "backend timed out",
			LastAt: utc(2026, 3, 2, 11, 0, 0), LastSuccessAt: utc(2026, 3, 2, 10, 0, 0),
			NewestOccurredAt: utc(2026, 3, 1, 5, 0, 0), EntryCount: 2,
		},
		{
			Source: "src-b", LastStatus: "succeeded", LastAt: utc(2026, 3, 2, 9, 0, 0),
			LastSuccessAt: utc(2026, 3, 2, 9, 0, 0), NewestOccurredAt: utc(2026, 3, 1, 3, 0, 0), EntryCount: 1,
		},
		{Source: "src-c", LastStatus: "disabled", LastReason: "disabled by config", LastAt: utc(2026, 3, 2, 8, 0, 0)},
	}
	if len(st.Sources) != len(want) {
		t.Fatalf("sources = %+v; want %d", st.Sources, len(want))
	}
	for i, w := range want {
		g := st.Sources[i]
		if g.Source != w.Source || g.LastStatus != w.LastStatus || g.LastReason != w.LastReason ||
			!g.LastAt.Equal(w.LastAt) || !g.LastSuccessAt.Equal(w.LastSuccessAt) ||
			!g.NewestOccurredAt.Equal(w.NewestOccurredAt) || g.EntryCount != w.EntryCount {
			t.Errorf("source %d:\n got  %+v\n want %+v", i, g, w)
		}
	}
	if st.StoreSizeBytes <= 0 {
		t.Errorf("StoreSizeBytes = %d; want > 0", st.StoreSizeBytes)
	}
	if st.LastReport == nil || st.LastReport.Content != "hello" || st.LastReport.Kind != "activity" ||
		st.LastReport.NarrowingJSON != rep.NarrowingJSON || st.LastReport.Generator != "test:1" ||
		!st.LastReport.GeneratedAt.Equal(rep.GeneratedAt) || !st.LastReport.Since.Equal(rep.Since) ||
		!st.LastReport.Before.Equal(rep.Before) {
		t.Errorf("LastReport = %+v; want the newest report", st.LastReport)
	}

	// The pull row keeps the open-ended range and every counter.
	var since string
	var count, unchanged, rejected, truncated int
	if err := s.sql.QueryRow(`SELECT since, count, unchanged, rejected, truncated FROM pull WHERE source='src-c'`).
		Scan(&since, &count, &unchanged, &rejected, &truncated); err != nil {
		t.Fatal(err)
	}
	if count != 3 || unchanged != 1 || rejected != 2 || truncated != 1 {
		t.Errorf("pull counters = %d/%d/%d/%d", count, unchanged, rejected, truncated)
	}
}

func TestRecordPullOpenEndedRangeAndMeta(t *testing.T) {
	s := openTemp(t)
	end := utc(2026, 3, 2, 10, 0, 0)
	if err := s.RecordPull(ctx, PullRow{Source: "src-a", StartedAt: end, EndedAt: end, Status: "succeeded"}); err != nil {
		t.Fatal(err)
	}
	var since string
	if err := s.sql.QueryRow(`SELECT since FROM pull`).Scan(&since); err != nil || since != "" {
		t.Errorf("open-ended since = %q, %v; want empty", since, err)
	}
	if err := s.RecordReport(ctx, ReportRow{Kind: "activity", GeneratedAt: end.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{"last_pull": formatTime(end), "last_report": formatTime(end.Add(time.Hour))} {
		var v string
		if err := s.sql.QueryRow(`SELECT value FROM meta WHERE key=?`, key).Scan(&v); err != nil || v != want {
			t.Errorf("meta %s = %q, %v; want %q", key, v, err, want)
		}
	}
	st, _ := s.Status(ctx)
	if st.LastReport == nil || !st.LastReport.Since.IsZero() {
		t.Errorf("LastReport open-ended since = %+v", st.LastReport)
	}
}

// INV-APPEND-1: no code path in this package updates or deletes an entry or
// entry_label row. A static guard over the non-test sources.
func TestNoCodePathMutatesEntries(t *testing.T) {
	forbidden := regexp.MustCompile(`(?is)\b(update|delete\s+from|insert\s+or\s+replace\s+into|replace\s+into|drop\s+table)\s+(entry|entry_label)\b`)
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("glob: %v, %v", files, err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if m := forbidden.Find(b); m != nil {
			t.Errorf("%s contains an entry mutation: %q", f, m)
		}
	}
}
