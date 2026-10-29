package main

import (
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/claude-tool-tracker/internal/store"
)

// app appends events with the real binary and ingests them with the real
// binary into temporary paths.
type app struct {
	t     *testing.T
	state string
}

func newApp(t *testing.T) *app { return &app{t: t, state: t.TempDir()} }

func (a *app) db() string { return filepath.Join(a.state, "claude-tool-tracker", "tracker.db") }

func (a *app) append(session, source, kind, toolUse, ts, eventID, data string, extra ...string) {
	a.t.Helper()
	args := []string{"append", "--session-id", session, "--source", source, "--kind", kind, "--ts", ts, "--event-id", eventID}
	if toolUse != "" {
		args = append(args, "--tool-use-id", toolUse)
	}
	args = append(args, extra...)
	if _, errOut, code := run(a.t, a.state, data, args...); code != 0 {
		a.t.Fatalf("append %s: exit %d: %s", kind, code, errOut)
	}
}

func (a *app) ingest() string {
	a.t.Helper()
	out, errOut, code := run(a.t, a.state, "", "ingest")
	if code != 0 {
		a.t.Fatalf("ingest: exit %d: %s", code, errOut)
	}
	return out
}

func (a *app) writeRaw(name, content string) {
	a.t.Helper()
	dir := filepath.Join(a.state, "claude-tool-tracker", "events")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		a.t.Fatal(err)
	}
	f, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		a.t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(content); err != nil {
		a.t.Fatal(err)
	}
}

func (a *app) open() *sql.DB {
	a.t.Helper()
	db, err := store.OpenReadOnly(a.db())
	if err != nil {
		a.t.Fatal(err)
	}
	a.t.Cleanup(func() { db.Close() })
	return db
}

func scalar[T any](t *testing.T, db *sql.DB, q string, args ...any) T {
	t.Helper()
	var v T
	if err := db.QueryRow(q, args...).Scan(&v); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return v
}

// fullCall appends one complete call's worth of events.
func (a *app) fullCall(session, tu, cwd string, ts int64) {
	a.t.Helper()
	n := func(i int64) string { return strconv.FormatInt(ts+i, 10) }
	a.append(session, "tracker", "tracker.call_start", tu, n(0), tu+"-1", `{"tool":"Bash","input":{"command":"ls"},"cwd":"`+cwd+`"}`)
	a.append(session, "tracker", "tracker.check", tu, n(1), tu+"-2", `{"decision":"ask","hook":"PreToolUse","reason":"needs a look"}`)
	a.append(session, "tracker", "tracker.pre_tool", tu, n(2), tu+"-3", `{"tool_name":"Bash","permission_mode":"auto","agent_type":"worker","cwd":"`+cwd+`"}`)
	a.append(session, "tracker", "tracker.running", tu, n(3), tu+"-4", `{"tool":"Bash"}`)
	a.append(session, "tracker", "tracker.post_tool", tu, n(40), tu+"-5", `{"hook_event":"PostToolUseFailure","tool_name":"Bash","duration_ms":37,"is_error":true,"error":"boom"}`)
	a.append(session, "tracker", "tracker.call_end", tu, n(41), tu+"-6", `{"outcome":"error","error":"boom"}`)
	a.append(session, "ceta", "ceta.verdict", tu, n(1), tu+"-7", `{"verdict":"ask","delivered":"reject","reason":"unattended","sandbox_enabled":true}`)
}

func TestIngestBuildsViewsAndCountsSkips(t *testing.T) {
	a := newApp(t)
	a.fullCall("sess1", "tu1", "/nonexistent-tracker-test/proj", 1700000000000)
	// A kind no code knows, an event with no tool_use_id, and a ceta-only session.
	a.append("sess1", "tracker", "tracker.future_thing", "tu1", "1700000000500", "u-1", `{"x":1}`)
	a.append("sess1", "tracker", "tracker.session_end", "", "1700000000600", "u-2", `{"reason":"clear","normal":true}`)
	a.append("sess2", "ceta", "ceta.verdict", "tu9", "1700000001000", "c-1", `{"verdict":"approve","delivered":"approve","sandbox_enabled":false}`)
	// Corrupt lines, written by hand, appended to the real file.
	a.writeRaw("tool-events.sess1.tracker.jsonl",
		"this is not json\n"+
			"{\"schema\":\"tool-events/1\",\"event_id\":\"m-1\",\"ts\":1,\"source\":\"tracker\",\"kind\":\"tracker.call_start\",\"session_id\":\"sess1\",\"data\":{}}\n"+
			"[1,2,3]\n\n")
	// Files that do not match the whole pattern are ignored.
	a.writeRaw("tool-events.sess1.tracker.jsonl.bak", "garbage\n")
	a.writeRaw("tool-events.other.Tracker.jsonl", "garbage\n")
	a.writeRaw("notes.txt", "garbage\n")

	out := a.ingest()
	if !strings.Contains(out, "tool-events.sess1.tracker.jsonl\tlines=12 new=8 duplicate=0 skipped_invalid_json=3 skipped_missing_field=1") {
		t.Errorf("per-file counts missing or wrong:\n%s", out)
	}
	if !strings.Contains(out, "total\tfiles=3 ") || strings.Contains(out, "bak") || strings.Contains(out, "notes") || strings.Contains(out, "Tracker") {
		t.Errorf("non-matching files must be ignored and totals reported:\n%s", out)
	}

	db := a.open()
	if got := scalar[int](t, db, "SELECT COUNT(*) FROM tool_calls"); got != 2 {
		t.Errorf("calls = %d, want 2 (tu1, tu9; session_end belongs to no call)", got)
	}
	var tool, verdict, hook, reason, mode, agentType, result, errText, denial any
	var dur, isErr, sandbox, runningTS, endTS any
	var engine, delivered, settledBy any
	err := db.QueryRow(`SELECT tool_name, verdict, deciding_hook, reason, permission_mode, agent_type, result, error_text,
		duration_ms, is_error, denial_reason, sandbox_enabled, engine_verdict, delivered_verdict, running_ts, call_end_ts, settled_by
		FROM tool_calls WHERE session_id='sess1' AND tool_use_id='tu1'`).
		Scan(&tool, &verdict, &hook, &reason, &mode, &agentType, &result, &errText, &dur, &isErr, &denial, &sandbox, &engine, &delivered, &runningTS, &endTS, &settledBy)
	if err != nil {
		t.Fatal(err)
	}
	want := []any{"Bash", "ask", "PreToolUse", "needs a look", "auto", "worker", "error", "boom", int64(37), int64(1), nil, int64(1), "ask", "reject", int64(1700000000003), int64(1700000000041), "ceta"}
	got := []any{tool, verdict, hook, reason, mode, agentType, result, errText, dur, isErr, denial, sandbox, engine, delivered, runningTS, endTS, settledBy}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("column %d = %v (%T), want %v", i, got[i], got[i], want[i])
		}
	}

	// Unknown kind appears as an annotation; known tracker kinds do not.
	if n := scalar[int](t, db, "SELECT COUNT(*) FROM annotations WHERE kind='tracker.future_thing' AND tool_use_id='tu1' AND call_id IS NOT NULL"); n != 1 {
		t.Errorf("unknown kind annotations = %d, want 1", n)
	}
	if n := scalar[int](t, db, "SELECT COUNT(*) FROM annotations WHERE kind='tracker.call_start'"); n != 0 {
		t.Errorf("core kinds must not be annotations, got %d", n)
	}
	if n := scalar[int](t, db, "SELECT COUNT(*) FROM annotations WHERE source='ceta'"); n != 2 {
		t.Errorf("ceta annotations = %d, want 2", n)
	}

	// Skipped-line view carries the run time.
	var s, bad, miss, noCall int
	var runMS int64
	if err := db.QueryRow(`SELECT ingest_ms, skipped_total, skipped_invalid_json, skipped_missing_field, events_without_call
		FROM ingest_skips WHERE file='tool-events.sess1.tracker.jsonl'`).Scan(&runMS, &s, &bad, &miss, &noCall); err != nil {
		t.Fatal(err)
	}
	if runMS <= 0 || s != 4 || bad != 3 || miss != 1 || noCall != 1 {
		t.Errorf("ingest_skips = run %d total %d invalid %d missing %d nocall %d", runMS, s, bad, miss, noCall)
	}

	// Sources per session: sess2 has CETA activity and no tracker file.
	if scalar[int](t, db, "SELECT has_tracker FROM session_sources WHERE session_id='sess2'") != 0 ||
		scalar[int](t, db, "SELECT has_ceta FROM session_sources WHERE session_id='sess2'") != 1 ||
		scalar[string](t, db, "SELECT sources FROM session_sources WHERE session_id='sess1'") != "ceta,tracker" {
		t.Error("session_sources wrong")
	}

	// Span: overall and per source.
	var lo, hi int64
	var sessions, calls int
	if err := db.QueryRow("SELECT earliest_ts, latest_ts, sessions, calls FROM event_span WHERE scope='all'").Scan(&lo, &hi, &sessions, &calls); err != nil {
		t.Fatal(err)
	}
	if lo != 1700000000000 || hi != 1700000001000 || sessions != 2 || calls != 2 {
		t.Errorf("event_span all = %d %d %d %d", lo, hi, sessions, calls)
	}
	if scalar[int](t, db, "SELECT calls FROM event_span WHERE scope='ceta'") != 2 || scalar[int](t, db, "SELECT sessions FROM event_span WHERE scope='tracker'") != 1 {
		t.Error("event_span per source wrong")
	}
}

func TestIngestIdempotentIncrementalAndStableCallIDs(t *testing.T) {
	a := newApp(t)
	a.fullCall("s1", "tuA", "/nonexistent-tracker-test/p", 1700000000000)
	a.fullCall("s1", "tuB", "/nonexistent-tracker-test/p", 1700000010000)
	a.ingest()
	db := a.open()
	ids := scalar[string](t, db, "SELECT group_concat(tool_use_id || '=' || call_id) FROM (SELECT * FROM tool_calls ORDER BY call_id)")
	events := scalar[int](t, db, "SELECT COUNT(*) FROM events")

	// Same files again: no new rows, same ids, and the second run reads nothing.
	out := a.ingest()
	if !strings.Contains(out, "total\tfiles=2 lines=0 new=0") {
		t.Errorf("second run should read nothing:\n%s", out)
	}
	db2 := a.open()
	if got := scalar[int](t, db2, "SELECT COUNT(*) FROM events"); got != events {
		t.Errorf("events %d -> %d after re-ingest", events, got)
	}
	if got := scalar[string](t, db2, "SELECT group_concat(tool_use_id || '=' || call_id) FROM (SELECT * FROM tool_calls ORDER BY call_id)"); got != ids {
		t.Errorf("call ids changed: %q -> %q", ids, got)
	}

	// A file extended by a later append is ingested incrementally; a torn
	// (unterminated) tail is left for the next run and then completed.
	a.fullCall("s1", "tuC", "/nonexistent-tracker-test/p", 1700000020000)
	a.writeRaw("tool-events.s1.tracker.jsonl", `{"schema":"tool-events/1","event_id":"torn-1","ts":1700000030000,"source":"tracker","kind":"tracker.call_start","session_id":"s1","tool_use_id":"tuD","data":{}`)
	out = a.ingest()
	if !strings.Contains(out, "tool-events.s1.tracker.jsonl\tlines=6 new=6 duplicate=0 skipped_invalid_json=0") {
		t.Errorf("incremental run should read only the 6 new tracker lines:\n%s", out)
	}
	a.writeRaw("tool-events.s1.tracker.jsonl", "}\n")
	out = a.ingest()
	if !strings.Contains(out, "tool-events.s1.tracker.jsonl\tlines=1 new=1") {
		t.Errorf("completed torn line should ingest:\n%s", out)
	}
	db3 := a.open()
	if got := scalar[string](t, db3, "SELECT group_concat(tool_use_id || '=' || call_id) FROM (SELECT * FROM tool_calls ORDER BY call_id)"); !strings.HasPrefix(got, ids+",tuC=") || !strings.Contains(got, "tuD=") {
		t.Errorf("ids after growth = %q, want prefix %q", got, ids)
	}

	// A file replaced by a shorter one is re-read from the start without duplicating.
	path := filepath.Join(a.state, "claude-tool-tracker", "events", "tool-events.s1.ceta.jsonl")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	first := before[:strings.Index(string(before), "\n")+1]
	if err := os.WriteFile(path, first, 0o600); err != nil {
		t.Fatal(err)
	}
	out = a.ingest()
	if !strings.Contains(out, "tool-events.s1.ceta.jsonl\tlines=1 new=0 duplicate=1") {
		t.Errorf("shrunk file should be re-read and deduplicated:\n%s", out)
	}
}

func TestIngestMapsWorktreeToMainRepoName(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	mainDir := filepath.Join(root, "the-repo")
	if err := os.MkdirAll(mainDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"commit", "-q", "--allow-empty", "-m", "init"},
		{"worktree", "add", "-q", "-b", "feat", filepath.Join(root, "wt", "feature")},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = mainDir
		cmd.Env = append(gitFixtureEnv(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@localhost", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@localhost")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	a := newApp(t)
	a.fullCall("w1", "main-call", mainDir, 1700000000000)
	a.fullCall("w1", "wt-call", filepath.Join(root, "wt", "feature"), 1700000010000)
	a.fullCall("w1", "other-call", root, 1700000020000)
	a.ingest()
	db := a.open()
	repoOf := func(tu string) sql.NullString {
		var r sql.NullString
		if err := db.QueryRow("SELECT repo FROM tool_calls WHERE tool_use_id=?", tu).Scan(&r); err != nil {
			t.Fatal(err)
		}
		return r
	}
	if r := repoOf("main-call"); r.String != "the-repo" {
		t.Errorf("main checkout repo = %v", r)
	}
	if r := repoOf("wt-call"); r.String != "the-repo" {
		t.Errorf("worktree repo = %v, want the-repo", r)
	}
	if r := repoOf("other-call"); r.Valid {
		t.Errorf("non-repo cwd should have no repo, got %v", r)
	}
}

// TestViewsDocumentMatchesDatabase checks docs/views.md against the produced
// database: every published view is documented with exactly its columns, and
// each one is queried read-only.
func TestViewsDocumentMatchesDatabase(t *testing.T) {
	doc, err := os.ReadFile(filepath.Join("..", "..", "docs", "views.md"))
	if err != nil {
		t.Fatal(err)
	}
	a := newApp(t)
	a.fullCall("d1", "tu1", "/nonexistent-tracker-test/p", 1700000000000)
	a.ingest()
	db := a.open()

	sections := map[string][]string{}
	var cur string
	heading := regexp.MustCompile("^## `([a-z_]+)`")
	row := regexp.MustCompile("^\\| `([a-z_]+)`\\s*\\|")
	for _, line := range strings.Split(string(doc), "\n") {
		if m := heading.FindStringSubmatch(line); m != nil {
			cur = m[1]
			sections[cur] = nil
		} else if m := row.FindStringSubmatch(line); m != nil && cur != "" {
			sections[cur] = append(sections[cur], m[1])
		}
	}
	if len(sections) != len(store.Views) {
		t.Errorf("docs/views.md documents %d views, store publishes %d", len(sections), len(store.Views))
	}
	for _, view := range store.Views {
		documented, ok := sections[view]
		if !ok {
			t.Errorf("view %s is not documented in docs/views.md", view)
			continue
		}
		rows, err := db.Query("SELECT * FROM " + view)
		if err != nil {
			t.Fatalf("select from %s: %v", view, err)
		}
		cols, _ := rows.Columns()
		for rows.Next() {
		}
		rows.Close()
		if strings.Join(cols, ",") != strings.Join(documented, ",") {
			t.Errorf("view %s columns\n  db:   %v\n  docs: %v", view, cols, documented)
		}
	}
	// The database really is read-only for readers.
	if _, err := db.Exec("DELETE FROM events"); err == nil {
		t.Error("read-only handle accepted a write")
	}
}

func TestIngestUsageAndMissingDirectory(t *testing.T) {
	state := t.TempDir()
	if _, _, code := run(t, state, "", "ingest", "--bogus"); code != 2 {
		t.Errorf("bad flag exit = %d, want 2", code)
	}
	if _, _, code := run(t, state, "", "ingest", "extra"); code != 2 {
		t.Errorf("extra arg exit = %d, want 2", code)
	}
	out, _, code := run(t, state, "", "ingest")
	if code != 0 || !strings.Contains(out, "total\tfiles=0") {
		t.Errorf("empty state: code %d out %q", code, out)
	}
	if _, err := os.Stat(filepath.Join(state, "claude-tool-tracker", "tracker.db")); err != nil {
		t.Errorf("database not created at the default path: %v", err)
	}
	custom := filepath.Join(state, "elsewhere", "t.db")
	if _, _, code := run(t, state, "", "ingest", "--db", custom, "--events-dir", filepath.Join(state, "none")); code != 0 {
		t.Errorf("--db/--events-dir exit = %d", code)
	}
	if _, err := os.Stat(custom); err != nil {
		t.Errorf("--db path not used: %v", err)
	}
}

// gitFixtureEnv is the environment without any GIT_* variable: a test run
// inside a git hook inherits GIT_DIR and GIT_INDEX_FILE, which would point its
// fixture git commands at the enclosing repository.
func gitFixtureEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GIT_") {
			env = append(env, kv)
		}
	}
	return env
}
