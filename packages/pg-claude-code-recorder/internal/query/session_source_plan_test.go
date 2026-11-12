package query

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/sources"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
)

// pa-monitor reads a session's registry records with source='session-registry'
// AND session IN (...) ORDER BY time DESC LIMIT n. A session can hold millions of
// records of one source (the transcript) and a few thousand of another. A unary
// plus on source hides the source test from the planner, which then reads every
// record of the session (11 s for 9.95M records, 12,105 of them registry) instead
// of searching (session_ref=? AND source=?) in records_session_ref_source_time
// (0.1 s). The plus stays on type and item, which that index does not hold.
func TestSessionSourceQuerySearchesSessionAndSource(t *testing.T) {
	st, err := store.Open(t.TempDir(), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	err = st.WithWrite("", func(tx *sql.Tx) error {
		for _, q := range []string{
			`INSERT INTO files(id,source,mode,first_seen_ns,cur_gen) VALUES(1,'claude-transcript','append',1,0)`,
			`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<20) INSERT INTO sessions(session_ref,session_id,first_time_ns) SELECT i,'sess-'||i,0 FROM n`,
			// session 1: 200,000 transcript records; 50 registry records
			`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<200000)
			 INSERT INTO records(id,file_id,gen,off,item,len,source,type,parser_version,time_ns,session_ref)
			 SELECT i,1,0,i,0,1,'claude-transcript','text',1,i,1 FROM n`,
			`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<50)
			 INSERT INTO records(id,file_id,gen,off,item,len,source,type,parser_version,time_ns,session_ref)
			 SELECT 200000+i,1,0,200000+i,0,1,'session-registry','registry',1,300000+i,1 FROM n`,
			// other sessions, each with registry records too
			`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<2000)
			 INSERT INTO records(id,file_id,gen,off,item,len,source,type,parser_version,time_ns,session_ref)
			 SELECT 300000+i,1,0,300000+i,0,1,CASE WHEN i%2=0 THEN 'claude-transcript' ELSE 'session-registry' END,CASE WHEN i%2=0 THEN 'text' ELSE 'registry' END,1,i,i%19+2 FROM n`,
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
	var flt Filter
	if e := Decode([]byte(`{"all":[{"field":"source","eq":"session-registry"},{"field":"session","in":["sess-1","sess-2"]}]}`), &flt); e != nil {
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
	q := "SELECT r.id," + c.expr + base(false) + " " + strings.Join(b.joins, " ") + " WHERE " + cond + " ORDER BY " + c.expr + " DESC, r.id LIMIT ?"
	args := append(append(append([]any{}, b.joinArgs...), wargs...), 5000)
	p := plan(t, st, q, args)
	if !strings.Contains(p, "SEARCH r USING INDEX records_session_ref_source_time (session_ref=? AND source=?)") {
		t.Errorf("records is not searched by (session_ref=? AND source=?):\n%s", p)
	}
	if strings.Contains(p, "SCAN") {
		t.Errorf("a table is scanned:\n%s", p)
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
	// session 1: 50 registry records; session 2: the i%19+2=2 odd i of 1..2000.
	want := 50
	for i := 1; i <= 2000; i++ {
		if i%2 == 1 && i%19+2 == 2 {
			want++
		}
	}
	if got != want {
		t.Errorf("rows = %d, want %d", got, want)
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("took %v", d)
	}
}
