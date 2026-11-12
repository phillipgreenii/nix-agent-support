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

// An unfiltered row query (ordered by id, small limit) must walk records by id
// and look each file up by primary key. Once sqlite_stat1 exists the planner
// otherwise scans files, probes records_ident per file and sorts every record
// in a temp b-tree before the limit applies: on the live store (tens of
// millions of records) that read took about 22 s, past the query deadline.
func TestUnfilteredRowQueryWalksRecordsByIDWithStatistics(t *testing.T) {
	st, err := store.Open(t.TempDir(), store.Options{ExtensionTables: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	err = st.WithWrite("", func(tx *sql.Tx) error {
		for _, q := range []string{
			`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<3000)
			 INSERT INTO files(id,source,mode,first_seen_ns,cur_gen) SELECT i,'claude-transcript','append',1,0 FROM n`,
			`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<30000)
			 INSERT INTO records(id,file_id,gen,off,item,len,source,type,parser_version,time_ns,session_ref)
			 SELECT i,i%3000+1,0,i,-1,1,'claude-transcript',CASE WHEN i%2=0 THEN 'user' ELSE 'assistant' END,1,i,i%400+1 FROM n`,
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
	if _, err := st.CollectStats(context.Background(), 400, time.Now()); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := st.Read().QueryRowContext(context.Background(), `SELECT COUNT(*) FROM sqlite_stat1`).Scan(&n); err != nil || n == 0 {
		t.Fatalf("store carries no sqlite_stat1 (%d rows, err %v)", n, err)
	}

	q := "SELECT r.id" + baseJoin(false, rowsJoin("1", nil)) + " WHERE 1 ORDER BY r.id LIMIT ?"
	p := plan(t, st, q, []any{2})
	if !strings.HasPrefix(p, "SCAN r") || strings.Contains(p, "SCAN fl") || strings.Contains(p, "TEMP B-TREE") {
		t.Errorf("unfiltered query does not walk records by id:\n%s", p)
	}

	srcs := sources.All()
	e := &Engine{DB: st.Read(), Catalog: sources.Catalog(srcs), Storage: sources.Storage(srcs)}
	ans, werr := e.Query(context.Background(), Request{Select: []string{"type"}, Limit: 1})
	if werr != nil {
		t.Fatal(werr)
	}
	if len(ans.Rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(ans.Rows))
	}
}
