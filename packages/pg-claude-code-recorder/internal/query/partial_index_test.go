package query

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/source"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
)

// partialStore opens a throwaway store and fills it with records whose "tokens"
// field is a number on every third record, text only (n NULL) on the next, and
// absent on the rest. With full set, fields_name_n is replaced by a full index
// to serve as the baseline for result comparisons.
func partialStore(t *testing.T, full bool) *store.Store {
	t.Helper()
	st, err := store.Open(t.TempDir(), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	err = st.WithWrite("", func(tx *sql.Tx) error {
		if full {
			for _, q := range []string{`DROP INDEX fields_name_n`, `CREATE INDEX fields_name_n ON fields(name_id, n)`} {
				if _, err := tx.Exec(q); err != nil {
					return err
				}
			}
		}
		for _, q := range []string{
			`INSERT INTO files(id,source,mode,first_seen_ns,cur_gen) VALUES(1,'claude-transcript','append',1,0)`,
			`INSERT INTO field_names(id,name) VALUES(1,'tokens'),(2,'label')`,
		} {
			if _, err := tx.Exec(q); err != nil {
				return err
			}
		}
		for i := 1; i <= 3000; i++ {
			if _, err := tx.Exec(`INSERT INTO records(id,file_id,gen,off,item,len,source,type,parser_version,time_ns) VALUES(?,1,0,?,0,1,'claude-transcript','assistant',1,?)`, i, i, i); err != nil {
				return err
			}
			if _, err := tx.Exec(`INSERT INTO fields(rec_id,name_id,s) VALUES(?,2,?)`, i, fmt.Sprint("l", i%7)); err != nil {
				return err
			}
			var err error
			switch i % 3 {
			case 0:
				_, err = tx.Exec(`INSERT INTO fields(rec_id,name_id,n) VALUES(?,1,?)`, i, i%100)
			case 1:
				_, err = tx.Exec(`INSERT INTO fields(rec_id,name_id,s) VALUES(?,1,'none')`, i)
			}
			if err != nil {
				return err
			}
		}
		_, err := tx.Exec(`ANALYZE`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return st
}

var partialCat = map[string]source.Kind{"tokens": source.KindInt, "label": source.KindKeyword}

// sqlFor builds the SELECT the query engine would run for a filter and an
// optional descending sort field, through the real builder.
func sqlFor(t *testing.T, where string, sortBy string) (string, []any) {
	t.Helper()
	b := newBuilder(partialCat)
	var f Filter
	if err := json.Unmarshal([]byte(where), &f); err != nil {
		t.Fatal(err)
	}
	w, wargs, werr := b.where(&f)
	if werr != nil {
		t.Fatal(werr)
	}
	ord := "r.id"
	if sortBy != "" {
		e, _, err := b.expr(sortBy)
		if err != nil {
			t.Fatal(err)
		}
		ord = e + " DESC,r.id"
	}
	q := "SELECT r.id" + base(false) + " " + strings.Join(b.joins, " ") + " WHERE " + w + " ORDER BY " + ord
	return q, append(append([]any{}, b.joinArgs...), wargs...)
}

func plan(t *testing.T, st *store.Store, q string, args []any) string {
	t.Helper()
	rs, err := st.Read().Query("EXPLAIN QUERY PLAN "+q, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rs.Close()
	var lines []string
	for rs.Next() {
		var id, parent, unused int
		var detail string
		if err := rs.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		lines = append(lines, detail)
	}
	return strings.Join(lines, "\n")
}

func ids(t *testing.T, st *store.Store, q string, args []any) []int64 {
	t.Helper()
	rs, err := st.Read().Query(q, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rs.Close()
	var out []int64
	for rs.Next() {
		var id int64
		if err := rs.Scan(&id); err != nil {
			t.Fatal(err)
		}
		out = append(out, id)
	}
	return out
}

var numericFilters = map[string]string{
	"eq":    `{"field":"tokens","eq":42}`,
	"in":    `{"field":"tokens","in":[3,42,99]}`,
	"range": `{"field":"tokens","range":{"gte":10,"lt":30}}`,
	"gt":    `{"field":"tokens","range":{"gt":90}}`,
}

func TestNumericFilterUsesPartialIndex(t *testing.T) {
	st := partialStore(t, false)
	// The index holds only the 1000 rows with a number, not the 2000 rows that
	// have a tokens row: a full index would make this count 2000.
	var held int
	if err := st.Read().QueryRow(`SELECT COUNT(*) FROM fields INDEXED BY fields_name_n WHERE name_id=1 AND n IS NOT NULL`).Scan(&held); err != nil {
		t.Fatal(err)
	}
	var def string
	if err := st.Read().QueryRow(`SELECT sql FROM sqlite_master WHERE name='fields_name_n'`).Scan(&def); err != nil || !strings.Contains(def, "WHERE n IS NOT NULL") {
		t.Fatalf("fields_name_n is not partial on n IS NOT NULL: %q, %v", def, err)
	}
	if held != 1000 {
		t.Fatalf("index holds %d numeric rows, want 1000", held)
	}
	for name, where := range numericFilters {
		q, args := sqlFor(t, where, "")
		if p := plan(t, st, q, args); !strings.Contains(p, "fields_name_n") {
			t.Errorf("%s: plan does not use fields_name_n:\n%s\nSQL: %s", name, p, q)
		}
	}
}

func TestPartialIndexResultsMatchFullIndexBaseline(t *testing.T) {
	part, full := partialStore(t, false), partialStore(t, true)
	cases := map[string][2]string{}
	for name, where := range numericFilters {
		cases[name] = [2]string{where, ""}
		cases[name+" sorted"] = [2]string{where, "tokens"}
	}
	cases["sort only"] = [2]string{`{"field":"label","eq":"l3"}`, "tokens"}
	cases["exists"] = [2]string{`{"field":"tokens","exists":true}`, ""}
	cases["absent"] = [2]string{`{"field":"tokens","exists":false}`, ""}
	for name, c := range cases {
		q, args := sqlFor(t, c[0], c[1])
		want, got := ids(t, full, q, args), ids(t, part, q, args)
		if len(want) == 0 {
			t.Errorf("%s: baseline is empty, the case proves nothing", name)
		}
		if !reflect.DeepEqual(want, got) {
			t.Errorf("%s: partial index gave %v, full index %v", name, got, want)
		}
	}
}

func TestFieldWithNullNumberStillAnswersCorrectly(t *testing.T) {
	st := partialStore(t, false)
	// Records with i%3==1 hold tokens as text only (n NULL): the partial index
	// does not hold them, so a query for them cannot use it.
	q := "SELECT r.id" + base(false) + " JOIN fields f ON f.rec_id=r.id AND f.name_id=1 WHERE f.n IS NULL ORDER BY r.id"
	if p := plan(t, st, q, nil); strings.Contains(p, "fields_name_n") {
		t.Fatalf("n IS NULL cannot be answered from a partial index, plan:\n%s", p)
	}
	got := ids(t, st, q, nil)
	if len(got) != 1000 || got[0] != 1 || got[1] != 4 {
		t.Fatalf("n IS NULL rows: %d, first %v", len(got), got[:2])
	}
	// The existence filter, which reads the row and not the index, still counts
	// both the numeric and the text-only rows.
	eq, args := sqlFor(t, `{"field":"tokens","exists":true}`, "")
	if n := len(ids(t, st, eq, args)); n != 2000 {
		t.Fatalf("exists tokens: %d rows, want 2000", n)
	}
}
