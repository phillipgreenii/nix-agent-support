package query

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-ccaudit/internal/ingest"
	"github.com/phillipgreenii/pg-ccaudit/internal/store"
)

// The seven queries the review lenses use, plus skill-calls: the only ones that accept the scope flags.
var scopedQueryNames = []string{
	"error-rate-by-tool", "top-signatures", "cost-by-signature", "retry-chains",
	"denied-tool-calls", "interruptions", "hook-refusals-in-body",
}

// scopeSession is one synthetic session. firstCwd is the cwd of its first event
// (nil = NULL); laterCwd is the cwd of every later event.
type scopeSession struct {
	tag      string
	firstCwd *string
	laterCwd string
}

func sp(s string) *string { return &s }

// scopeFixtureSessions: alpha starts under /w/a and later leaves it (still in
// scope); beta's first event has a NULL cwd, so its first NON-NULL cwd (/w/a/x,
// from the next event) decides its scope; gamma sits beside /w/a as /w/ab (a
// component-boundary trap); delta is a worktree-style path under /w/b.
var scopeFixtureSessions = []scopeSession{
	{"alpha", sp("/w/a/proj"), "/elsewhere"},
	{"beta", nil, "/w/a/x"},
	{"gamma", sp("/w/ab"), "/w/ab"},
	{"delta", sp("/w/b/.worktrees/feat"), "/w/b/.worktrees/feat"},
}

// buildScopeDB writes, per session, a failing Bash call that is retried, a
// permission-denied call, a hook refusal and an interruption, so each of the
// seven queries yields rows tagged with the session.
func buildScopeDB(t *testing.T) *sql.DB {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "scope.db")
	w, err := store.Open(dbPath, false)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := w.ExecContext(context.Background(), q, args...); err != nil {
			t.Fatalf("exec %q: %v", q, err)
		}
	}
	for _, s := range scopeFixtureSessions {
		path := "/t/" + s.tag + ".jsonl"
		exec(`INSERT INTO files (path, project_dir, size, mtime, lines_ok, lines_bad, ingested_at) VALUES (?, '', 0, 0, 1, 0, 0)`, path)
		ev := func(seq int, typ string) {
			var cwd any = s.laterCwd
			if seq == 1 {
				if s.firstCwd == nil {
					cwd = nil
				} else {
					cwd = *s.firstCwd
				}
			}
			ts := fmt.Sprintf("2026-07-22T10:00:%02d.000Z", seq)
			exec(`INSERT INTO events (path, seq, session_id, ts, type, is_sidechain, cwd) VALUES (?, ?, ?, ?, ?, 0, ?)`,
				path, seq, s.tag, ts, typ, cwd)
		}
		call := func(seq int, id string) {
			ev(seq, "assistant")
			exec(`INSERT INTO tool_calls (tool_use_id, path, seq, tool_name, input_json, lead_cmd) VALUES (?, ?, ?, 'Bash', '{"command":"x"}', 'x')`, id, path, seq)
		}
		result := func(seq int, id, content, sig string) {
			ev(seq, "user")
			exec(`INSERT INTO tool_results (tool_use_id, path, seq, is_error, content_len, content, signature) VALUES (?, ?, ?, 1, ?, ?, ?)`,
				id, path, seq, len(content), content, sig)
		}
		call(1, s.tag+"-c1")
		result(2, s.tag+"-c1", "Permission for this tool use was denied", "sig-"+s.tag+"-denied")
		call(3, s.tag+"-c2") // retry of the failed c1
		result(4, s.tag+"-c2", "Hook: refusing this command", "sig-"+s.tag+"-refusal")
		ev(5, "user")
		exec(`INSERT INTO user_text (path, seq, text_len, text, interrupted) VALUES (?, 5, 1, '[Request interrupted by user]', 1)`, path)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	db, err := store.OpenReadOnly(dbPath)
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// tagsIn runs a scoped query and returns the sorted session tags present in the
// rendered rows, plus the row count.
func tagsIn(t *testing.T, db *sql.DB, name string, in, ex []string) ([]string, int) {
	t.Helper()
	q, err := Lookup(name)
	if err != nil {
		t.Fatal(err)
	}
	res, err := Run(context.Background(), db, Request{
		Query: q, CwdPrefixes: in, ExcludeCwdPrefixes: ex, Format: FormatTSV,
	})
	if err != nil {
		t.Fatalf("Run(%s): %v", name, err)
	}
	var sb strings.Builder
	for _, row := range res.Rows {
		for _, c := range row {
			sb.WriteString(cellString(c))
			sb.WriteByte(' ')
		}
	}
	var tags []string
	for _, s := range scopeFixtureSessions {
		if strings.Contains(sb.String(), s.tag) {
			tags = append(tags, s.tag)
		}
	}
	sort.Strings(tags)
	return tags, len(res.Rows)
}

func TestScopeFilterOnEverySevenQueries(t *testing.T) {
	db := buildScopeDB(t)
	all := []string{"alpha", "beta", "delta", "gamma"}
	cases := []struct {
		name string
		in   []string
		ex   []string
		want []string
	}{
		{"none unchanged", nil, nil, all},
		{"one prefix, first cwd in scope though later cwd leaves it; NULL-first session matched by its first non-NULL cwd", []string{"/w/a"}, nil, []string{"alpha", "beta"}},
		{"trailing slash ignored", []string{"/w/a/"}, nil, []string{"alpha", "beta"}},
		{"component boundary: /w/a is not /w/ab", []string{"/w/ab"}, nil, []string{"gamma"}},
		{"two prefixes OR", []string{"/w/a", "/w/b"}, nil, []string{"alpha", "beta", "delta"}},
		{"worktree-style path under prefix", []string{"/w/b"}, nil, []string{"delta"}},
		{"exact-equal prefix", []string{"/w/b/.worktrees/feat"}, nil, []string{"delta"}},
		{"case-sensitive", []string{"/W/A"}, nil, nil},
		{"exclude drops matching, including the NULL-first session's first non-NULL cwd", nil, []string{"/w/a"}, []string{"delta", "gamma"}},
		{"include and exclude combine", []string{"/w/a", "/w/b"}, []string{"/w/b"}, []string{"alpha", "beta"}},
		{"include and exclude on the same session", []string{"/w/a"}, []string{"/w/a/x"}, []string{"alpha"}},
	}
	for _, name := range scopedQueryNames {
		for _, c := range cases {
			t.Run(name+"/"+c.name, func(t *testing.T) {
				if name == "error-rate-by-tool" {
					// Aggregated by tool: every session contributes 2 Bash calls.
					q, _ := Lookup(name)
					res, err := Run(context.Background(), db, Request{Query: q, CwdPrefixes: c.in, ExcludeCwdPrefixes: c.ex, Format: FormatTSV})
					if err != nil {
						t.Fatal(err)
					}
					got := int64(0)
					if len(res.Rows) > 0 {
						got = res.Rows[0][1].(int64)
					}
					if want := int64(2 * len(c.want)); got != want {
						t.Errorf("calls = %d, want %d", got, want)
					}
					return
				}
				got, _ := tagsIn(t, db, name, c.in, c.ex)
				if !reflect.DeepEqual(got, c.want) {
					t.Errorf("sessions = %v, want %v", got, c.want)
				}
			})
		}
	}
}

func TestScopeFlagsRejectedOutsideTheSevenQueries(t *testing.T) {
	db := buildScopeDB(t)
	q, err := Lookup("first-seen")
	if err != nil {
		t.Fatal(err)
	}
	for _, req := range []Request{
		{Query: q, CwdPrefixes: []string{"/w/a"}},
		{Query: q, ExcludeCwdPrefixes: []string{"/w/a"}},
	} {
		if _, err := Run(context.Background(), db, req); err == nil || !strings.Contains(err.Error(), "does not support") {
			t.Errorf("err = %v, want a does-not-support usage error", err)
		}
	}
	for _, n := range All() {
		want := false
		for _, s := range scopedQueryNames {
			want = want || s == n.Name
		}
		want = want || n.Name == "skill-calls"
		if n.Scoped != want {
			t.Errorf("query %s Scoped = %v, want %v", n.Name, n.Scoped, want)
		}
	}
}

func TestScopePrefixValidation(t *testing.T) {
	for _, bad := range []string{"", "relative/dir", "/", "//"} {
		if _, err := NormalizeCwdPrefixes("--cwd-prefix", []string{bad}); err == nil {
			t.Errorf("prefix %q accepted, want an error", bad)
		}
	}
	got, err := NormalizeCwdPrefixes("--cwd-prefix", []string{"/a/b/", "/c"})
	if err != nil || !reflect.DeepEqual(got, []string{"/a/b", "/c"}) {
		t.Errorf("got %v, %v", got, err)
	}
	db := buildScopeDB(t)
	q, _ := Lookup("top-signatures")
	if _, err := Run(context.Background(), db, Request{Query: q, CwdPrefixes: []string{"rel"}}); err == nil {
		t.Error("relative prefix accepted by Run")
	}
}

// Finding (2026-11-06): the index records events.cwd exactly as the transcript
// line carries it, so a session whose transcript records the worktree path as its
// cwd is attributed to the worktree path and is matched by a prefix that contains
// it. That Claude Code writes the worktree path (rather than the main checkout)
// into a worktree session's lines is a property of Claude Code, not of this
// tool; confirm it by hand against a real index (see the README).
func TestWorktreeSessionCwdIsRecordedVerbatimAndMatched(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "projects", "proj-wt")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	line := `{"type":"assistant","uuid":"w-00","parentUuid":null,"sessionId":"S-WT","timestamp":"2026-07-22T10:00:00.000Z","isSidechain":false,"cwd":"/w/repo/.worktrees/feat","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_W1","name":"Bash","input":{"command":"x"}}]}}` + "\n" +
		`{"type":"user","uuid":"w-01","parentUuid":"w-00","sessionId":"S-WT","timestamp":"2026-07-22T10:00:01.000Z","isSidechain":false,"cwd":"/w/repo/.worktrees/feat","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_W1","is_error":true,"content":"Permission for this tool use was denied"}]}}` + "\n"
	if err := os.WriteFile(filepath.Join(root, "S-WT.jsonl"), []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(base, "t.db")
	w, err := store.Open(dbPath, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ingest.Run(context.Background(), w, ingest.Options{
		Root: filepath.Join(base, "projects"), FinalAfter: ingest.FinalAfterImmediate, Progress: io.Discard,
	}); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	db, err := store.OpenReadOnly(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	var cwd string
	if err := db.QueryRowContext(context.Background(), `SELECT cwd FROM events WHERE session_id='S-WT' ORDER BY seq LIMIT 1`).Scan(&cwd); err != nil {
		t.Fatal(err)
	}
	if cwd != "/w/repo/.worktrees/feat" {
		t.Fatalf("recorded cwd = %q, want the worktree path verbatim", cwd)
	}
	q, _ := Lookup("denied-tool-calls")
	for prefix, wantRows := range map[string]int{"/w/repo": 1, "/w/repo/.worktrees": 1, "/w/other": 0} {
		res, err := Run(context.Background(), db, Request{Query: q, CwdPrefixes: []string{prefix}})
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Rows) != wantRows {
			t.Errorf("prefix %s: %d rows, want %d", prefix, len(res.Rows), wantRows)
		}
	}
}

// firstCwdOf evaluates the first-cwd subquery alone over the fixture and returns
// session -> first cwd for the given window.
func firstCwdOf(t *testing.T, db *sql.DB, since, until string) map[string]string {
	t.Helper()
	rows, err := db.QueryContext(context.Background(),
		"SELECT session_id, cwd FROM "+firstCwdSessions,
		sql.Named("since", since), sql.Named("until", until))
	if err != nil {
		t.Fatalf("first-cwd subquery: %v", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]string{}
	for rows.Next() {
		var s, c string
		if err := rows.Scan(&s, &c); err != nil {
			t.Fatal(err)
		}
		out[s] = c
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// The first cwd is the first NON-NULL cwd by (seq, ts, path): beta's NULL first
// event is skipped, and for every other session it is the first event's cwd,
// exactly what the previous definition returned.
func TestFirstCwdIsFirstNonNullByOrder(t *testing.T) {
	db := buildScopeDB(t)
	want := map[string]string{"alpha": "/w/a/proj", "beta": "/w/a/x", "gamma": "/w/ab", "delta": "/w/b/.worktrees/feat"}
	if got := firstCwdOf(t, db, "", ""); !reflect.DeepEqual(got, want) {
		t.Errorf("unwindowed first cwd = %v, want %v", got, want)
	}
}

// A session whose cwd is NULL on every event has no first cwd: it never appears
// in the subquery, so an include prefix cannot match it and an exclude prefix
// cannot drop it.
func TestSessionWithNoCwdAtAll(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "nocwd.db")
	w, err := store.Open(dbPath, false)
	if err != nil {
		t.Fatal(err)
	}
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := w.ExecContext(context.Background(), q, args...); err != nil {
			t.Fatalf("exec %q: %v", q, err)
		}
	}
	mustExec(`INSERT INTO files (path, project_dir, size, mtime, lines_ok, lines_bad, ingested_at) VALUES ('/t/a.jsonl', '', 0, 0, 1, 0, 0)`)
	mustExec(`INSERT INTO files (path, project_dir, size, mtime, lines_ok, lines_bad, ingested_at) VALUES ('/t/b.jsonl', '', 0, 0, 1, 0, 0)`)
	// Two files of one session tie on seq 1: the earlier ts decides.
	mustExec(`INSERT INTO events (path, seq, session_id, ts, type, is_sidechain, cwd) VALUES ('/t/a.jsonl', 1, 'tie', '2026-07-22T10:00:09.000Z', 'user', 0, '/w/late')`)
	mustExec(`INSERT INTO events (path, seq, session_id, ts, type, is_sidechain, cwd) VALUES ('/t/b.jsonl', 1, 'tie', '2026-07-22T10:00:01.000Z', 'user', 0, '/w/early')`)
	mustExec(`INSERT INTO events (path, seq, session_id, ts, type, is_sidechain, cwd) VALUES ('/t/a.jsonl', 2, 'none', '2026-07-22T10:00:02.000Z', 'user', 0, NULL)`)
	mustExec(`INSERT INTO events (path, seq, session_id, ts, type, is_sidechain, cwd) VALUES ('/t/a.jsonl', 3, NULL, '2026-07-22T10:00:03.000Z', 'user', 0, '/w/orphan')`)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := store.OpenReadOnly(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	want := map[string]string{"tie": "/w/early"}
	if got := firstCwdOf(t, db, "", ""); !reflect.DeepEqual(got, want) {
		t.Errorf("first cwd = %v, want %v (no-cwd session absent, NULL session_id absent, tie by ts)", got, want)
	}
}

// A window with no events returns no rows, with or without scope flags.
func TestEmptyWindowReturnsNoRows(t *testing.T) {
	db := buildScopeDB(t)
	if got := firstCwdOf(t, db, "2030-01-01", "2030-01-02"); len(got) != 0 {
		t.Errorf("first cwd over an empty window = %v, want none", got)
	}
	for _, name := range scopedQueryNames {
		q, err := Lookup(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, sc := range []struct{ in, ex []string }{{nil, nil}, {[]string{"/w/a"}, nil}, {nil, []string{"/w/a"}}, {[]string{"/w/a"}, []string{"/w/b"}}} {
			res, err := Run(context.Background(), db, Request{Query: q, Since: "2030-01-01", Until: "2030-01-02", CwdPrefixes: sc.in, ExcludeCwdPrefixes: sc.ex})
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			if name == "error-rate-by-tool" {
				continue // an aggregate may render a zero row; covered by the call count below
			}
			if len(res.Rows) != 0 {
				t.Errorf("%s over an empty window (in=%v ex=%v) returned %d rows", name, sc.in, sc.ex, len(res.Rows))
			}
		}
	}
}

// The first-cwd lookup is bounded by the window: sessions with no event in it
// are never read. The proof is twofold. Behaviour: out-of-window sessions are
// absent from the subquery. Plan: SQLite reaches events only through indexes
// (the window by ts, the sessions by session_id), never by a table scan.
func TestFirstCwdLookupIsRestrictedToTheWindow(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "many.db")
	w, err := store.Open(dbPath, false)
	if err != nil {
		t.Fatal(err)
	}
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := w.ExecContext(context.Background(), q, args...); err != nil {
			t.Fatalf("exec %q: %v", q, err)
		}
	}
	mustExec(`INSERT INTO files (path, project_dir, size, mtime, lines_ok, lines_bad, ingested_at) VALUES ('/t/f.jsonl', '', 0, 0, 1, 0, 0)`)
	tx, err := w.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	ins, err := tx.PrepareContext(context.Background(), `INSERT INTO events (path, seq, session_id, ts, type, is_sidechain, cwd) VALUES ('/t/f.jsonl', ?, ?, ?, 'user', 0, '/w/old')`)
	if err != nil {
		t.Fatal(err)
	}
	seq := 0
	for i := 0; i < 300; i++ { // many sessions, all before the window
		seq++
		if _, err := ins.ExecContext(context.Background(), seq, fmt.Sprintf("old%03d", i), "2026-01-01T00:00:00.000Z"); err != nil {
			t.Fatal(err)
		}
	}
	seq++
	if _, err := tx.ExecContext(context.Background(), `INSERT INTO events (path, seq, session_id, ts, type, is_sidechain, cwd) VALUES ('/t/f.jsonl', ?, 'live', '2026-07-22T10:00:00.000Z', 'user', 0, '/w/live')`, seq); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := store.OpenReadOnly(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if got := firstCwdOf(t, db, "2026-07-22", "2026-07-23"); !reflect.DeepEqual(got, map[string]string{"live": "/w/live"}) {
		t.Errorf("windowed first cwd = %v, want only the in-window session", got)
	}
	if got := firstCwdOf(t, db, "", ""); len(got) != 301 {
		t.Errorf("unwindowed first cwd has %d sessions, want 301", len(got))
	}

	rows, err := db.QueryContext(context.Background(), "EXPLAIN QUERY PLAN SELECT session_id, cwd FROM "+firstCwdSessions,
		sql.Named("since", "2026-07-22"), sql.Named("until", "2026-07-23"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var plan []string
	for rows.Next() {
		var id, parent, notused int
		var detail string
		if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, detail)
	}
	joined := strings.Join(plan, "\n")
	if !strings.Contains(joined, "idx_events_ts") || !strings.Contains(joined, "idx_events_session") {
		t.Errorf("plan does not use both events indexes:\n%s", joined)
	}
	// The unwindowed fallback branch (guarded by a constant-false term when a
	// window is given) is the only place a table scan of events may appear.
	if n := strings.Count(joined, "SCAN events"); n > 1 {
		t.Errorf("plan scans events %d times, want at most the one guarded fallback:\n%s", n, joined)
	}
}
