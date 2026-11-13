package query

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-ccaudit/internal/store"
)

// oldSQL is the text of each changed query before the window was made to drive
// its outer loop. It is the oracle: the new statement must return exactly the
// rows this one does. (Both are composed from the shared scoped()/prevToolAgg
// fragments, which this change did not touch.)
var oldSQL = map[string]string{
	"error-rate-by-tool": `
SELECT c.tool_name                                                             AS tool_name,
       COUNT(*)                                                                AS calls,
       SUM(CASE WHEN r.is_error = 1 THEN 1 ELSE 0 END)                         AS errors,
       ROUND(100.0 * SUM(CASE WHEN r.is_error = 1 THEN 1 ELSE 0 END) / COUNT(*), 3) AS error_pct
FROM tool_calls c
JOIN events e ON e.path = c.path AND e.seq = c.seq
LEFT JOIN tool_results r ON r.tool_use_id = c.tool_use_id
WHERE ` + scoped("e") + `
GROUP BY c.tool_name
ORDER BY errors DESC, calls DESC, tool_name`,
	"interruptions": `
WITH ` + prevToolAgg + `,
marks AS (
  SELECT e.path AS path, e.seq AS seq, e.session_id AS session_id,
         COALESCE(e.is_sidechain, 0) AS is_sidechain, e.ts AS ts, u.text AS text,
         (SELECT MAX(c.seq) FROM tool_calls c
           WHERE c.path = e.path AND c.seq < e.seq) AS prev_seq
  FROM user_text u
  JOIN events e ON e.path = u.path AND e.seq = u.seq
  WHERE u.interrupted = 1 AND ` + scoped("e") + `
)
SELECT m.session_id                  AS session_id,
       m.is_sidechain                AS is_sidechain,
       m.path                        AS path,
       m.seq                         AS seq,
       m.ts                          AS ts,
       p.seq                         AS prev_tool_seq,
       pe.ts                         AS prev_ts,
       COALESCE(p.calls, 0)          AS prev_tool_calls,
       p.tool_names                  AS prev_tool_names,
       p.lead_cmds                   AS prev_lead_cmds,
       m.text                        AS marker
FROM marks m
LEFT JOIN events pe ON pe.path = m.path AND pe.seq = m.prev_seq
                   AND pe.session_id IS m.session_id
LEFT JOIN prev_lines p ON p.path = pe.path AND p.seq = pe.seq
ORDER BY m.path, m.seq`,
}

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
	res, err := Run(context.Background(), db, Request{Query: q, Since: w.since, Until: w.until, CwdPrefixes: f.in, ExcludeCwdPrefixes: f.ex})
	if err != nil {
		t.Fatalf("%s: %v", q.Name, err)
	}
	return res.Rows
}

func smallFixture(t testing.TB) *sql.DB {
	return buildBigFixture(t, bigFixture{sessions: 90, perSess: 240})
}

// The new statements return exactly the old statements' rows over every window
// and flag combination, on a fixture whose day window cuts through sessions.
func TestWindowDrivenQueriesEqualTheirOldSQL(t *testing.T) {
	db := smallFixture(t)
	for _, name := range changedQueries {
		newQ, err := Lookup(name)
		if err != nil {
			t.Fatal(err)
		}
		oldQ := newQ
		oldQ.SQL = oldSQL[name]
		for _, w := range windowCases {
			for _, f := range flagCases {
				t.Run(name+"/"+w.name+"/"+f.name, func(t *testing.T) {
					got, want := runWith(t, db, newQ, w, f), runWith(t, db, oldQ, w, f)
					if !reflect.DeepEqual(got, want) {
						t.Errorf("new rows differ from old\n got %d rows: %v\nwant %d rows: %v", len(got), head(got), len(want), head(want))
					}
					if w.name == "day window cutting through sessions" && f.name == "none" && len(want) == 0 {
						t.Error("oracle returned no rows for the day window: the fixture proves nothing")
					}
				})
			}
		}
	}
}

func head(rows [][]any) [][]any {
	if len(rows) > 3 {
		return rows[:3]
	}
	return rows
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

// The old statement is the one whose plan scanned every tool call: keep that
// visible so the plan assertion above cannot pass vacuously.
func TestOldSQLScannedEveryToolCall(t *testing.T) {
	db := smallFixture(t)
	q, _ := Lookup("error-rate-by-tool")
	q.SQL = oldSQL["error-rate-by-tool"]
	plan := planOf(t, db, q, windowCases[4], flagCases[0])
	if !strings.Contains(plan, "SCAN c USING INDEX idx_calls_name") {
		t.Errorf("oracle plan no longer scans tool_calls; the plan assertion is not discriminating:\n%s", plan)
	}
}

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
	const t1, t2, t3 = "2026-07-01T10:00:00.000Z", "2026-07-01T11:00:00.000Z", "2026-07-01T12:00:00.000Z"
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
	// And the oracle agrees on every one of these.
	oldQ := q
	oldQ.SQL = oldSQL["error-rate-by-tool"]
	for _, w := range []windowCase{{"", t2, ""}, {"", "", t2}, {"", t1, t3}} {
		for _, f := range flagCases {
			if got, want := runWith(t, db, q, w, f), runWith(t, db, oldQ, w, f); !reflect.DeepEqual(got, want) {
				t.Errorf("window %+v flags %+v: new %v, old %v", w, f, got, want)
			}
		}
	}
}

func openBoundaryFixture(t testing.TB) *sql.DB {
	t.Helper()
	db := buildScopeDBRaw(t)
	return db
}

// Opt-in timing: PG_CCAUDIT_BIGFIXTURE=1 builds a corpus shaped like the live
// one (2M events, ~128k tool calls) and times old against new SQL. It is a
// measurement, not a gate, and is skipped in the unit run.
func TestWindowDrivenQueryTimingsOnBigFixture(t *testing.T) {
	if os.Getenv("PG_CCAUDIT_BIGFIXTURE") == "" {
		t.Skip("set PG_CCAUDIT_BIGFIXTURE=1 to run the 2M-event timing comparison")
	}
	db := buildBigFixture(t, bigFixture{sessions: 1200, perSess: 1700})
	for _, name := range changedQueries {
		newQ, _ := Lookup(name)
		oldQ := newQ
		oldQ.SQL = oldSQL[name]
		for _, w := range windowCases {
			f := flagCase{in: []string{"/w/a"}, ex: []string{"/w/a/p3"}}
			if w.name != "unbounded" && w.name != "day window cutting through sessions" {
				continue // the other windows are covered for equality, not timed
			}
			for _, pass := range []string{"cold", "warm"} {
				t0 := time.Now()
				rOld := runWith(t, db, oldQ, w, f)
				dOld := time.Since(t0)
				t0 = time.Now()
				rNew := runWith(t, db, newQ, w, f)
				dNew := time.Since(t0)
				equal := reflect.DeepEqual(rOld, rNew)
				t.Logf("%s | %s | %s: old %v new %v rows %d equal %v", name, w.name, pass, dOld, dNew, len(rNew), equal)
				if !equal {
					t.Errorf("rows differ")
				}
			}
		}
	}
}

// buildScopeDBRaw is one session, "early": a bookkeeping line at 09:00 whose cwd
// is /w/a (before every call), then one Bash call at each of 10:00, 11:00 and
// 12:00.
func buildScopeDBRaw(t testing.TB) *sql.DB {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "boundary.db")
	w, err := store.Open(dbPath, false)
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
	db, err := store.OpenReadOnly(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}
