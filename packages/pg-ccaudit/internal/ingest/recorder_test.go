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
	if third.Entries != 2 || third.ToolCalls != 1 {
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
	if cur := rowCount(t, db, `SELECT COUNT(*) FROM feed_cursors WHERE cursor <> '999:1'`); cur != 4 {
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

const (
	userTyped  = `{"type":"user","uuid":"u1","sessionId":"example-session-1","timestamp":"2026-10-29T19:59:58Z","promptSource":"typed","message":{"role":"user","content":"please list the example files"}}` + "\n"
	asstPlain  = `{"type":"assistant","uuid":"a0","parentUuid":"u1","sessionId":"example-session-1","timestamp":"2026-10-29T19:59:59Z","message":{"id":"m0","role":"assistant","content":[{"type":"text","text":"Sure, listing now."}]}}` + "\n"
	systemHook = `{"type":"system","uuid":"s1","sessionId":"example-session-1","timestamp":"2026-10-29T20:00:05Z","hookCount":3,"hookErrors":["example hook failed"]}` + "\n"
)

func TestRecorderIngestFillsEveryKindOfRow(t *testing.T) {
	env := clienttest.Start(t)
	env.Write(rel, userTyped+asstPlain+callOne+resErr+callTwo+resOK+systemHook)
	env.Write("projects/example-proj/example-session-1.status.jsonl", `{"rate_limits":{"five_hour":{"used_percentage":3}}}`+"\n")
	env.Settle()
	db := openDB(t)
	recSweep(t, db, env.Dial(context.Background()))

	if n := rowCount(t, db, `SELECT COUNT(*) FROM events`); n != 7 {
		t.Fatalf("events=%d, want 7 (one per transcript line, none for the status file)", n)
	}
	if n := rowCount(t, db, `SELECT COUNT(*) FROM files`); n != 1 {
		t.Fatalf("files=%d, want only the transcript", n)
	}
	var txt, src string
	var ln int
	if err := db.QueryRow(`SELECT text, text_len FROM user_text`).Scan(&txt, &ln); err != nil || txt != "please list the example files" {
		t.Fatalf("user_text: %q %v", txt, err)
	}
	if err := db.QueryRow(`SELECT prompt_source FROM events WHERE uuid='u1'`).Scan(&src); err != nil || src != "typed" {
		t.Fatalf("prompt_source=%q err=%v", src, err)
	}
	if err := db.QueryRow(`SELECT text FROM assistant_text`).Scan(&txt); err != nil || txt != "Sure, listing now." {
		t.Fatalf("assistant_text without a tool call: %q %v", txt, err)
	}
	var hc int
	var he string
	if err := db.QueryRow(`SELECT hook_count, hook_errors FROM events WHERE uuid='s1'`).Scan(&hc, &he); err != nil || hc != 3 || he != `["example hook failed"]` {
		t.Fatalf("hook keys: %d %q %v", hc, he, err)
	}
	var ts string
	if err := db.QueryRow(`SELECT ts FROM events WHERE uuid='a0'`).Scan(&ts); err != nil || ts != "2026-10-29T19:59:59.000Z" {
		t.Fatalf("ts=%q err=%v", ts, err)
	}
	// Per-file order is the record offset: a line's seq grows with its position.
	if n := rowCount(t, db, `SELECT COUNT(*) FROM events a JOIN events b ON a.uuid='a0' AND b.uuid='u1' WHERE a.seq > b.seq AND a.path = b.path`); n != 1 {
		t.Fatalf("seq does not follow the file order")
	}
	// The adjacency join: the error result and the narration line beside it.
	if n := rowCount(t, db, `SELECT COUNT(*) FROM tool_results r JOIN events e ON e.path=r.path AND e.seq=r.seq WHERE r.is_error=1 AND e.uuid='a2'`); n != 1 {
		t.Fatalf("error result is not on its line's event row")
	}
}

func TestRecorderIngestRerunKeepsRawDerivedColumns(t *testing.T) {
	env := clienttest.Start(t)
	env.Write(rel, userTyped+systemHook)
	env.Settle()
	db := openDB(t)
	c := env.Dial(context.Background())
	recSweep(t, db, c)
	if _, err := db.Exec(`UPDATE feed_cursors SET cursor = '999:1' WHERE name = 'lines'`); err != nil {
		t.Fatal(err)
	}
	recSweep(t, db, c) // the lines subscription resyncs and upserts every event again
	var src string
	if err := db.QueryRow(`SELECT prompt_source FROM events WHERE uuid='u1'`).Scan(&src); err != nil || src != "typed" {
		t.Fatalf("a replayed line event dropped prompt_source: %q %v", src, err)
	}
}

// A snapshot that spans more pages than one run gets to finish (the recorder's
// read deadline cuts it) must continue at the next page on the next run, not
// restart from the first page.
func TestRecorderIngestResumesASnapshotAfterADeadline(t *testing.T) {
	env := clienttest.Start(t)
	env.Write(rel, userTyped+asstPlain+callOne+resErr+callTwo+resOK)
	env.Settle()
	db := openDB(t)
	c := env.Dial(context.Background())

	deadline := &client.Error{Name: "unavailable", Message: `Post "http://recorder/v1/snapshot": context deadline exceeded`}
	var seen []client.SnapshotRequest
	failAt := 3 // the third snapshot request of the first run hits the deadline
	opt := RecorderOptions{Client: c, PageLimit: 1}
	opt.beforeSnapshot = func(req client.SnapshotRequest) error {
		seen = append(seen, req)
		if len(seen) == failAt {
			return deadline
		}
		return nil
	}
	_, err := RunRecorder(context.Background(), db, opt)
	if err == nil || !client.Retryable(err) {
		t.Fatalf("first run: want a retryable deadline error, got %v", err)
	}
	if n := rowCount(t, db, `SELECT COUNT(*) FROM feed_cursors WHERE name = 'lines'`); n != 0 {
		t.Fatalf("the lines cursor was saved before the snapshot finished")
	}
	if n := rowCount(t, db, `SELECT COUNT(*) FROM events`); n != 2 {
		t.Fatalf("events=%d after two committed pages, want 2", n)
	}
	var pos string
	if err := db.QueryRow(`SELECT cursor FROM feed_cursors WHERE name = 'lines.snapshot'`).Scan(&pos); err != nil || pos == "" {
		t.Fatalf("no snapshot position kept: %q %v", pos, err)
	}

	// The next run continues: its first snapshot request carries the saved
	// position, and it finishes every subscription.
	seen = nil
	failAt = -1
	if _, err := RunRecorder(context.Background(), db, opt); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if len(seen) == 0 || seen[0].At == "" || seen[0].Page == "" {
		t.Fatalf("the second run restarted the snapshot: first request %+v", seen)
	}
	if n := rowCount(t, db, `SELECT COUNT(*) FROM events`); n != 6 {
		t.Fatalf("events=%d, want 6", n)
	}
	if n := rowCount(t, db, `SELECT COUNT(*) FROM tool_calls`); n != 2 {
		t.Fatalf("tool_calls=%d, want 2", n)
	}
	if n := rowCount(t, db, `SELECT COUNT(*) FROM feed_cursors WHERE name LIKE '%.snapshot'`); n != 0 {
		t.Fatalf("a finished snapshot left its position behind")
	}
	if n := rowCount(t, db, `SELECT COUNT(*) FROM feed_cursors`); n != 4 {
		t.Fatalf("cursors=%d, want one per subscription", n)
	}
}

// A saved snapshot position the recorder no longer honours (its epoch changed)
// restarts the snapshot instead of failing every run.
func TestRecorderIngestRestartsASnapshotWithAnExpiredPosition(t *testing.T) {
	env := clienttest.Start(t)
	env.Write(rel, callOne+resErr)
	env.Settle()
	db := openDB(t)
	if _, err := db.Exec(`INSERT INTO feed_cursors (name, cursor) VALUES ('lines.snapshot', '999:1|r1')`); err != nil {
		t.Fatal(err)
	}
	st := recSweep(t, db, env.Dial(context.Background()))
	if st.ToolCalls != 1 {
		t.Fatalf("stats: %+v", st)
	}
	if n := rowCount(t, db, `SELECT COUNT(*) FROM events`); n != 2 {
		t.Fatalf("events=%d, want 2", n)
	}
}
