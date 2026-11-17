package query

import (
	"database/sql"
	"encoding/json"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/sources"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
)

// pa-monitor counts a session's user turns with an aggregate pinned to the
// session that also tests the LEFT JOINed is_sidechain field. As an equality
// that test turned the join into an inner join and drove the query from
// fields_name_n across every main-thread record of the store (25 s deadline on
// a 17 GB store, IO-bound: about 4 s of CPU in 32 s). The aggregate path must
// pin the session like a row query so the session index drives.
func TestSessionPinnedAggregateIsDrivenBySessionIndex(t *testing.T) {
	st := sessionPlanStore(t)
	srcs := sources.All()
	const reqJSON = `{"from":"records","limit":10000,"aggregate":[{"fn":"count","as":"n"}],"where":{"all":[{"field":"source","eq":"claude-transcript"},{"field":"is_sidechain","eq":false},{"field":"session","eq":"sess-7"},{"field":"type","eq":"user"},{"field":"item","eq":-1}]}}`
	var req Request
	if err := json.Unmarshal([]byte(reqJSON), &req); err != nil {
		t.Fatal(err)
	}

	// Run the real aggregate path over a recording Querier so the plan is read
	// for the exact SQL it issues, not for a re-typed query.
	e := &Engine{DB: st.Read(), Catalog: sources.Catalog(srcs), Storage: sources.Storage(srcs), SessionRef: st.SessionRefReady, TypedStorage: st.HasExt}
	rec := &recordingQuerier{Querier: st.Read()}
	ans := &Answer{Rows: [][]any{}}
	if werr := e.aggregate(rec, req, ans); werr != nil {
		t.Fatal(werr)
	}
	if len(rec.sql) != 1 {
		t.Fatalf("aggregate issued %d statements, want 1", len(rec.sql))
	}
	p := plan(t, st, rec.sql[0], rec.args[0])
	lines := strings.Split(p, "\n")
	if len(lines) == 0 || !strings.HasPrefix(lines[0], "SEARCH r USING INDEX records_session_ref_source_time (session_ref=?") {
		t.Errorf("records is not the outer table searched through the session index:\n%s", p)
	}
	for _, l := range lines {
		if strings.Contains(l, "SEARCH f0 USING COVERING INDEX fields_name_n") || strings.Contains(l, "SCAN") {
			t.Errorf("fields or records is scanned across sessions (%q):\n%s", l, p)
		}
	}
	// 4000 records over 400 sessions: 10 per session, all of sess-7 user lines (even ids).
	if len(ans.Rows) != 1 || ans.Rows[0][0] != int64(10) {
		t.Errorf("rows = %v, want [[10]]", ans.Rows)
	}
}

// recordingQuerier remembers each statement and its arguments.
type recordingQuerier struct {
	store.Querier
	sql  []string
	args [][]any
}

func (r *recordingQuerier) Query(q string, args ...any) (*sql.Rows, error) {
	r.sql, r.args = append(r.sql, q), append(r.args, args)
	return r.Querier.Query(q, args...)
}
