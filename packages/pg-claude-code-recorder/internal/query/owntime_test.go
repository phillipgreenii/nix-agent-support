package query_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/testkit"
)

// ownTimeFixture has, per source, lines with their own timestamp and lines
// without one (whose time_ns is the time the recorder saw the bytes), and an
// assistant line whose tool_use child records carry the line's time_ns but no
// timestamp of their own.
const ownTimeFixture = `{"type":"user","uuid":"o1","sessionId":"example-session-1","timestamp":"2026-10-29T20:00:00Z","message":{"role":"user","content":"first words"}}
{"type":"assistant","uuid":"o2","parentUuid":"o1","sessionId":"example-session-1","timestamp":"2026-10-29T20:00:01.5Z","message":{"id":"m1","role":"assistant","content":[{"type":"text","text":"reply words"},{"type":"tool_use","id":"tu_1","name":"Bash","input":{"command":"echo hi"}}]}}
{"type":"user","uuid":"o3","parentUuid":"o2","sessionId":"example-session-1","message":{"role":"user","content":"no timestamp here"}}
{"type":"assistant","uuid":"o4","parentUuid":"o3","sessionId":"example-session-1","message":{"id":"m2","role":"assistant","content":[{"type":"tool_use","id":"tu_2","name":"Bash","input":{"command":"echo two"}}]}}
{"type":"user","uuid":"o5","parentUuid":"o4","sessionId":"example-session-1","timestamp":"2026-10-29T20:00:04Z","message":{"role":"user","content":"last words"}}
`

const ownTimeHistory = `{"display":"with a time","timestamp":1761768000000,"project":"/example/work","sessionId":"example-session-1"}` + "\n" +
	`{"display":"without a time","project":"/example/work"}` + "\n" +
	`{"display":"fractional","timestamp":1761768001500.5,"project":"/example/work"}` + "\n"

// ownTimeRequests are every query shape over the timestamp field: each operator,
// a select, a sort in both directions, a group, an aggregate, an existence test,
// negation, and a follow.
var ownTimeRequests = []string{
	`{"where":{"field":"timestamp","eq":"2026-10-29T20:00:00Z"},"select":["uuid","timestamp"]}`,
	`{"where":{"field":"timestamp","eq":"2026-10-29T20:00:01.5Z"},"select":["uuid","timestamp","item"]}`,
	`{"where":{"field":"timestamp","in":["2026-10-29T20:00:00Z","2026-10-29T20:00:04Z"]},"select":["uuid","timestamp"],"order":[{"by":"uuid"}]}`,
	`{"where":{"field":"timestamp","range":{"from":"2026-10-29T20:00:00Z","to":"2026-10-29T20:00:02Z"}},"select":["uuid","type","item","timestamp"],"order":[{"by":"file"},{"by":"offset"},{"by":"item"}]}`,
	`{"where":{"field":"timestamp","range":{"gte":"2020-01-01T00:00:00Z"}},"select":["source","type","item","timestamp"],"order":[{"by":"source"},{"by":"type"},{"by":"item"},{"by":"timestamp"}]}`,
	`{"where":{"field":"timestamp","range":{"gt":"2026-10-29T20:00:00Z","lte":"2026-10-29T20:00:04Z"}},"select":["uuid","timestamp"],"order":[{"by":"timestamp","dir":"desc"}]}`,
	`{"where":{"field":"timestamp","exists":true},"select":["source","type","item"],"order":[{"by":"source"},{"by":"type"},{"by":"item"}]}`,
	`{"where":{"field":"timestamp","exists":false},"select":["source","type","item"],"order":[{"by":"source"},{"by":"type"},{"by":"item"}]}`,
	`{"where":{"not":{"field":"timestamp","exists":true}},"select":["source","type","item"],"order":[{"by":"source"},{"by":"type"},{"by":"item"}]}`,
	`{"where":{"not":{"field":"timestamp","eq":"2026-10-29T20:00:00Z"}},"select":["source","type","item","timestamp"],"order":[{"by":"source"},{"by":"type"},{"by":"item"}]}`,
	`{"where":{"all":[{"field":"timestamp","exists":true},{"field":"source","eq":"claude-history"}]},"select":["type","timestamp"],"order":[{"by":"timestamp"}]}`,
	`{"select":["source","type","item","timestamp"],"order":[{"by":"source"},{"by":"type"},{"by":"item"},{"by":"timestamp","dir":"desc"}]}`,
	`{"group":["source","timestamp"],"aggregate":[{"fn":"count","as":"n"}],"order":[{"by":"source"},{"by":"timestamp"}]}`,
	`{"group":["type"],"aggregate":[{"fn":"min","field":"timestamp","as":"first"},{"fn":"max","field":"timestamp","as":"last"},{"fn":"count","field":"timestamp","as":"n"}],"order":[{"by":"type"}]}`,
	`{"where":{"field":"uuid","eq":"o3"},"select":["uuid","timestamp"],"follow":"parent"}`,
}

// TestOwnTimestampServedFromTimeNs parses lines with and without their own
// timestamp, in the transcript and history sources, and asks every request
// shape three ways: as parsed (no timestamp rows stored, the marker serving the
// field from time_ns), with the former timestamp rows written and the marker
// cleared, and with both rows and marker. All three must give the same answer.
func TestOwnTimestampServedFromTimeNs(t *testing.T) {
	k := testkit.New(t)
	k.Add("example-p1", "orig.jsonl", ownTimeFixture)
	k.Add("", "history.jsonl", ownTimeHistory)
	k.Parse()

	if n := fieldCount(t, k, "timestamp"); n != 0 {
		t.Fatalf("%d timestamp rows stored", n)
	}
	// Non-vacuity: some records carry the marker, some lines and every child do not.
	marked := k.Count(`SELECT COUNT(*) FROM records WHERE own_time=1`)
	if marked != 5 {
		t.Fatalf("%d marked records, want the 3 transcript lines and 2 history entries with a timestamp", marked)
	}
	if n := k.Count(`SELECT COUNT(*) FROM records WHERE own_time=0 AND item=-1`); n != 3 {
		t.Fatalf("%d unmarked lines, want the 2 transcript lines and 1 history entry without a timestamp", n)
	}
	if n := k.Count(`SELECT COUNT(*) FROM records WHERE own_time=0 AND item>=0 AND time_ns>0`); n == 0 {
		t.Fatalf("no unmarked child records with the line's time_ns")
	}

	run := func() []string {
		var out []string
		for _, req := range ownTimeRequests {
			a := qe(t, k.Engine, req)
			out = append(out, js(a.Columns)+js(a.Rows))
		}
		return out
	}
	served := run()
	for i, req := range ownTimeRequests {
		if !strings.Contains(served[i], "[[") && !strings.Contains(served[i], "[]") {
			t.Fatalf("%s: no answer: %s", req, served[i])
		}
	}
	// Absolute counts, so a wrong answer common to all three modes still fails.
	total := k.Count(`SELECT COUNT(*) FROM records`)
	if got := len(qe(t, k.Engine, ownTimeRequests[6]).Rows); got != marked {
		t.Fatalf("exists:true returned %d rows, want the %d marked records", got, marked)
	}
	if got := len(qe(t, k.Engine, ownTimeRequests[7]).Rows); got != total-marked {
		t.Fatalf("exists:false returned %d rows, want the %d unmarked records", got, total-marked)
	}
	// A line with no timestamp of its own must not match its observation time.
	var obs int64
	if err := k.Store.Read().QueryRowContext(context.Background(), `SELECT time_ns FROM records WHERE own_time=0 AND item=-1 AND source='claude-transcript' ORDER BY id LIMIT 1`).Scan(&obs); err != nil {
		t.Fatal(err)
	}
	ob := qe(t, k.Engine, `{"where":{"field":"timestamp","eq":"`+time.Unix(0, obs).UTC().Format(time.RFC3339Nano)+`"},"select":["uuid"]}`)
	if len(ob.Rows) != 0 {
		t.Fatalf("a line without a timestamp matched its observation time: %s", js(ob.Rows))
	}
	// And the answers carry values: a timestamp is selected for the marked lines.
	sel := qe(t, k.Engine, `{"where":{"field":"uuid","eq":"o2"},"select":["timestamp"]}`)
	if js(sel.Rows) != `[["2026-10-29T20:00:01.5Z"]]` {
		t.Fatalf("selected timestamp: %s", js(sel.Rows))
	}

	// The baseline: the rows the parser stored before, and no marker.
	setMarker := func(on bool) {
		err := k.Store.WithWrite("", func(tx *sql.Tx) error {
			if !on {
				_, err := tx.ExecContext(context.Background(), `UPDATE records SET own_time=0`)
				return err
			}
			_, err := tx.ExecContext(context.Background(), `UPDATE records SET own_time=1 WHERE id IN (SELECT rec_id FROM fields WHERE name_id=(SELECT id FROM field_names WHERE name='timestamp'))`)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	err := k.Store.WithWrite("", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(context.Background(), `INSERT INTO field_names(name) VALUES('timestamp') ON CONFLICT(name) DO NOTHING`); err != nil {
			return err
		}
		_, err := tx.ExecContext(context.Background(), `INSERT INTO fields(rec_id,name_id,n) SELECT id,(SELECT id FROM field_names WHERE name='timestamp'),time_ns FROM records WHERE own_time=1`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	setMarker(false)
	stored := run()
	setMarker(true)
	both := run()
	for i, req := range ownTimeRequests {
		if served[i] != stored[i] || served[i] != both[i] {
			t.Errorf("%s\n served %s\n stored %s\n both   %s", req, served[i], stored[i], both[i])
		}
	}
}

// A since/until time filter never returns an untimed row: its time_ns is 0, so
// an upper bound alone must not match it.
func TestTimeFilterNeverReturnsAnUntimedRow(t *testing.T) {
	k := testkit.New(t)
	k.Add("example-p1", "orig.jsonl", ownTimeFixture)
	k.Parse()
	if n := k.Count(`SELECT COUNT(*) FROM records WHERE item=-1 AND time_ns=0`); n != 2 {
		t.Fatalf("%d untimed lines stored with time 0, want 2", n)
	}
	for _, req := range []string{
		`{"where":{"field":"time","range":{"to":"2030-01-01T00:00:00Z"}},"select":["uuid"],"order":[{"by":"uuid"}]}`,
		`{"where":{"field":"time","range":{"lte":"2030-01-01T00:00:00Z"}},"select":["uuid"],"order":[{"by":"uuid"}]}`,
		`{"where":{"field":"time","range":{"gte":"1970-01-01T00:00:00Z"}},"select":["uuid"],"order":[{"by":"uuid"}]}`,
	} {
		a := qe(t, k.Engine, req)
		for _, r := range a.Rows {
			if len(r) == 1 && r[0] == "o3" || r[0] == "o4" {
				t.Fatalf("%s returned an untimed line: %s", req, js(a.Rows))
			}
		}
		if len(a.Rows) == 0 {
			t.Fatalf("%s returned nothing", req)
		}
	}
}
