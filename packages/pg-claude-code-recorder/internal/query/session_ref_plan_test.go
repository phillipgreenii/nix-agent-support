package query

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
)

// The store has no sqlite_stat1, so a session filter over session_ref must
// resolve the id to its reference once and then search records through the
// session_ref index; it must not walk the table or read every row's session id.
func TestSessionFilterPlanSearchesTheSessionRefIndex(t *testing.T) {
	st, err := store.Open(t.TempDir(), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	err = st.WithWrite("", func(tx *sql.Tx) error {
		for _, q := range []string{
			`INSERT INTO files(id,source,mode,first_seen_ns,cur_gen) VALUES(1,'claude-transcript','append',1,0)`,
			`INSERT INTO sessions(session_id,first_time_ns) VALUES('s',0)`,
			`INSERT INTO records(id,file_id,gen,off,item,len,source,type,parser_version,session,time_ns,session_ref) VALUES(1,1,0,0,0,1,'claude-transcript','user',1,'s',1,1)`,
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
	for _, f := range []string{`{"field":"session_id","eq":"s"}`, `{"field":"session_id","in":["s","t"]}`} {
		var flt Filter
		if e := Decode([]byte(f), &flt); e != nil {
			t.Fatal(e)
		}
		b := newBuilder(nil)
		b.sessionRef = true
		cond, args, werr := b.where(&flt)
		if werr != nil {
			t.Fatal(werr)
		}
		q := `EXPLAIN QUERY PLAN SELECT r.id FROM records r JOIN files fl ON fl.id=r.file_id AND r.gen=fl.cur_gen WHERE ` + cond
		rows, err := st.Read().Query(q, args...)
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
		if !strings.Contains(p, "SEARCH r USING INDEX records_session_ref_source_time") && !strings.Contains(p, "SEARCH r USING COVERING INDEX records_session_ref_source_time") {
			t.Errorf("%s: records is not searched through the session_ref index:\n%s", f, p)
		}
		if strings.Contains(p, "SCAN r") {
			t.Errorf("%s: records is scanned:\n%s", f, p)
		}
	}
}
