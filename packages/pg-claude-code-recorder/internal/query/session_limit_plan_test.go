package query

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/sources"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
)

// A row query orders by r.id and takes a limit. With a session filter plus a
// filter on type (and no sqlite_stat1) the planner used to pick records_type,
// which yields rows in id order with no sort, and then walk every row of that
// type looking for the one session: seconds on a large store when the session
// has no match. The session index must serve the lookup instead.
func TestSessionFilteredLimitQuerySearchesTheSessionIndex(t *testing.T) {
	st, err := store.Open(t.TempDir(), store.Options{ExtensionTables: true, V3Layout: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	const n = 100000
	err = st.WithWrite("", func(tx *sql.Tx) error {
		for _, q := range []string{
			`INSERT INTO files(id,source,mode,first_seen_ns,cur_gen) VALUES(1,'claude-transcript','append',1,0)`,
			`INSERT INTO field_names(id,name) VALUES(1,'message.model'),(2,'agent_id')`,
			// 1000 sessions; session 1 has user records only, so a query for its
			// assistant records matches nothing.
			`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<1000) INSERT INTO sessions(session_ref,session_id,first_time_ns) SELECT i,'sess-'||i,0 FROM n`,
			`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<100000)
			 INSERT INTO records(id,file_id,gen,off,item,len,source,type,parser_version,time_ns,session_ref)
			 SELECT i,1,0,i,0,1,'claude-transcript',CASE WHEN i%2=0 OR i%1000=1 THEN 'user' ELSE 'assistant' END,1,i,
			        CASE WHEN i%1000=1 THEN 1 ELSE i%999+2 END FROM n`,
			`INSERT INTO fields(rec_id,name_id,s) SELECT id,1,'m' FROM records WHERE type='assistant'`,
		} {
			if _, err := tx.Exec(q); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	srcs := sources.All()
	cases := []string{
		`{"all":[{"field":"session_id","eq":"sess-1"},{"field":"agent_id","exists":false},{"field":"type","in":["assistant"]}]}`,
		`{"all":[{"field":"type","eq":"assistant"},{"field":"session_id","eq":"sess-1"}]}`,
		`{"all":[{"field":"source","eq":"claude-transcript"},{"field":"session_id","in":["sess-1","sess-2"]},{"field":"type","eq":"assistant"}]}`,
	}
	for _, f := range cases {
		var flt Filter
		if e := Decode([]byte(f), &flt); e != nil {
			t.Fatal(e)
		}
		b := newBuilder(sources.Catalog(srcs))
		b.sessionRef = true
		b.idWalk = pinsSession(&flt) // as Engine.rows does
		c, werr := b.col("model")
		if werr != nil {
			t.Fatal(werr)
		}
		cond, wargs, werr := b.where(&flt)
		if werr != nil {
			t.Fatal(werr)
		}
		q := "SELECT r.id," + c.expr + base(false) + " " + strings.Join(b.joins, " ") + " WHERE " + cond + " ORDER BY r.id LIMIT ?"
		args := append(append(append([]any{}, b.joinArgs...), wargs...), 2)
		rows, err := st.Read().Query("EXPLAIN QUERY PLAN "+q, args...)
		if err != nil {
			t.Fatal(err)
		}
		var plan []string
		for rows.Next() {
			var id, parent, unused int
			var d string
			if err := rows.Scan(&id, &parent, &unused, &d); err != nil {
				t.Fatal(err)
			}
			plan = append(plan, d)
		}
		_ = rows.Close()
		p := strings.Join(plan, "\n")
		if !strings.Contains(p, "SEARCH r USING INDEX records_session_ref_source_time") {
			t.Errorf("%s: records is not searched through the session_ref index:\n%s", f, p)
		}
		start := time.Now()
		rs, err := st.Read().Query(q, args...)
		if err != nil {
			t.Fatal(err)
		}
		got := 0
		for rs.Next() {
			got++
		}
		_ = rs.Close()
		if d := time.Since(start); d > time.Second {
			t.Errorf("%s: took %v over %d records", f, d, n)
		}
	}
}

func TestPinsSession(t *testing.T) {
	for f, want := range map[string]bool{
		`{"field":"session_id","eq":"s"}`:                                        true,
		`{"field":"session","in":["s"]}`:                                         true,
		`{"all":[{"field":"type","eq":"user"},{"field":"session_id","eq":"s"}]}`: true,
		`{"field":"type","eq":"user"}`:                                           false,
		`{"any":[{"field":"session_id","eq":"s"},{"field":"type","eq":"u"}]}`:    false,
		`{"not":{"field":"session_id","eq":"s"}}`:                                false,
	} {
		var flt Filter
		if e := Decode([]byte(f), &flt); e != nil {
			t.Fatal(e)
		}
		if got := pinsSession(&flt); got != want {
			t.Errorf("%s: %v want %v", f, got, want)
		}
	}
	if pinsSession(nil) {
		t.Error("nil filter pins a session")
	}
}
