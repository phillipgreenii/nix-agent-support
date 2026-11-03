package lifecycle

import (
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"

	_ "modernc.org/sqlite" // the database driver
)

// oldCwdQuery is the query transcriptFacts used before cwdQuery: kept as the
// equivalence oracle and as the plan the guard proves has changed.
const oldCwdQuery = `SELECT f.s FROM records r JOIN fields f ON f.rec_id=r.id AND f.name_id=(SELECT id FROM field_names WHERE name='cwd')
WHERE r.session=? AND r.source='claude-transcript' ORDER BY r.time_ns DESC, r.id DESC LIMIT 1`

const transcript = "claude-transcript"

type cwdFixture struct {
	t  *testing.T
	db *sql.DB
	id int64
}

// newCwdFixture opens a real store (which creates the schema) and then writes
// rows into it directly, so no parser is needed.
func newCwdFixture(t *testing.T) *cwdFixture {
	t.Helper()
	st, err := store.Open(t.TempDir(), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	db, err := sql.Open("sqlite", filepath.Join(st.Dir(), store.DBName))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return &cwdFixture{t: t, db: db}
}

// rec inserts a record and the given (field name, value) pairs; a nil value
// stores a NULL string.
func (f *cwdFixture) rec(session, source string, timeNS int64, fields ...[2]any) {
	f.t.Helper()
	f.id++
	if _, err := f.db.Exec(`INSERT INTO records(id,file_id,gen,off,item,len,source,type,parser_version,session,time_ns)
VALUES(?,1,1,?,0,1,?,'x',1,?,?)`, f.id, f.id, source, session, timeNS); err != nil {
		f.t.Fatal(err)
	}
	for _, fv := range fields {
		if _, err := f.db.Exec(`INSERT OR IGNORE INTO field_names(name) VALUES(?)`, fv[0]); err != nil {
			f.t.Fatal(err)
		}
		if _, err := f.db.Exec(`INSERT INTO fields(rec_id,name_id,s) VALUES(?,(SELECT id FROM field_names WHERE name=?),?)`,
			f.id, fv[0], fv[1]); err != nil {
			f.t.Fatal(err)
		}
	}
}

func (f *cwdFixture) run(q, session string) string {
	f.t.Helper()
	var s sql.NullString
	err := f.db.QueryRow(q, session).Scan(&s)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return "<no row>"
	case err != nil:
		f.t.Fatal(err)
	case !s.Valid:
		return "<NULL>"
	}
	return "=" + s.String
}

func cwd(v any) [2]any { return [2]any{"cwd", v} }

func TestCwdQueryEquivalentToTheOldQuery(t *testing.T) {
	cases := []struct {
		name  string
		build func(f *cwdFixture)
	}{
		{"newest record has no cwd", func(f *cwdFixture) {
			f.rec("s", transcript, 10, cwd("/old"))
			f.rec("s", transcript, 20, [2]any{"other", "x"})
		}},
		{"tie on time_ns broken by id", func(f *cwdFixture) {
			f.rec("s", transcript, 10, cwd("/first"))
			f.rec("s", transcript, 10, cwd("/second"))
		}},
		{"another session is newer", func(f *cwdFixture) {
			f.rec("s", transcript, 10, cwd("/mine"))
			f.rec("other", transcript, 99, cwd("/theirs"))
		}},
		{"newest cwd is empty", func(f *cwdFixture) {
			f.rec("s", transcript, 10, cwd("/old"))
			f.rec("s", transcript, 20, cwd(""))
		}},
		{"newest cwd is NULL", func(f *cwdFixture) {
			f.rec("s", transcript, 10, cwd("/old"))
			f.rec("s", transcript, 20, cwd(nil))
		}},
		{"non-transcript record is newer", func(f *cwdFixture) {
			f.rec("s", transcript, 10, cwd("/mine"))
			f.rec("s", "session-registry", 99, cwd("/registry"))
		}},
		{"session without transcript records", func(f *cwdFixture) {
			f.rec("s", "session-registry", 10, cwd("/registry"))
			f.rec("other", transcript, 10, cwd("/theirs"))
		}},
		{"several records of one session out of time order", func(f *cwdFixture) {
			f.rec("s", transcript, 10, cwd("/a"))
			f.rec("s", transcript, 30, cwd("/b"))
			f.rec("s", transcript, 20, cwd("/c"))
		}},
		{"several fields on one record", func(f *cwdFixture) {
			f.rec("s", transcript, 10, cwd("/a"), [2]any{"cwd2", "/z"}, [2]any{"aaa", "/y"})
			f.rec("s", transcript, 10, [2]any{"zzz", "/q"}, cwd("/b"))
		}},
		{"empty store", func(f *cwdFixture) {}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newCwdFixture(t)
			c.build(f)
			want, got := f.run(oldCwdQuery, "s"), f.run(cwdQuery, "s")
			if got != want {
				t.Errorf("cwdQuery returned %s, old query %s", got, want)
			}
		})
	}
}

func TestCwdQueryPlanStartsFromTheSessionIndex(t *testing.T) {
	f := newCwdFixture(t)
	f.rec("s", transcript, 10, cwd("/a"))
	var ver string
	if err := f.db.QueryRow(`SELECT sqlite_version()`).Scan(&ver); err != nil {
		t.Fatal(err)
	}
	t.Logf("sqlite_version() = %s", ver)

	plan := func(q string) []string {
		rows, err := f.db.Query(`EXPLAIN QUERY PLAN `+q, "s")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = rows.Close() }()
		var out []string
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				t.Fatal(err)
			}
			out = append(out, detail)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return out
	}

	oldPlan := strings.Join(plan(oldCwdQuery), "\n")
	// On the interned-name schema (fields keyed by rec_id, name_id) the plain
	// JOIN already starts from records_session in this SQLite; on the earlier
	// schema it started from every cwd field. CROSS JOIN pins the order so a
	// planner or schema change cannot bring the scan back; this log records
	// whether the old text still shows the problem.
	if strings.Contains(oldPlan, "fields_name_n") {
		t.Logf("old query plan still starts from fields_name_n:\n%s", oldPlan)
	} else {
		t.Logf("old query plan already avoids fields_name_n (the guard pins the order, it does not prove a regression):\n%s", oldPlan)
	}

	newPlan := plan(cwdQuery)
	search := regexp.MustCompile(`SEARCH.*USING INDEX records_session`)
	scan := regexp.MustCompile(`SCAN`)
	var sawSearch bool
	for _, d := range newPlan {
		if search.MatchString(d) {
			sawSearch = true
		}
		if scan.MatchString(d) {
			t.Errorf("plan row scans: %q", d)
		}
		if strings.Contains(d, "fields_name_n") {
			t.Errorf("plan row uses fields_name_n: %q", d)
		}
	}
	if !sawSearch {
		t.Errorf("no plan row matches %v; plan: %s", search, fmt.Sprint(newPlan))
	}
}
