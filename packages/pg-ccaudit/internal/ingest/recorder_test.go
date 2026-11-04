package ingest

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/pg-ccaudit/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/client"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/client/clienttest"
)

// These tests run the real recorder client against the recorder's in-process
// test server (a real store, parser, feed and socket; no started daemon).

const (
	rel     = "projects/example-proj/example-session-1.jsonl"
	callOne = `{"type":"assistant","uuid":"a1","sessionId":"example-session-1","timestamp":"2026-10-29T20:00:00Z","message":{"id":"m1","role":"assistant","content":[{"type":"tool_use","id":"tu_1","name":"Bash","input":{"command":"sudo rtk find foo"}}]}}` + "\n"
	resErr  = `{"type":"user","uuid":"a2","sessionId":"example-session-1","timestamp":"2026-10-29T20:00:01Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu_1","is_error":true,"content":"rtk: command not found"}]}}` + "\n"
	callTwo = `{"type":"assistant","uuid":"a3","sessionId":"example-session-1","timestamp":"2026-10-29T20:00:02Z","message":{"id":"m2","role":"assistant","content":[{"type":"tool_use","id":"tu_2","name":"Read","input":{"file_path":"/example/x"}}]}}` + "\n"
	resOK   = `{"type":"user","uuid":"a4","sessionId":"example-session-1","timestamp":"2026-10-29T20:00:03Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu_2","is_error":false,"content":"file body here"}]}}` + "\n"
)

func openDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "t.db"), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func rowCount(t *testing.T, db *sql.DB, q string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func recSweep(t *testing.T, db *sql.DB, c *client.Client) RecorderStats {
	t.Helper()
	st, err := RunRecorder(context.Background(), db, RecorderOptions{Client: c})
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestRecorderIngestSnapshotThenOnlyNewRecords(t *testing.T) {
	env := clienttest.Start(t)
	env.Write(rel, callOne+resErr+callTwo+resOK)
	env.Settle()
	db := openDB(t)
	c := env.Dial(context.Background())

	first := recSweep(t, db, c)
	if first.ToolCalls != 2 || first.ToolResult != 2 || first.Errors != 1 {
		t.Fatalf("first sweep stats: %+v", first)
	}
	if n := rowCount(t, db, `SELECT COUNT(*) FROM tool_calls`); n != 2 {
		t.Fatalf("tool_calls=%d, want 2", n)
	}
	if n := rowCount(t, db, `SELECT COUNT(*) FROM tool_results`); n != 2 {
		t.Fatalf("tool_results=%d, want 2", n)
	}
	// Error body and signature come from the recorder's copy; the success body
	// is never stored, its length is.
	var content, sig sql.NullString
	if err := db.QueryRow(`SELECT content, signature FROM tool_results WHERE tool_use_id='tu_1'`).Scan(&content, &sig); err != nil || content.String != "rtk: command not found" || sig.String == "" {
		t.Fatalf("error row: %v %v %v", content, sig, err)
	}
	var okContent sql.NullString
	var okLen int
	if err := db.QueryRow(`SELECT content, content_len FROM tool_results WHERE tool_use_id='tu_2'`).Scan(&okContent, &okLen); err != nil || okContent.Valid || okLen == 0 {
		t.Fatalf("success row: %v %d %v", okContent, okLen, err)
	}
	var lead sql.NullString
	if err := db.QueryRow(`SELECT lead_cmd FROM tool_calls WHERE tool_use_id='tu_1'`).Scan(&lead); err != nil || lead.String != "rtk" {
		t.Fatalf("lead_cmd=%v err=%v", lead, err)
	}

	// A re-run with nothing new takes nothing.
	if again := recSweep(t, db, c); again.Entries != 0 {
		t.Fatalf("second sweep took %d entries, want 0", again.Entries)
	}
	// Only the appended records arrive.
	env.Append(rel, `{"type":"assistant","uuid":"a5","sessionId":"example-session-1","timestamp":"2026-10-29T20:00:04Z","message":{"id":"m3","role":"assistant","content":[{"type":"tool_use","id":"tu_3","name":"Bash","input":{"command":"ls"}}]}}`+"\n")
	env.Settle()
	third := recSweep(t, db, c)
	if third.Entries != 1 || third.ToolCalls != 1 {
		t.Fatalf("third sweep: %+v", third)
	}
	if n := rowCount(t, db, `SELECT COUNT(*) FROM tool_calls`); n != 3 {
		t.Fatalf("tool_calls=%d, want 3", n)
	}
}

func TestRecorderIngestSkipsStatusFiles(t *testing.T) {
	env := clienttest.Start(t)
	env.Write("projects/example-proj/example-session-1.status.jsonl", `{"rate_limits":{"five_hour":{"used_percentage":3}}}`+"\n")
	env.Write(rel, callOne+resErr)
	env.Settle()
	db := openDB(t)
	recSweep(t, db, env.Dial(context.Background()))
	if n := rowCount(t, db, `SELECT COUNT(*) FROM files`); n != 1 {
		t.Fatalf("files=%d, want only the transcript", n)
	}
}

func TestRecorderIngestAppliesAResetBeforeDeduplicating(t *testing.T) {
	env := clienttest.Start(t)
	env.Write(rel, callOne+resErr)
	env.Settle()
	db := openDB(t)
	c := env.Dial(context.Background())
	recSweep(t, db, c)
	before := rowCount(t, db, `SELECT COUNT(*) FROM tool_results`)

	if err := env.Daemon.Parser().Reparse("claude-transcript"); err != nil {
		t.Fatal(err)
	}
	st := recSweep(t, db, c)
	if st.Resets == 0 {
		t.Fatalf("no reset was delivered after a forced re-parse: %+v", st)
	}
	if n := rowCount(t, db, `SELECT COUNT(*) FROM tool_results`); n != before {
		t.Fatalf("tool_results=%d after reset, want %d (no loss, no duplicates)", n, before)
	}
	if n := rowCount(t, db, `SELECT COUNT(*) FROM tool_calls`); n != 1 {
		t.Fatalf("tool_calls=%d after reset, want 1", n)
	}
}

func TestRecorderIngestResyncsOnAnExpiredCursor(t *testing.T) {
	env := clienttest.Start(t)
	env.Write(rel, callOne+resErr)
	env.Settle()
	db := openDB(t)
	c := env.Dial(context.Background())
	recSweep(t, db, c)
	if _, err := db.Exec(`UPDATE feed_cursors SET cursor = '999:1'`); err != nil {
		t.Fatal(err)
	}
	st := recSweep(t, db, c)
	if st.Resyncs == 0 {
		t.Fatalf("an expired cursor did not resync: %+v", st)
	}
	if n := rowCount(t, db, `SELECT COUNT(*) FROM tool_calls`); n != 1 {
		t.Fatalf("tool_calls=%d after resync, want 1", n)
	}
	if cur := rowCount(t, db, `SELECT COUNT(*) FROM feed_cursors WHERE cursor <> '999:1'`); cur != 2 {
		t.Fatalf("cursors were not replaced: %d", cur)
	}
}

func TestRecorderDownIsReportedNotFallenBack(t *testing.T) {
	env := clienttest.Start(t)
	env.Write(rel, callOne+resErr)
	env.Settle()
	sock := env.Socket
	env.Stop()
	_, err := client.Dial(context.Background(), client.Options{Socket: sock})
	if err == nil || !isDown(err) {
		t.Fatalf("dial of a stopped recorder: %v", err)
	}
	db := openDB(t)
	if _, err := RunRecorder(context.Background(), db, RecorderOptions{}); err == nil {
		t.Fatal("no client must be an error, never a file fallback")
	}
	if n := rowCount(t, db, `SELECT COUNT(*) FROM tool_calls`); n != 0 {
		t.Fatalf("rows were written without a recorder: %d", n)
	}
}

func isDown(err error) bool { return errors.Is(err, client.ErrDown) }
