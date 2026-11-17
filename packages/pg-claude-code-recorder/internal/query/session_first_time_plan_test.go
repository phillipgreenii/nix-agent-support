package query

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/sources"
)

// pa-monitor reads a session's first (and last) record time with one ordered
// single-row query: the transcript source, user or assistant turns, a main-thread
// test on the is_sidechain field, the session pinned, order by time, limit 1.
// The is_sidechain test reads a LEFT JOINed fields row; written as an equality
// it rejects the unmatched rows, so the planner (this test database is unanalyzed; the live store is analyzed daily
// and the plan must hold either way)
// drove the query from fields_name_n over every main-thread record of the store
// and filtered by session afterwards: 23 s for a session of 381 records on a 15 GB
// store. The session index must drive it and fields must be probed per record.
func TestSessionFirstTimeQueryIsDrivenBySessionIndex(t *testing.T) {
	st := sessionPlanStore(t)
	const sessions, perSession = 100, 10
	srcs := sources.All()
	var flt Filter
	if e := Decode([]byte(`{"all":[{"field":"source","eq":"claude-transcript"},{"any":[{"field":"type","eq":"user"},{"field":"type","eq":"assistant"}]},{"field":"is_sidechain","eq":false},{"field":"session","eq":"sess-7"}]}`), &flt); e != nil {
		t.Fatal(e)
	}
	b := newBuilder(sources.Catalog(srcs))
	b.sessionRef = true
	b.pinSession(&flt) // as Engine.rows does
	c, werr := b.col("time")
	if werr != nil {
		t.Fatal(werr)
	}
	cond, wargs, werr := b.where(&flt)
	if werr != nil {
		t.Fatal(werr)
	}
	q := "SELECT r.id," + c.expr + base(false) + " " + strings.Join(b.joins, " ") + " WHERE " + cond + " ORDER BY " + c.expr + " ASC, r.id LIMIT ?"
	args := append(append(append([]any{}, b.joinArgs...), wargs...), 2)
	p := plan(t, st, q, args)
	lines := strings.Split(p, "\n")
	if len(lines) == 0 || !strings.HasPrefix(lines[0], "SEARCH r USING INDEX records_session_ref_source_time (session_ref=?") {
		t.Errorf("records is not the outer table searched through the session index:\n%s", p)
	}
	for _, l := range lines {
		if strings.Contains(l, "SEARCH f0 USING COVERING INDEX fields_name_n") || strings.Contains(l, "SCAN") {
			t.Errorf("fields or records is scanned across sessions (%q):\n%s", l, p)
		}
	}
	start := time.Now()
	rs, err := st.Read().QueryContext(context.Background(), q, args...)
	if err != nil {
		t.Fatal(err)
	}
	got := 0
	for rs.Next() {
		got++
	}
	_ = rs.Close()
	if got != 2 {
		t.Errorf("rows = %d, want 2 (limit 2 over a session of %d records)", got, perSession)
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("took %v over %d records", d, sessions*perSession)
	}
}
