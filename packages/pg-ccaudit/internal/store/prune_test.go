package store

import (
	"context"
	"database/sql"
	"strconv"
	"testing"
	"time"
)

func seedFile(t *testing.T, db *sql.DB, path, ts string, ingestedAt int64) {
	t.Helper()
	stmts := []string{
		`INSERT INTO files (path, project_dir, size, mtime, lines_ok, lines_bad, ingested_at) VALUES ('` + path + `', '', 0, 0, 1, 0, ` + strconv.FormatInt(ingestedAt, 10) + `)`,
		`INSERT INTO tool_calls (tool_use_id, path, seq, tool_name, input_json) VALUES ('` + path + `-u', '` + path + `', 1, 'Bash', '{}')`,
		`INSERT INTO tool_results (tool_use_id, path, seq, is_error, content_len) VALUES ('` + path + `-u', '` + path + `', 2, 0, 3)`,
		`INSERT INTO assistant_text (path, seq, text) VALUES ('` + path + `', 3, 'x')`,
		`INSERT INTO user_text (path, seq, text_len) VALUES ('` + path + `', 4, 1)`,
	}
	if ts != "" {
		stmts = append(stmts,
			`INSERT INTO events (path, seq, ts, type) VALUES ('`+path+`', 1, '2000-01-01T00:00:00.000Z', 'user')`,
			`INSERT INTO events (path, seq, ts, type) VALUES ('`+path+`', 2, '`+ts+`', 'assistant')`)
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("seed %q: %v", s, err)
		}
	}
}

func countRows(t *testing.T, db *sql.DB, table string) int {
	t.Helper()
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestPruneRemovesOnlyFilesWhoseNewestEventIsOlderThanCutoff(t *testing.T) {
	_, db := tempDB(t, false)
	now := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	cutoff := now.AddDate(0, 0, -90)
	old := now.AddDate(0, 0, -200).Format(TSLayout)
	// Straddles the cutoff: an old first event but a recent newest event.
	recent := now.AddDate(0, 0, -10).Format(TSLayout)
	seedFile(t, db, "old/g1", old, now.Unix())
	seedFile(t, db, "straddle/g1", recent, now.Unix())
	// No events, long untouched: removed. No events, just touched: kept.
	seedFile(t, db, "empty-stale/g1", "", now.AddDate(0, 0, -200).Unix())
	seedFile(t, db, "empty-fresh/g1", "", now.Unix())
	if _, err := db.Exec(`INSERT INTO feed_cursors (name, cursor) VALUES ('c', '42')`); err != nil {
		t.Fatal(err)
	}

	dry, err := Prune(context.Background(), db, cutoff, true)
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if dry.Files != 2 || dry.Rows["events"] != 2 || dry.Rows["tool_calls"] != 2 {
		t.Fatalf("dry run counts wrong: %+v", dry)
	}
	if countRows(t, db, "files") != 4 || countRows(t, db, "events") != 4 {
		t.Fatal("dry run deleted rows")
	}

	got, err := Prune(context.Background(), db, cutoff, false)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if got.Files != 2 || !got.Vacuumed {
		t.Fatalf("want 2 files removed and vacuumed, got %+v", got)
	}
	if n := countRows(t, db, "files"); n != 2 {
		t.Fatalf("files left = %d, want 2", n)
	}
	for table, want := range map[string]int{"events": 2, "tool_calls": 2, "tool_results": 2, "assistant_text": 2, "user_text": 2, "feed_cursors": 1} {
		if n := countRows(t, db, table); n != want {
			t.Errorf("%s rows = %d, want %d", table, n, want)
		}
	}
	var left int
	if err := db.QueryRow(`SELECT COUNT(*) FROM files WHERE path IN ('straddle/g1','empty-fresh/g1')`).Scan(&left); err != nil || left != 2 {
		t.Fatalf("kept the wrong files: %d, %v", left, err)
	}

	// Nothing left to prune: no VACUUM.
	again, err := Prune(context.Background(), db, cutoff, false)
	if err != nil || again.Files != 0 || again.Vacuumed {
		t.Fatalf("second prune = %+v, %v", again, err)
	}
}

func TestPruneCoversTheOptionalThinkingTable(t *testing.T) {
	_, db := tempDB(t, true)
	now := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	seedFile(t, db, "old/g1", now.AddDate(0, 0, -200).Format(TSLayout), now.Unix())
	if _, err := db.Exec(`INSERT INTO thinking (path, seq, text) VALUES ('old/g1', 9, 't')`); err != nil {
		t.Fatal(err)
	}
	if _, err := Prune(context.Background(), db, now.AddDate(0, 0, -90), false); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, db, "thinking"); n != 0 {
		t.Fatalf("thinking rows left = %d", n)
	}
}
