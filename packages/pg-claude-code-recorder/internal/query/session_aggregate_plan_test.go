package query

import (
	"context"
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
	st, err := store.Open(t.TempDir(), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	err = st.WithWrite("", func(tx *sql.Tx) error {
		for _, q := range []string{
			`INSERT INTO files(id,source,mode,first_seen_ns,cur_gen) VALUES(1,'claude-transcript','append',1,0)`,
			`INSERT INTO field_names(id,name) VALUES(1,'is_sidechain')`,
			`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<400) INSERT INTO sessions(session_ref,session_id,first_time_ns) SELECT i,'sess-'||i,0 FROM n`,
			`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<20000)
			 INSERT INTO records(id,file_id,gen,off,item,len,source,type,parser_version,time_ns,session_ref)
			 SELECT i,1,0,i,-1,1,'claude-transcript',CASE WHEN i%2=0 THEN 'user' ELSE 'assistant' END,1,i,i%400+1 FROM n`,
			`INSERT INTO fields(rec_id,name_id,n) SELECT id,1,0 FROM records`,
		} {
			if _, err := tx.ExecContext(context.Background(), q); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
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
	// 20000 records over 400 sessions: 50 per session, all of sess-7 user lines.
	if len(ans.Rows) != 1 || ans.Rows[0][0] != int64(50) {
		t.Errorf("rows = %v, want [[50]]", ans.Rows)
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
