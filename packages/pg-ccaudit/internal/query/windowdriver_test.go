package query

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-ccaudit/internal/store"
)

// The old-SQL oracle (the statements as they were before the window drove the
// outer loop) and the tests that run it live in windowdriver_oracle_test.go
// behind the integration tag: they are far too slow for the unit tier. What
// stays here are the checks that need no old statement.

// changedQueries are the statements whose outer loop is driven by the window.
var changedQueries = []string{"error-rate-by-tool", "interruptions"}

type windowCase struct {
	name         string
	since, until string
}

var windowCases = []windowCase{
	{"unbounded", "", ""},
	{"empty window", "2030-01-01T00:00:00.000Z", "2030-01-02T00:00:00.000Z"},
	{"since only", windowSince, ""},
	{"until only", "", windowUntil},
	{"day window cutting through sessions", windowSince, windowUntil},
}

type flagCase struct {
	name   string
	in, ex []string
}

var flagCases = []flagCase{
	{"none", nil, nil},
	{"include", []string{"/w/a"}, nil},
	{"exclude only", nil, []string{"/w/a/p3"}},
	{"include and exclude", []string{"/w/a", "/w/b"}, []string{"/w/a/p3"}},
}

func runWith(t testing.TB, db *sql.DB, q Query, w windowCase, f flagCase) [][]any {
	t.Helper()
	return runOnce(t, db, Request{Query: q, Since: w.since, Until: w.until, CwdPrefixes: f.in, ExcludeCwdPrefixes: f.ex}).Rows
}

func smallFixture(t testing.TB) *sql.DB {
	t.Helper()
	return shared(t, "small", smallFixtureUncached)
}

func smallFixtureUncached(t testing.TB) *sql.DB {
	sessions, perSess := 90, 240
	if raceBuild {
		// A third of the sessions and two fifths of the lines per
		// session: the old-SQL oracle is quadratic in the lines per
		// session and the race detector makes it 20 to 30 times slower.
		sessions, perSess = 30, 144
	}
	return buildBigFixture(t, bigFixture{sessions: sessions, perSess: perSess})
}

// The fixture carries the rows the equality test exists to cover: calls and
// interruptions with a NULL ts, events with no session, and sessions with no cwd.
func TestFixtureHasNullTsNullSessionAndNoCwdRows(t *testing.T) {
	db := smallFixture(t)
	for name, q := range map[string]string{
		"NULL-ts tool call":              `SELECT COUNT(*) FROM events e JOIN tool_calls c ON c.path = e.path AND c.seq = e.seq WHERE e.ts IS NULL`,
		"NULL-ts interruption":           `SELECT COUNT(*) FROM events e JOIN user_text u ON u.path = e.path AND u.seq = e.seq WHERE u.interrupted = 1 AND e.ts IS NULL`,
		"NULL-session tool call":         `SELECT COUNT(*) FROM events e JOIN tool_calls c ON c.path = e.path AND c.seq = e.seq WHERE e.session_id IS NULL`,
		"NULL-session interruption":      `SELECT COUNT(*) FROM events e JOIN user_text u ON u.path = e.path AND u.seq = e.seq WHERE u.interrupted = 1 AND e.session_id IS NULL`,
		"session with no cwd, with call": `SELECT COUNT(*) FROM events e JOIN tool_calls c ON c.path = e.path AND c.seq = e.seq WHERE e.session_id IS NOT NULL AND e.session_id NOT IN (SELECT session_id FROM events WHERE cwd IS NOT NULL AND session_id IS NOT NULL)`,
	} {
		var n int
		if err := db.QueryRowContext(context.Background(), q).Scan(&n); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if n == 0 {
			t.Errorf("fixture has no %s rows", name)
		}
	}
}

// Windowed runs drop NULL-ts calls and unbounded runs keep them, on both the
// windowed statement and the unbounded one, so the NULL-ts branch of
// windowEvents is what the unbounded equality cases exercise.
func TestNullTsCallsAreCountedOnlyWhenUnbounded(t *testing.T) {
	db := smallFixture(t)
	q, _ := Lookup("error-rate-by-tool")
	total := func(q Query, w windowCase) (n int64) {
		for _, r := range runWith(t, db, q, w, flagCases[0]) {
			n += r[1].(int64)
		}
		return n
	}
	windowedOnly := q
	windowedOnly.UnboundedSQL = ""
	var allCalls, nullTs int64
	if err := db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM tool_calls`).Scan(&allCalls); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM events e JOIN tool_calls c ON c.path = e.path AND c.seq = e.seq WHERE e.ts IS NULL`).Scan(&nullTs); err != nil {
		t.Fatal(err)
	}
	for name, qq := range map[string]Query{"dispatched": q, "windowed statement": windowedOnly} {
		if got := total(qq, windowCases[0]); got != allCalls {
			t.Errorf("%s, unbounded: %d calls, want all %d", name, got, allCalls)
		}
		since := total(qq, windowCase{since: "2000-01-01T00:00:00.000Z"})
		if since != allCalls-nullTs {
			t.Errorf("%s, since only: %d calls, want %d (all %d minus %d NULL-ts)", name, since, allCalls-nullTs, allCalls, nullTs)
		}
	}
}

// An unbounded error-rate-by-tool starts at tool_calls (the statement it had
// before the window drove the outer loop); a bounded one does not.
func TestUnboundedErrorRateByToolUsesTheToolCallDrivenPlan(t *testing.T) {
	db := smallFixture(t)
	q, _ := Lookup("error-rate-by-tool")
	if q.UnboundedSQL == "" {
		t.Fatal("error-rate-by-tool has no unbounded statement")
	}
	u := q
	u.SQL = q.UnboundedSQL
	plan := planOf(t, db, u, windowCases[0], flagCases[1])
	top := topLevel(plan)
	if len(top) < 2 || !strings.Contains(top[0], "SCAN c USING INDEX idx_calls_name") ||
		!strings.Contains(top[1], "SEARCH e USING INDEX sqlite_autoindex_events_1 (path=? AND seq=?)") {
		t.Errorf("unbounded statement is not tool_calls then an events search:\n%s", plan)
	}
}

func planOf(t testing.TB, db *sql.DB, q Query, w windowCase, f flagCase) string {
	t.Helper()
	req := Request{Query: q, Since: w.since, Until: w.until, CwdPrefixes: f.in, ExcludeCwdPrefixes: f.ex}
	args, err := req.Bind()
	if err != nil {
		t.Fatal(err)
	}
	rows, err := db.QueryContext(context.Background(), "EXPLAIN QUERY PLAN "+q.SQL, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var sb strings.Builder
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&sb, "%d<%d %s\n", id, parent, detail)
	}
	return sb.String()
}

// planLines returns the plan's detail lines whose parent is 0 (the outer
// statement, not a subquery body) and, separately, all lines.
func topLevel(plan string) []string {
	var out []string
	for _, l := range strings.Split(plan, "\n") {
		if strings.Contains(l, "<0 ") {
			out = append(out, l)
		}
	}
	return out
}

// The window drives the outer query: events are read through idx_events_ts (as
// the window co-routine) before tool_calls is touched, and tool_calls is only
// ever reached by a search, never scanned.
func TestWindowDrivesTheOuterQuery(t *testing.T) {
	db := smallFixture(t)
	for _, name := range changedQueries {
		q, _ := Lookup(name)
		for _, f := range flagCases {
			t.Run(name+"/"+f.name, func(t *testing.T) {
				plan := planOf(t, db, q, windowCases[4], f)
				if !strings.Contains(plan, "SEARCH events USING INDEX idx_events_ts (ts>?)") {
					t.Errorf("window does not use idx_events_ts:\n%s", plan)
				}
				for _, l := range strings.Split(plan, "\n") {
					// tool_calls (aliased c) is only ever reached by a search.
					if strings.Contains(l, "SCAN c") || strings.Contains(l, "SCAN tool_calls") {
						t.Errorf("tool_calls scanned without a search term: %s", l)
					}
				}
				switch name {
				case "error-rate-by-tool":
					top := strings.Join(topLevel(plan), "\n")
					if !strings.Contains(top, "SCAN e") || !strings.Contains(top, "SEARCH c USING INDEX idx_calls_pathseq (path=? AND seq=?)") {
						t.Errorf("outer loop is not window rows then a tool_calls search:\n%s", plan)
					}
					if strings.Index(top, "SCAN e") > strings.Index(top, "SEARCH c ") {
						t.Errorf("tool_calls is outside the window:\n%s", plan)
					}
				case "interruptions":
					if !strings.Contains(plan, "SEARCH c USING INDEX idx_calls_pathseq (path=? AND seq=?)") {
						t.Errorf("prev_lines does not search tool_calls by (path, seq):\n%s", plan)
					}
					if !strings.Contains(plan, "MATERIALIZE marks") {
						t.Errorf("marks is not materialized (it would run its scope lookup twice):\n%s", plan)
					}
				}
			})
		}
	}
}

// The three call timestamps of the boundary fixture.
const (
	boundaryT1 = "2026-07-01T10:00:00.000Z"
	boundaryT2 = "2026-07-01T11:00:00.000Z"
	boundaryT3 = "2026-07-01T12:00:00.000Z"
)

// since is inclusive and until is exclusive, a session that starts before since
// but has later in-window events is attributed by its whole history, and rows
// outside the window never count.
func TestWindowBoundariesAndSessionStartingBeforeSince(t *testing.T) {
	db := openBoundaryFixture(t)
	q, _ := Lookup("error-rate-by-tool")
	count := func(since, until string, in []string) int64 {
		rows := runWith(t, db, q, windowCase{since: since, until: until}, flagCase{in: in})
		var n int64
		for _, r := range rows {
			n += r[1].(int64)
		}
		return n
	}
	const t1, t2, t3 = boundaryT1, boundaryT2, boundaryT3
	// Calls sit at t1, t2 and t3 (one each, session "early").
	for _, c := range []struct {
		name         string
		since, until string
		want         int64
	}{
		{"since equal to a call's ts includes it", t2, "", 2},
		{"until equal to a call's ts excludes it", "", t2, 1},
		{"window [t1, t3) holds t1 and t2", t1, t3, 2},
		{"window (t1, t2) after a tick holds none", "2026-07-01T10:00:00.001Z", t2, 0},
	} {
		if got := count(c.since, c.until, nil); got != c.want {
			t.Errorf("%s: calls = %d, want %d", c.name, got, c.want)
		}
	}
	// The session's first cwd (/w/a, at t0, before every call) decides its scope
	// even though the window starts after it.
	if got := count(t2, "", []string{"/w/a"}); got != 2 {
		t.Errorf("session starting before since, include /w/a: calls = %d, want 2", got)
	}
	if got := count(t2, "", []string{"/w/z"}); got != 0 {
		t.Errorf("session starting before since, include /w/z: calls = %d, want 0 (first cwd is /w/a)", got)
	}
}

func openBoundaryFixture(t testing.TB) *sql.DB {
	t.Helper()
	return shared(t, "boundary", buildScopeDBRaw)
}

// buildScopeDBRaw is one session, "early": a bookkeeping line at 09:00 whose cwd
// is /w/a (before every call), then one Bash call at each of 10:00, 11:00 and
// 12:00.
func buildScopeDBRaw(t testing.TB) *sql.DB {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "boundary.db")
	w, err := store.Open(t.Context(), dbPath, false)
	if err != nil {
		t.Fatal(err)
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := w.ExecContext(context.Background(), q, args...); err != nil {
			t.Fatalf("exec %q: %v", q, err)
		}
	}
	exec(`INSERT INTO files (path, project_dir, size, mtime, lines_ok, lines_bad, ingested_at) VALUES ('/t/early.jsonl', '', 0, 0, 1, 0, 0)`)
	exec(`INSERT INTO events (path, seq, session_id, ts, type, is_sidechain, cwd) VALUES ('/t/early.jsonl', 1, 'early', '2026-07-01T09:00:00.000Z', 'system', 0, '/w/a')`)
	for i, hour := range []string{"10", "11", "12"} {
		seq := i + 2
		exec(`INSERT INTO events (path, seq, session_id, ts, type, is_sidechain, cwd) VALUES ('/t/early.jsonl', ?, 'early', ?, 'assistant', 0, '/w/elsewhere')`,
			seq, "2026-07-01T"+hour+":00:00.000Z")
		exec(`INSERT INTO tool_calls (tool_use_id, path, seq, tool_name, input_json, lead_cmd) VALUES (?, '/t/early.jsonl', ?, 'Bash', '{}', 'x')`,
			fmt.Sprintf("b%d", seq), seq)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := store.OpenReadOnly(t.Context(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}
