package ingest

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The frozen-fixture half of the differential requirement: a committed corpus
// with counts derived by hand (see testhelp_test.go), so a refactor that silently
// changes what is extracted fails here rather than in a live audit.
func TestFixtureCorpusCounts(t *testing.T) {
	root := fixtureCorpus(t)
	_, db := openTestDB(t, false)
	stats := sweep(t, db, root)

	if stats.Scanned != wantFiles {
		t.Errorf("scanned = %d, want %d", stats.Scanned, wantFiles)
	}
	if stats.Changed() != wantFiles {
		t.Errorf("changed = %d, want %d on a first sweep", stats.Changed(), wantFiles)
	}
	if stats.LinesOK != wantLinesOK {
		t.Errorf("lines_ok = %d, want %d", stats.LinesOK, wantLinesOK)
	}
	if stats.LinesBad != wantLinesBad {
		t.Errorf("lines_bad = %d, want %d", stats.LinesBad, wantLinesBad)
	}
	if stats.Errors != wantErrors {
		t.Errorf("errors = %d, want %d", stats.Errors, wantErrors)
	}

	for _, tc := range []struct {
		what  string
		query string
		want  int64
	}{
		{"files", `SELECT COUNT(*) FROM files`, wantFiles},
		{"events", `SELECT COUNT(*) FROM events`, wantEvents},
		{"tool_calls", `SELECT COUNT(*) FROM tool_calls`, wantToolCalls},
		{"tool_results", `SELECT COUNT(*) FROM tool_results`, wantToolResults},
		{"errors", `SELECT COUNT(*) FROM tool_results WHERE is_error = 1`, wantErrors},
		{"assistant_text", `SELECT COUNT(*) FROM assistant_text`, wantNarration},
		{"lines_ok", `SELECT SUM(lines_ok) FROM files`, wantLinesOK},
		{"lines_bad", `SELECT SUM(lines_bad) FROM files`, wantLinesBad},
	} {
		if got := countRows(t, db, tc.query); got != tc.want {
			t.Errorf("%s = %d, want %d", tc.what, got, tc.want)
		}
	}
}

// T-2 / T-15: a malformed line costs one LINE, not the file it lives in.
func TestMalformedLinesSkipPerLineNotPerFile(t *testing.T) {
	root := fixtureCorpus(t)
	_, db := openTestDB(t, false)
	sweep(t, db, root)

	bad := filepath.Join(root, "projB", "sess-bad.jsonl")
	var linesOK, linesBad int64
	if err := db.QueryRow(
		`SELECT lines_ok, lines_bad FROM files WHERE path = ?`, bad,
	).Scan(&linesOK, &linesBad); err != nil {
		t.Fatalf("read sess-bad row: %v", err)
	}
	if linesOK != 7 || linesBad != 2 {
		t.Fatalf("sess-bad lines_ok/lines_bad = %d/%d, want 7/2", linesOK, linesBad)
	}
	// The records on BOTH SIDES of the corrupt line must be present; that is what
	// makes coverage provable rather than assumed.
	for _, seq := range []int64{0, 1, 2, 5, 6, 7, 8} {
		if got := countRows(t, db, `SELECT COUNT(*) FROM events WHERE path = ? AND seq = ?`, bad, seq); got != 1 {
			t.Errorf("events row for seq %d missing (a per-file skip would lose it)", seq)
		}
	}
	// The bad lines themselves are counted, not indexed.
	for _, seq := range []int64{3, 4} {
		if got := countRows(t, db, `SELECT COUNT(*) FROM events WHERE path = ? AND seq = ?`, bad, seq); got != 0 {
			t.Errorf("events row exists for malformed seq %d", seq)
		}
	}
}

// T-9: is_error is PRESENT-AND-TRUE, never a boolean default. The fixture carries
// two `"is_error":false` results; counting either as an error would inflate every
// downstream rate.
func TestIsErrorIsPresentAndTrueOnly(t *testing.T) {
	root := fixtureCorpus(t)
	_, db := openTestDB(t, false)
	sweep(t, db, root)

	for _, id := range []string{"toolu_A3", "toolu_C2"} {
		var isErr int64
		var contentLen int64
		var content sql.NullString
		var sig sql.NullString
		if err := db.QueryRow(
			`SELECT is_error, content_len, content, signature FROM tool_results WHERE tool_use_id = ?`,
			id,
		).Scan(&isErr, &contentLen, &content, &sig); err != nil {
			t.Fatalf("read %s: %v", id, err)
		}
		if isErr != 0 {
			t.Errorf(`%s has is_error=%d; "is_error":false MUST NOT be counted as an error`, id, isErr)
		}
		// T-3a: length always populated, body never stored for a success.
		if contentLen == 0 {
			t.Errorf("%s content_len = 0; it must be populated for a successful result too", id)
		}
		if content.Valid {
			t.Errorf("%s stored a successful result body (%q); T-3a stores length only", id, content.String)
		}
		if sig.Valid {
			t.Errorf("%s stored a signature for a non-error", id)
		}
	}
	// Exact hand-computed lengths.
	if got := countRows(t, db, `SELECT content_len FROM tool_results WHERE tool_use_id='toolu_A3'`); got != 14 {
		t.Errorf("toolu_A3 content_len = %d, want 14 (\"file body here\")", got)
	}
	if got := countRows(t, db, `SELECT content_len FROM tool_results WHERE tool_use_id='toolu_C2'`); got != 2 {
		t.Errorf("toolu_C2 content_len = %d, want 2 (array of one text block \"ok\")", got)
	}
}

// T-3: error bodies stored in full, and T-6: the signature computed at ingest.
func TestErrorBodiesStoredInFullWithSignature(t *testing.T) {
	root := fixtureCorpus(t)
	_, db := openTestDB(t, false)
	sweep(t, db, root)

	var content, sig string
	var contentLen int64
	if err := db.QueryRow(
		`SELECT content, content_len, signature FROM tool_results WHERE tool_use_id = 'toolu_C1'`,
	).Scan(&content, &contentLen, &sig); err != nil {
		t.Fatalf("read toolu_C1: %v", err)
	}
	wantBody := "File does not exist: /home/nope/x"
	if content != wantBody {
		t.Errorf("content = %q, want %q (untruncated)", content, wantBody)
	}
	if contentLen != int64(len(wantBody)) {
		t.Errorf("content_len = %d, want %d", contentLen, len(wantBody))
	}
	if sig != sigMissing {
		t.Errorf("signature = %q, want %q", sig, sigMissing)
	}
}

// T-3: tool inputs stored untruncated, and the lead command peeled from the FULL
// input.
func TestToolInputsStoredUntruncated(t *testing.T) {
	root := fixtureCorpus(t)
	_, db := openTestDB(t, false)
	sweep(t, db, root)

	var input string
	var lead sql.NullString
	if err := db.QueryRow(
		`SELECT input_json, lead_cmd FROM tool_calls WHERE tool_use_id = 'toolu_A4'`,
	).Scan(&input, &lead); err != nil {
		t.Fatalf("read toolu_A4: %v", err)
	}
	if !strings.Contains(input, `"VAR=1 nice sleep 5"`) {
		t.Errorf("input_json = %q; the full command must be stored", input)
	}
	if !lead.Valid || lead.String != "sleep" {
		t.Errorf("lead_cmd = %v, want sleep (VAR= and nice peeled)", lead)
	}
	// lead_cmd is Bash-only.
	var readLead sql.NullString
	if err := db.QueryRow(
		`SELECT lead_cmd FROM tool_calls WHERE tool_use_id = 'toolu_A3'`,
	).Scan(&readLead); err != nil {
		t.Fatalf("read toolu_A3: %v", err)
	}
	if readLead.Valid {
		t.Errorf("lead_cmd populated for a Read call (%q); it is Bash-only", readLead.String)
	}
}

// T-4/T-5: every field the queries depend on is retained. Retrofitting any of
// them means a full re-ingest, so their presence is asserted explicitly.
func TestEventsRetainEveryRequiredField(t *testing.T) {
	root := fixtureCorpus(t)
	_, db := openTestDB(t, false)
	sweep(t, db, root)

	var (
		uuid, parent, session, ts, typ, cwd, branch, mode, promptSrc, userType, srcTool, entry sql.NullString
		sidechain                                                                              sql.NullInt64
	)
	err := db.QueryRow(`
		SELECT uuid, parent_uuid, session_id, ts, type, is_sidechain, cwd, git_branch,
		       permission_mode, prompt_source, user_type, source_tool_assistant_uuid, entrypoint
		FROM events WHERE path = ? AND seq = 1`, mainTranscript(root)).Scan(
		&uuid, &parent, &session, &ts, &typ, &sidechain, &cwd, &branch,
		&mode, &promptSrc, &userType, &srcTool, &entry,
	)
	if err != nil {
		t.Fatalf("read event: %v", err)
	}
	for _, tc := range []struct {
		field string
		got   sql.NullString
		want  string
	}{
		{"uuid", uuid, "a-01"},
		{"parent_uuid", parent, "a-00"},
		{"session_id", session, "S-MAIN"},
		{"ts", ts, "2026-07-22T10:00:02.000Z"},
		{"type", typ, "user"},
		{"cwd", cwd, "/work/repo"},
		{"git_branch", branch, "main"},
		{"prompt_source", promptSrc, "system"},
		{"user_type", userType, "external"},
		{"source_tool_assistant_uuid", srcTool, "a-00"},
	} {
		if !tc.got.Valid || tc.got.String != tc.want {
			t.Errorf("%s = %v, want %q", tc.field, tc.got, tc.want)
		}
	}
	if !sidechain.Valid || sidechain.Int64 != 0 {
		t.Errorf("is_sidechain = %v, want 0", sidechain)
	}

	// The subagent session's rows must carry is_sidechain = 1 — this single column
	// is what decides where a fix belongs.
	if got := countRows(t, db,
		`SELECT COUNT(*) FROM events WHERE is_sidechain = 1`); got != 4 {
		t.Errorf("is_sidechain=1 events = %d, want 4", got)
	}

	// T-5 fields present on the system event that carries them.
	var duration, hookCount sql.NullInt64
	var hookErrors sql.NullString
	if err := db.QueryRow(
		`SELECT duration_ms, hook_count, hook_errors FROM events WHERE path = ? AND seq = 7`,
		mainTranscript(root),
	).Scan(&duration, &hookCount, &hookErrors); err != nil {
		t.Fatalf("read system event: %v", err)
	}
	if !duration.Valid || duration.Int64 != 1234 {
		t.Errorf("duration_ms = %v, want 1234", duration)
	}
	if !hookCount.Valid || hookCount.Int64 != 3 {
		t.Errorf("hook_count = %v, want 3", hookCount)
	}
	if !hookErrors.Valid || hookErrors.String != `["ceta: denied"]` {
		t.Errorf("hook_errors = %v, want [\"ceta: denied\"]", hookErrors)
	}
	// An EMPTY hookErrors array is a real observation and is stored, so
	// "rejected nothing" stays distinguishable from "no hook data".
	var emptyHooks sql.NullString
	if err := db.QueryRow(
		`SELECT hook_errors FROM events WHERE path = ? AND seq = 7`,
		filepath.Join(root, "projB", "sess-bad.jsonl"),
	).Scan(&emptyHooks); err != nil {
		t.Fatalf("read empty-hooks event: %v", err)
	}
	if !emptyHooks.Valid || emptyHooks.String != "[]" {
		t.Errorf("hook_errors = %v, want []", emptyHooks)
	}
}

// T-1: a second sweep with nothing new does ZERO work, and says so in its
// progress output.
func TestSecondSweepDoesNothing(t *testing.T) {
	root := fixtureCorpus(t)
	_, db := openTestDB(t, false)
	first := sweep(t, db, root)
	if first.BytesParsed == 0 {
		t.Fatal("first sweep parsed no bytes")
	}

	var out strings.Builder
	second := sweepWith(t, db, Options{Root: root, FinalAfter: FinalAfterImmediate, Progress: &out})
	if second.Changed() != 0 {
		t.Errorf("changed = %d, want 0", second.Changed())
	}
	if second.BytesParsed != 0 {
		t.Errorf("bytes = %d, want 0", second.BytesParsed)
	}
	if second.Unchanged != wantFiles {
		t.Errorf("unchanged = %d, want %d", second.Unchanged, wantFiles)
	}
	summary := out.String()
	if !strings.Contains(summary, "changed=0") || !strings.Contains(summary, "bytes=0") {
		t.Errorf("progress output does not state the zero-work outcome:\n%s", summary)
	}
	// Idempotent: no duplicated rows.
	if got := countRows(t, db, `SELECT COUNT(*) FROM events`); got != wantEvents {
		t.Errorf("events = %d after two sweeps, want %d", got, wantEvents)
	}
}

// T-1a: an append re-parses ONLY the appended byte range. Without this the active
// session's transcript would be re-parsed on every tick, which is what makes a
// scheduled sweep affordable or not.
func TestAppendParsesOnlyTheDelta(t *testing.T) {
	root := fixtureCorpus(t)
	_, db := openTestDB(t, false)
	sweep(t, db, root)

	path := mainTranscript(root)
	before, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	added := `{"type":"assistant","uuid":"a-10","sessionId":"S-MAIN","timestamp":"2026-07-22T10:00:20.000Z","isSidechain":false,"message":{"role":"assistant","content":[{"type":"text","text":"appended"}]}}`
	appendLines(t, path, added)
	after, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	delta := after.Size() - before.Size()

	stats := sweep(t, db, root)
	if stats.Resumed != 1 {
		t.Errorf("resumed = %d, want 1", stats.Resumed)
	}
	if stats.BytesParsed != delta {
		t.Errorf("bytes = %d, want exactly the appended %d", stats.BytesParsed, delta)
	}
	if stats.LinesOK != 1 {
		t.Errorf("lines_ok = %d, want 1", stats.LinesOK)
	}
	// The line ordinal continues from where the previous sweep stopped.
	if got := countRows(t, db, `SELECT COUNT(*) FROM events WHERE path = ? AND seq = 10`, path); got != 1 {
		t.Error("appended line was not recorded at seq 10; the resumed ordinal is wrong")
	}
	if got := countRows(t, db, `SELECT lines_ok FROM files WHERE path = ?`, path); got != 11 {
		t.Errorf("lines_ok = %d, want 11 (10 + 1 appended)", got)
	}
	if got := countRows(t, db, `SELECT COUNT(*) FROM events WHERE path = ?`, path); got != 11 {
		t.Errorf("events for the file = %d, want 11", got)
	}
}

// T-1a: a file whose size SHRANK was rewritten (compaction) and is re-ingested
// from zero — resuming into different content would silently corrupt the index.
func TestShrunkFileIsReingestedFromZero(t *testing.T) {
	root := fixtureCorpus(t)
	_, db := openTestDB(t, false)
	sweep(t, db, root)

	path := mainTranscript(root)
	replacement := `{"type":"assistant","uuid":"z-00","sessionId":"S-NEW","timestamp":"2026-07-25T00:00:00.000Z","isSidechain":false,"message":{"role":"assistant","content":[{"type":"text","text":"compacted"}]}}` + "\n"
	if err := os.WriteFile(path, []byte(replacement), 0o644); err != nil {
		t.Fatalf("rewrite: %v", err)
	}

	stats := sweep(t, db, root)
	if stats.Reingested != 1 {
		t.Errorf("reingested = %d, want 1", stats.Reingested)
	}
	if got := countRows(t, db, `SELECT COUNT(*) FROM events WHERE path = ?`, path); got != 1 {
		t.Errorf("events for the rewritten file = %d, want 1 (stale rows must be purged)", got)
	}
	// Every record class from the old content must be gone.
	for _, tbl := range []string{"tool_calls", "tool_results", "assistant_text"} {
		var n int64
		if err := db.QueryRow(`SELECT COUNT(*) FROM `+tbl+` WHERE path = ?`, path).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", tbl, err)
		}
		wantN := int64(0)
		if tbl == "assistant_text" {
			wantN = 1 // the single replacement line is narration
		}
		if n != wantN {
			t.Errorf("%s rows for the rewritten file = %d, want %d", tbl, n, wantN)
		}
	}
	if got := countRows(t, db, `SELECT resume_offset FROM files WHERE path = ?`, path); got != int64(len(replacement)) {
		t.Errorf("resume_offset = %d, want %d", got, len(replacement))
	}
}

// T-1a: an mtime that moved BACKWARD is the other rewrite signal.
func TestBackwardMtimeIsReingested(t *testing.T) {
	root := fixtureCorpus(t)
	_, db := openTestDB(t, false)
	sweep(t, db, root)

	path := mainTranscript(root)
	setMtime(t, path, time.Now().Add(-72*time.Hour))
	stats := sweep(t, db, root)
	if stats.Reingested != 1 {
		t.Errorf("reingested = %d, want 1 (a backward mtime means the file was rewritten)", stats.Reingested)
	}
	if got := countRows(t, db, `SELECT COUNT(*) FROM events WHERE path = ?`, path); got != 10 {
		t.Errorf("events = %d, want 10 after a clean re-ingest", got)
	}
}

// A stored offset that no longer lands just after a newline means the file was
// rewritten to the same length. Resuming there would parse from the middle of a
// record; the boundary check turns that into a clean re-ingest.
func TestCorruptResumeOffsetForcesReingest(t *testing.T) {
	root := fixtureCorpus(t)
	_, db := openTestDB(t, false)
	sweep(t, db, root)

	path := mainTranscript(root)
	// Poison the offset so it points mid-line, then make the file look grown.
	if _, err := db.Exec(`UPDATE files SET resume_offset = 17, mtime = mtime - 1000000000 WHERE path = ?`, path); err != nil {
		t.Fatalf("poison offset: %v", err)
	}
	// Its size must still match so `decide` chooses resume, not reingest.
	stats := sweep(t, db, root)
	if stats.Reingested != 1 {
		t.Fatalf("reingested = %d, want 1 (a mid-line offset must force a clean re-ingest); stats=%+v", stats.Reingested, stats)
	}
	if got := countRows(t, db, `SELECT COUNT(*) FROM events WHERE path = ?`, path); got != 10 {
		t.Errorf("events = %d, want 10", got)
	}
}

// T-15: a file whose last line has no terminator is being written right now. The
// torn line is neither parsed nor counted as malformed, the offset stops before
// it, and the file is recorded OPEN so a later tick finishes it.
func TestTornTrailingLineIsNotConsumedAndFileStaysOpen(t *testing.T) {
	root := fixtureCorpus(t)
	_, db := openTestDB(t, false)
	sweep(t, db, root)

	path := mainTranscript(root)
	full, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	partial := `{"type":"assistant","uuid":"a-10","sessionId":"S-MAIN","timestamp":"2026-07-22T10:00:2`
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := f.WriteString(partial); err != nil {
		t.Fatalf("write partial: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	stats := sweep(t, db, root)
	if stats.BytesParsed != 0 {
		t.Errorf("bytes = %d, want 0 — an unterminated trailing line must not be consumed", stats.BytesParsed)
	}
	if stats.LinesBad != 0 {
		t.Errorf("lines_bad = %d, want 0 — a torn write is not malformed input", stats.LinesBad)
	}
	var offset, complete int64
	if err := db.QueryRow(
		`SELECT resume_offset, complete FROM files WHERE path = ?`, path,
	).Scan(&offset, &complete); err != nil {
		t.Fatalf("read files row: %v", err)
	}
	if offset != full.Size() {
		t.Errorf("resume_offset = %d, want %d (the start of the torn line)", offset, full.Size())
	}
	if complete != 0 {
		t.Error("file recorded complete while a torn tail is unparsed; partial ingestion must be distinguishable")
	}

	// Finish the line; the next sweep picks it up whole.
	appendLines(t, path, `0.000Z","isSidechain":false,"message":{"role":"assistant","content":[{"type":"text","text":"finished"}]}}`)
	stats = sweep(t, db, root)
	if stats.LinesOK != 1 {
		t.Errorf("lines_ok = %d, want 1 once the record is terminated", stats.LinesOK)
	}
	if got := countRows(t, db, `SELECT complete FROM files WHERE path = ?`, path); got != 1 {
		t.Error("file still recorded open after being fully consumed and quiescent")
	}
}

// T-15: a file that is still being appended is recorded OPEN even when every
// visible byte has been consumed, because the quiescence window has not passed.
func TestActiveFileIsRecordedOpen(t *testing.T) {
	root := fixtureCorpus(t)
	_, db := openTestDB(t, false)
	// A generous window with fresh mtimes: nothing can be final yet.
	sweepWith(t, db, Options{Root: root, FinalAfter: time.Hour, Progress: nil})
	now := time.Now()
	for _, p := range []string{
		mainTranscript(root),
		filepath.Join(root, "projA", "sess-sub.jsonl"),
		filepath.Join(root, "projB", "sess-bad.jsonl"),
	} {
		setMtime(t, p, now)
	}
	// Re-sweep so the fresh mtimes are what `complete` is computed against.
	sweepWith(t, db, Options{Root: root, FinalAfter: time.Hour, Progress: nil})
	if got := countRows(t, db, `SELECT COUNT(*) FROM files WHERE complete = 1`); got != 0 {
		t.Errorf("%d file(s) recorded complete inside the quiescence window, want 0", got)
	}
	if got := countRows(t, db, `SELECT COUNT(*) FROM files WHERE complete = 0`); got != wantFiles {
		t.Errorf("%d file(s) recorded open, want %d", got, wantFiles)
	}
}

// T-14: coverage does not depend on a session terminating cleanly. A transcript
// abandoned mid-write — the shape a killed session leaves — is still indexed up
// to its last complete record.
func TestAbnormallyTerminatedSessionIsStillIndexed(t *testing.T) {
	root := fixtureCorpus(t)
	killed := filepath.Join(root, "projB", "sess-killed.jsonl")
	content := `{"type":"assistant","uuid":"k-00","sessionId":"S-KILL","timestamp":"2026-07-26T00:00:00.000Z","isSidechain":false,"message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_K1","name":"Bash","input":{"command":"sleep 900"}}]}}` + "\n" +
		`{"type":"user","uuid":"k-01","sessionId":"S-KILL","timestamp":"2026-07-26T00:15:00.000Z","isSidechain":false,"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_K1","is_error":true,"content":"Blocked: sleep 900"}]}}` + "\n" +
		`{"type":"assistant","uuid":"k-02","sessionId":"S-KILL","timesta` // died here
	if err := os.WriteFile(killed, []byte(content), 0o644); err != nil {
		t.Fatalf("write killed transcript: %v", err)
	}
	_, db := openTestDB(t, false)
	sweep(t, db, root)

	if got := countRows(t, db, `SELECT COUNT(*) FROM events WHERE path = ?`, killed); got != 2 {
		t.Errorf("events = %d, want 2 — the two complete records before the kill", got)
	}
	if got := countRows(t, db,
		`SELECT COUNT(*) FROM tool_results WHERE tool_use_id = 'toolu_K1' AND is_error = 1`); got != 1 {
		t.Error("the killed session's error was not indexed; a session-end hook would have missed it entirely")
	}
	if got := countRows(t, db, `SELECT complete FROM files WHERE path = ?`, killed); got != 0 {
		t.Error("a transcript with an unterminated tail must be recorded open, not final")
	}
}

// T-16: off by default, and the flag actually captures.
func TestThinkingCaptureIsOptOut(t *testing.T) {
	root := fixtureCorpus(t)
	path := filepath.Join(root, "projA", "sess-think.jsonl")
	if err := os.WriteFile(path, []byte(
		`{"type":"assistant","uuid":"t-00","sessionId":"S-THINK","timestamp":"2026-07-27T00:00:00.000Z","isSidechain":false,"message":{"role":"assistant","content":[{"type":"thinking","thinking":"pondering"},{"type":"text","text":"said"}]}}`+"\n",
	),
		0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	t.Run("off by default", func(t *testing.T) {
		_, db := openTestDB(t, false)
		stats := sweep(t, db, root)
		if stats.Thinking != 0 {
			t.Errorf("thinking captured = %d with the flag off, want 0", stats.Thinking)
		}
		// The narration is still captured; only the thinking block is dropped.
		if got := countRows(t, db, `SELECT COUNT(*) FROM assistant_text WHERE path = ?`, path); got != 1 {
			t.Errorf("assistant_text = %d, want 1", got)
		}
	})

	t.Run("captured with the flag", func(t *testing.T) {
		_, db := openTestDB(t, true)
		stats := sweepWith(t, db, Options{Root: root, Thinking: true, FinalAfter: FinalAfterImmediate})
		if stats.Thinking != 1 {
			t.Errorf("thinking captured = %d, want 1", stats.Thinking)
		}
		var text string
		if err := db.QueryRow(`SELECT text FROM thinking WHERE path = ?`, path).Scan(&text); err != nil {
			t.Fatalf("read thinking: %v", err)
		}
		if text != "pondering" {
			t.Errorf("thinking text = %q", text)
		}
	})
}

// T-8: progress is emitted incrementally so a supervising watchdog observes
// liveness. This is the direct fix for the two stalled runs that motivated the
// whole tool.
func TestProgressIsEmittedPerBatch(t *testing.T) {
	root := fixtureCorpus(t)
	_, db := openTestDB(t, false)
	var out strings.Builder
	sweepWith(t, db, Options{
		Root:          root,
		FinalAfter:    FinalAfterImmediate,
		ProgressEvery: 1,
		Progress:      &out,
	})
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	var progress int
	for _, l := range lines {
		if strings.HasPrefix(l, "progress: ") {
			progress++
		}
	}
	if progress != wantFiles {
		t.Errorf("%d progress line(s) for %d files at cadence 1, want %d", progress, wantFiles, wantFiles)
	}
	if !strings.HasPrefix(lines[len(lines)-1], "ingest complete: ") {
		t.Errorf("last line is not the summary: %q", lines[len(lines)-1])
	}
}

// An unreadable file must cost that file, not the sweep — provable coverage
// requires the run to finish and record what it saw.
func TestUnreadableFileDoesNotAbortTheSweep(t *testing.T) {
	root := fixtureCorpus(t)
	bad := filepath.Join(root, "projB", "unreadable.jsonl")
	if err := os.WriteFile(bad, []byte("{}\n"), 0o000); err != nil {
		t.Fatalf("write: %v", err)
	}
	if os.Geteuid() == 0 {
		t.Skip("running as root; mode 0000 is not enforced")
	}
	_, db := openTestDB(t, false)
	var out strings.Builder
	stats := sweepWith(t, db, Options{Root: root, FinalAfter: FinalAfterImmediate, Progress: &out})
	if stats.Failed != 1 {
		t.Errorf("failed = %d, want 1", stats.Failed)
	}
	if stats.Changed() != wantFiles {
		t.Errorf("changed = %d, want %d — the readable files must still be indexed", stats.Changed(), wantFiles)
	}
	if !strings.Contains(out.String(), "warn: ") {
		t.Error("the failure was not reported")
	}
}

func TestDecideClassification(t *testing.T) {
	prev := fileState{size: 100, mtime: 500, resumeOffset: 100, linesOK: 8, linesBad: 2}
	cases := []struct {
		name       string
		found      bool
		size       int64
		mtime      int64
		wantMode   mode
		wantOffset int64
		wantSeq    int64
	}{
		{"unseen file is fresh", false, 100, 500, modeFresh, 0, 0},
		{"identical stat is unchanged", true, 100, 500, modeUnchanged, 0, 0},
		{"grown file resumes at the offset", true, 180, 900, modeResume, 100, 10},
		{"shrunk file is reingested", true, 40, 900, modeReingest, 0, 0},
		{"backward mtime is reingested", true, 180, 100, modeReingest, 0, 0},
		{"offset past EOF is reingested", true, 100, 900, modeResume, 100, 10},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, off, seq := decide(prev, tc.found, tc.size, tc.mtime)
			if m != tc.wantMode {
				t.Errorf("mode = %v, want %v", m, tc.wantMode)
			}
			if off != tc.wantOffset {
				t.Errorf("offset = %d, want %d", off, tc.wantOffset)
			}
			if seq != tc.wantSeq {
				t.Errorf("seq = %d, want %d", seq, tc.wantSeq)
			}
		})
	}
	// A recorded offset beyond the file's current end cannot be resumed.
	if m, _, _ := decide(fileState{size: 100, mtime: 500, resumeOffset: 500}, true, 120, 900); m != modeReingest {
		t.Errorf("offset past EOF: mode = %v, want reingest", m)
	}
}

// Replaying the same byte range must not duplicate a record. This is the property
// that lets the sweep prefer correctness over cleverness: even a wrong resume
// decision cannot inflate a count.
func TestReplayingABytesRangeDoesNotDuplicate(t *testing.T) {
	root := fixtureCorpus(t)
	_, db := openTestDB(t, false)
	sweep(t, db, root)
	path := mainTranscript(root)

	// Simulate the one real replay path: a sweep that parsed and committed, then
	// a CRASH that lost the files-row update. On the next tick the recorded
	// offset and line counts are still at their pre-parse values, so the same
	// byte range is read again — and every insert must upsert over the rows that
	// are already there. Rewinding the offset and the counts TOGETHER is what
	// keeps this a realistic state rather than an impossible one (they are
	// written in the same transaction, so they never disagree in practice).
	// mtime is set behind the on-disk value so the file is classified as grown.
	if _, err := db.Exec(
		`UPDATE files SET resume_offset = 0, lines_ok = 0, lines_bad = 0, mtime = mtime - 1000000000
		 WHERE path = ?`, path,
	); err != nil {
		t.Fatalf("rewind: %v", err)
	}
	stats := sweep(t, db, root)
	if stats.Resumed != 1 {
		t.Fatalf("resumed = %d, want 1", stats.Resumed)
	}

	for _, tc := range []struct {
		what  string
		query string
		want  int64
	}{
		{"events", `SELECT COUNT(*) FROM events`, wantEvents},
		{"tool_calls", `SELECT COUNT(*) FROM tool_calls`, wantToolCalls},
		{"tool_results", `SELECT COUNT(*) FROM tool_results`, wantToolResults},
		{"assistant_text", `SELECT COUNT(*) FROM assistant_text`, wantNarration},
	} {
		if got := countRows(t, db, tc.query); got != tc.want {
			t.Errorf("%s = %d after a replay, want %d", tc.what, got, tc.want)
		}
	}
}

func TestProjectDirRecorded(t *testing.T) {
	root := fixtureCorpus(t)
	_, db := openTestDB(t, false)
	sweep(t, db, root)
	var dir string
	if err := db.QueryRow(`SELECT project_dir FROM files WHERE path = ?`, mainTranscript(root)).Scan(&dir); err != nil {
		t.Fatalf("read project_dir: %v", err)
	}
	if dir != "projA" {
		t.Errorf("project_dir = %q, want projA", dir)
	}
}
