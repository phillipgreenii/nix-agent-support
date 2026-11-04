package settle

import (
	"database/sql"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
)

// sessionRefFixture loads a marked store with two sessions: one named by a
// registry file that is still present, one named only by a registry version
// that is no longer the current generation, and a transcript file both write.
func sessionRefFixture(t *testing.T, dir string) *store.Store {
	t.Helper()
	st, err := store.Open(dir, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	err = st.WithWrite("", func(tx *sql.Tx) error {
		stmts := []string{
			`INSERT INTO files(id,source,mode,first_seen_ns,cur_gen) VALUES(1,'session-registry','replace',1,1),(2,'claude-transcript','append',1,0),(3,'session-registry','replace',1,1)`,
			`INSERT INTO paths(file_id,path,first_seen_ns,closed_ns) VALUES(1,'sessions/1.json',1,NULL),(3,'sessions/3.json',1,5)`,
			`INSERT INTO snapshots(file_id,version,hash,size,seen_ns) VALUES(1,1,0,111,1),(1,0,0,99,1),(3,0,0,77,1)`,
			`INSERT INTO sessions(session_id,first_time_ns) VALUES('example-a',0),('example-b',0),('',0)`,
			`INSERT INTO field_names(name) VALUES('version')`,
			// registry file 1 names example-a (gens 0 and 1); registry file 3 names example-b at gen 0 only
			`INSERT INTO records(id,file_id,gen,off,item,len,source,type,parser_version,session,time_ns,session_ref) VALUES
(1,1,0,0,0,1,'session-registry','registry',3,'example-a',1,(SELECT session_ref FROM sessions WHERE session_id='example-a')),
(2,1,1,0,0,1,'session-registry','registry',3,'example-a',2,(SELECT session_ref FROM sessions WHERE session_id='example-a')),
(3,3,0,0,0,1,'session-registry','registry',4,'example-b',3,(SELECT session_ref FROM sessions WHERE session_id='example-b')),
(4,2,0,0,0,1,'claude-transcript','user',7,'example-a',4,(SELECT session_ref FROM sessions WHERE session_id='example-a')),
(5,2,0,10,0,1,'claude-transcript','user',9,'example-b',5,(SELECT session_ref FROM sessions WHERE session_id='example-b')),
(6,2,0,20,0,1,'claude-transcript','user',9,'',6,(SELECT session_ref FROM sessions WHERE session_id=''))`,
			`INSERT INTO fields(rec_id,name_id,s) VALUES(1,(SELECT id FROM field_names WHERE name='version'),'1.0.1'),(2,(SELECT id FROM field_names WHERE name='version'),'1.0.2'),(3,(SELECT id FROM field_names WHERE name='version'),'0.9.0')`,
		}
		for _, q := range stmts {
			if _, err := tx.Exec(q); err != nil {
				t.Fatalf("%s: %v", q, err)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return st
}

type sessionReads struct {
	Versions map[string][]store.ManifestRegistry
	Names    map[string]bool
	Info     map[string]WhereInfo
	OfFile   map[int64][]string
}

func readSessionFacts(t *testing.T, st *store.Store) sessionReads {
	t.Helper()
	s := &Settler{st: st}
	out := sessionReads{Versions: map[string][]store.ManifestRegistry{}, Names: map[string]bool{}, Info: map[string]WhereInfo{}, OfFile: map[int64][]string{}}
	for _, id := range []string{"example-a", "example-b", "absent"} {
		v, err := s.registryVersions(id)
		if err != nil {
			t.Fatal(err)
		}
		out.Versions[id] = v
		out.Names[id] = s.registryNames(id)
		var info WhereInfo
		s.fillVersions(&info, id)
		out.Info[id] = info
	}
	for _, f := range []int64{1, 2, 3, 99} {
		ids, err := st.SessionsOfFile(f)
		if err != nil {
			t.Fatal(err)
		}
		out.OfFile[f] = ids
	}
	return out
}

// On a real store the settle reads of a session answer the same through
// session_ref and sessions as from records.session, which is what a store
// without the mark still reads.
func TestSettleReadsThroughSessionRefEqualTheTextSessionPath(t *testing.T) {
	dir := t.TempDir()
	st := sessionRefFixture(t, dir)
	if !st.SessionRefReady() {
		t.Fatal("the fixture store is not marked ready")
	}
	want := readSessionFacts(t, st)
	// Nonempty answers, so equality is not two empty results.
	if len(want.Versions["example-a"]) != 2 || !want.Names["example-a"] || want.Names["example-b"] ||
		want.Info["example-a"].ClaudeCodeVersion != "1.0.2" || want.Info["example-b"].ParserVersion != 9 ||
		!reflect.DeepEqual(want.OfFile[2], []string{"example-a", "example-b"}) {
		t.Fatalf("the fixture shows nothing to compare: %+v", want)
	}
	if len(want.Versions["absent"]) != 0 || want.Names["absent"] || want.Info["absent"].ParserVersion != 0 {
		t.Fatalf("an unknown session matched: %+v", want)
	}

	// The same data as a store that predates session_ref: no mark, session_ref 0,
	// no session rows.
	for _, q := range []string{`UPDATE records SET session_ref=0`, `DELETE FROM session_files`, `DELETE FROM session_stats`, `DELETE FROM sessions`, `DELETE FROM meta WHERE key='session_ref_complete'`, `DROP INDEX records_session_ref_source_time`} {
		if err := st.WithWrite("", func(tx *sql.Tx) error { _, e := tx.Exec(q); return e }); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	old, err := store.Open(dir, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = old.Close() })
	if old.SessionRefReady() {
		t.Fatal("the older store is marked ready")
	}
	if got := readSessionFacts(t, old); !reflect.DeepEqual(got, want) {
		t.Fatalf("text path differs from the session_ref path\n text: %+v\n  ref: %+v", got, want)
	}
}

// The session_ref forms of the settle reads must resolve the id once and search
// records through the session_ref index; they must not walk records or fields.
func TestSettleSessionRefQueryPlansSearchTheSessionRefIndex(t *testing.T) {
	st := sessionRefFixture(t, t.TempDir())
	t.Cleanup(func() { _ = st.Close() })
	scan := regexp.MustCompile(`^SCAN (r|f|records|fields)\b`)
	for name, q := range map[string]string{
		"parserVersion":    parserVersionQueryRef,
		"claudeVersion":    claudeVersionQueryRef,
		"registryVersions": registryVersionsQueryRef,
		"registryNames":    registryNamesQueryRef,
	} {
		t.Run(name, func(t *testing.T) {
			rows, err := st.Read().Query("EXPLAIN QUERY PLAN "+q, "example-a")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = rows.Close() }()
			var plan []string
			for rows.Next() {
				var id, parent, unused int
				var d string
				if err := rows.Scan(&id, &parent, &unused, &d); err != nil {
					t.Fatal(err)
				}
				plan = append(plan, d)
				if scan.MatchString(d) {
					t.Errorf("plan scans: %q", d)
				}
			}
			if p := strings.Join(plan, "\n"); !strings.Contains(p, "records_session_ref_source_time") {
				t.Errorf("records is not searched through the session_ref index:\n%s", p)
			}
		})
	}
}
