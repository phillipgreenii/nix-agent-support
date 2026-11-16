//go:build integration

package query

import (
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

// The old-SQL oracle: the full cross product of window and flag cases run
// against both the old and the new statement. Under the race detector it costs
// about 160 G instructions (the old statements are quadratic in the lines per
// session), far over the unit-test budget, so it sits behind the integration tag
// and runs in checks.<system>.pg-ccaudit-integration-tests, not in the unit gate.
// The unit tier keeps the plan assertions, the boundary cases and the
// hand-computed counts that do not need the old statements.

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
		// The windowed statement itself, even for an unbounded run (Run swaps in
		// UnboundedSQL there, which would make the unbounded case trivial).
		newQ.UnboundedSQL = ""
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

// The old statement is the one whose plan scanned every tool call: keep that
// visible so the plan assertion in the unit tier cannot pass vacuously.
func TestOldSQLScannedEveryToolCall(t *testing.T) {
	db := smallFixture(t)
	q, _ := Lookup("error-rate-by-tool")
	q.SQL = oldSQL["error-rate-by-tool"]
	plan := planOf(t, db, q, windowCases[4], flagCases[0])
	if !strings.Contains(plan, "SCAN c USING INDEX idx_calls_name") {
		t.Errorf("oracle plan no longer scans tool_calls; the plan assertion is not discriminating:\n%s", plan)
	}
}

// The oracle agrees with the new statement on every boundary window the unit
// tier checks by hand.
func TestWindowBoundariesAgreeWithOldSQL(t *testing.T) {
	db := openBoundaryFixture(t)
	q, _ := Lookup("error-rate-by-tool")
	oldQ := q
	oldQ.SQL = oldSQL["error-rate-by-tool"]
	for _, w := range []windowCase{{"", boundaryT2, ""}, {"", "", boundaryT2}, {"", boundaryT1, boundaryT3}} {
		for _, f := range flagCases {
			if got, want := runWith(t, db, q, w, f), runWith(t, db, oldQ, w, f); !reflect.DeepEqual(got, want) {
				t.Errorf("window %+v flags %+v: new %v, old %v", w, f, got, want)
			}
		}
	}
}

// Opt-in timing: PG_CCAUDIT_BIGFIXTURE=1 builds a corpus shaped like the live
// one (2M events, ~128k tool calls) and times old against new SQL. It is a
// measurement, not a gate, and is skipped in the integration run.
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
				if w.name == "unbounded" && newQ.UnboundedSQL != "" {
					// What the unbounded run would cost without the dispatch.
					wq := newQ
					wq.UnboundedSQL = ""
					t0 = time.Now()
					rWin := runWith(t, db, wq, w, f)
					t.Logf("%s | unbounded | %s: windowed statement (no dispatch) %v, dispatched %v, old %v", name, pass, time.Since(t0), dNew, dOld)
					if !reflect.DeepEqual(rOld, rWin) {
						t.Errorf("windowed statement rows differ")
					}
				}
				if !equal {
					t.Errorf("rows differ")
				}
			}
		}
	}
}
