package query

import (
	"context"
	"database/sql"
	"reflect"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-ccaudit/internal/store"
)

// droppedEventsIndexes are the two events indexes the schema no longer creates.
var droppedEventsIndexes = []string{"idx_events_side", "idx_events_src"}

// eventsIndexUsers pins which lens plans go through each candidate index. The
// measurement (every lens, every window and scope flag, with and without ANALYZE)
// found that, contrary to the column inventory, two lenses DO plan through them,
// so neither index can be dropped without changing those lenses first. When a
// lens stops using one, delete its entry here and drop the index in the schema.
var eventsIndexUsers = map[string]string{
	"human-turns":           "idx_events_side",
	"typed-turn-candidates": "idx_events_src",
}

// legacyIndexedFixture is the big fixture with the two dropped indexes put back
// (and prompt_source filled in, so idx_events_src has something to be chosen
// for), opened read-only, with or without ANALYZE statistics.
func legacyIndexedFixture(t *testing.T, analyze bool) *sql.DB {
	t.Helper()
	ro := buildBigFixture(t, bigFixture{sessions: 40, perSess: 160})
	var dbPath string
	rows, err := ro.QueryContext(context.Background(), `PRAGMA database_list`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var seq int
		var name, file string
		if err := rows.Scan(&seq, &name, &file); err != nil {
			t.Fatal(err)
		}
		if name == "main" {
			dbPath = file
		}
	}
	_ = rows.Close()
	// Open the file raw: store.Open would drop the indexes this test adds back.
	w, err := sql.Open(store.DriverName, "file:"+dbPath+"?_pragma=journal_mode(wal)&_pragma=busy_timeout(10000)")
	if err != nil {
		t.Fatal(err)
	}
	w.SetMaxOpenConns(1)
	stmts := []string{
		`UPDATE events SET prompt_source = CASE WHEN seq % 3 = 0 THEN 'typed' WHEN seq % 3 = 1 THEN 'hook' END WHERE type = 'user'`,
		// IF NOT EXISTS: the schema still creates both today; if it ever stops,
		// this keeps measuring what the planner would do with them.
		`CREATE INDEX IF NOT EXISTS idx_events_side ON events(is_sidechain, type)`,
		`CREATE INDEX IF NOT EXISTS idx_events_src ON events(prompt_source)`,
	}
	if analyze {
		stmts = append(stmts, `ANALYZE`)
	}
	for _, s := range stmts {
		if _, err := w.ExecContext(context.Background(), s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	_ = ro.Close()
	db, err := store.OpenReadOnly(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// Measures which lens statements (windowed, unbounded, scoped or not) plan
// through idx_events_side and idx_events_src when both exist, and requires the
// result to equal eventsIndexUsers.
func TestOnlyPinnedLensesPlanThroughCandidateEventsIndexes(t *testing.T) {
	for _, analyze := range []bool{false, true} {
		name := "no stats"
		if analyze {
			name = "analyzed"
		}
		t.Run(name, func(t *testing.T) {
			db := legacyIndexedFixture(t, analyze)
			for _, idx := range droppedEventsIndexes {
				var n int
				if err := db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM sqlite_master WHERE name = ?`, idx).Scan(&n); err != nil || n != 1 {
					t.Fatalf("fixture lacks %s (n=%d, err=%v): the check below would prove nothing", idx, n, err)
				}
			}
			plans := 0
			used := map[string]string{}
			for _, q := range All() {
				stmts := map[string]string{"sql": q.SQL}
				if q.UnboundedSQL != "" {
					stmts["unbounded"] = q.UnboundedSQL
				}
				for _, text := range stmts {
					qq := q
					qq.SQL = text
					for _, w := range windowCases {
						for _, f := range flagCases {
							if !q.Scoped && (f.in != nil || f.ex != nil) {
								continue
							}
							plan := planOf(t, db, qq, w, f)
							plans++
							for _, idx := range droppedEventsIndexes {
								if strings.Contains(plan, idx) {
									if prev, ok := used[q.Name]; ok && prev != idx {
										t.Errorf("%s plans through both %s and %s", q.Name, prev, idx)
									}
									used[q.Name] = idx
								}
							}
						}
					}
				}
			}
			want := map[string]string{}
			for lens, idx := range eventsIndexUsers {
				// human-turns picks idx_events_side only once ANALYZE statistics exist.
				if idx == "idx_events_side" && !analyze {
					continue
				}
				want[lens] = idx
			}
			if !reflect.DeepEqual(used, want) {
				t.Errorf("lenses planning through the candidate indexes = %v, want %v", used, want)
			}
			var side, src int64
			_ = db.QueryRowContext(context.Background(), `SELECT SUM(pgsize) FROM dbstat WHERE name = ?`, "idx_events_side").Scan(&side)
			_ = db.QueryRowContext(context.Background(), `SELECT SUM(pgsize) FROM dbstat WHERE name = ?`, "idx_events_src").Scan(&src)
			t.Logf("fixture index bytes: idx_events_side=%d idx_events_src=%d", side, src)
			if plans < len(All()) {
				t.Fatalf("only %d plans checked for %d lenses", plans, len(All()))
			}
		})
	}
}

// Every registered lens runs (every window and scope flag) against the schema,
// which keeps the two indexes scoped queries need.
func TestEveryLensRunsWithOnlyTheRequiredEventsIndexes(t *testing.T) {
	db := smallFixture(t)
	has := func(name string) bool {
		var n int
		if err := db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name = ?`, name).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n == 1
	}
	for _, idx := range []string{"idx_events_ts", "idx_events_session"} {
		if !has(idx) {
			t.Errorf("slimmed schema lost %s", idx)
		}
	}
	for _, q := range All() {
		for _, w := range windowCases {
			for _, f := range flagCases {
				if !q.Scoped && (f.in != nil || f.ex != nil) {
					continue
				}
				runWith(t, db, q, w, f)
			}
		}
	}
}
