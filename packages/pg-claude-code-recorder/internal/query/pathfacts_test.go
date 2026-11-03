package query_test

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/query"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/source"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/sources"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/testkit"
)

const historyLine = `{"display":"fix the example bug","timestamp":1761768000000,"project":"/example/work","sessionId":"example-session-1"}` + "\n"

// fieldCount is how many fields rows the store holds under one field name.
func fieldCount(t *testing.T, k *testkit.Kit, name string) int {
	t.Helper()
	return k.Count(`SELECT COUNT(*) FROM fields f JOIN field_names n ON n.id=f.name_id WHERE n.name=?`, name)
}

// legacyRows writes, for every record, the fields rows the parser stored before
// project, file_session, type and session_id were dropped: the path captures of
// the record's source (re-derived with the source's real Match) and the type
// and session columns. It is the baseline the path-served answers are compared
// with.
func legacyRows(t *testing.T, k *testkit.Kit) {
	t.Helper()
	bySrc := map[string]source.Source{}
	for _, s := range sources.All() {
		bySrc[s.Name()] = s
	}
	type rec struct {
		id            int64
		src, path     string
		typ, sessions string
	}
	var recs []rec
	rs, err := k.Store.Read().Query(`SELECT r.id, r.source, p.path, r.type, r.session FROM records r JOIN paths p ON p.file_id=r.file_id AND p.closed_ns IS NULL`)
	if err != nil {
		t.Fatal(err)
	}
	for rs.Next() {
		var r rec
		if err := rs.Scan(&r.id, &r.src, &r.path, &r.typ, &r.sessions); err != nil {
			t.Fatal(err)
		}
		recs = append(recs, r)
	}
	_ = rs.Close()
	err = k.Store.WithWrite("", func(tx *sql.Tx) error {
		put := func(id int64, name, s string) error {
			if s == "" {
				return nil
			}
			if _, err := tx.Exec(`INSERT INTO field_names(name) VALUES(?) ON CONFLICT(name) DO NOTHING`, name); err != nil {
				return err
			}
			_, err := tx.Exec(`INSERT INTO fields(rec_id,name_id,s) VALUES(?,(SELECT id FROM field_names WHERE name=?),?)
ON CONFLICT(rec_id,name_id) DO NOTHING`, id, name, s)
			return err
		}
		for _, r := range recs {
			rel := strings.TrimPrefix(r.path, testkit.ClaudeDir+"/")
			if caps, ok := bySrc[r.src].Match(rel); ok {
				if err := put(r.id, "project", caps["project"]); err != nil {
					return err
				}
				if err := put(r.id, "file_session", caps["session"]); err != nil {
					return err
				}
			}
			if err := put(r.id, "type", r.typ); err != nil {
				return err
			}
			if err := put(r.id, "session_id", r.sessions); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// pathFactRequests are the requests that used to read project and file_session
// from stored fields rows: every operator, a select, a sort, a group and an
// existence test.
var pathFactRequests = []string{
	`{"where":{"field":"project","eq":"example-p1"},"select":["uuid","project","file_session"]}`,
	`{"where":{"field":"project","in":["example-p1","example-p2"]},"select":["project"],"order":[{"by":"project","dir":"desc"},{"by":"file_session"}]}`,
	`{"where":{"field":"project","contains":"p2"},"select":["project","file_session"]}`,
	`{"where":{"field":"file_session","eq":"orig"},"select":["uuid","file_session"]}`,
	`{"where":{"field":"file_session","in":["orig","sess9"]},"select":["file_session","project"]}`,
	`{"where":{"field":"file_session","contains":"es"},"select":["file_session"]}`,
	`{"where":{"field":"project","exists":true},"select":["source","project"],"order":[{"by":"source"}]}`,
	`{"where":{"field":"project","exists":false},"select":["source","type"],"order":[{"by":"source"}]}`,
	`{"where":{"field":"file_session","exists":false},"select":["source","type"],"order":[{"by":"source"}]}`,
	`{"where":{"not":{"field":"project","eq":"example-p1"}},"select":["source","project"]}`,
	`{"group":["project","file_session"],"aggregate":[{"fn":"count","as":"n"}],"order":[{"by":"project"},{"by":"file_session"}]}`,
	`{"group":["source","project"],"aggregate":[{"fn":"count","field":"file_session","distinct":"file_session","as":"n"}],"order":[{"by":"source"},{"by":"project"}]}`,
	`{"where":{"field":"type","eq":"assistant"},"select":["type","session_id","project"]}`,
	`{"where":{"field":"session_id","eq":"S1"},"select":["session_id","file_session"]}`,
	`{"where":{"field":"item","eq":0},"select":["type","project","file_session"],"follow":"parent"}`,
}

// TestPathFactsMatchTheirFormerStoredRows parses a real transcript fixture
// (a main file, a resume, a fork in another project and a subagent file) and
// the history file, then asks each request three ways: with no project,
// file_session, type or session_id rows stored, with the legacy rows added and
// path reading off, and with both. All three must give the same answer.
func TestPathFactsMatchTheirFormerStoredRows(t *testing.T) {
	k := testkit.New(t)
	line := func(uuid, parent, sess string) string {
		p := ""
		if parent != "" {
			p = `"parentUuid":"` + parent + `",`
		}
		return `{"type":"user","uuid":"` + uuid + `",` + p + `"sessionId":"` + sess + `","message":{"role":"user","content":"hello"}}` + "\n"
	}
	k.Add("example-p1", "orig.jsonl", line("u1", "", "S1")+line("u2", "u1", "S1"))
	k.Add("example-p1", "resume.jsonl", line("u1", "", "S2")+line("u3", "u1", "S2"))
	k.Add("example-p2", "fork.jsonl", line("u1", "", "S1"))
	k.AddAgent("example-p1", "sess9", "a1", fixture)
	k.Add("", "history.jsonl", historyLine)
	k.Parse()

	// The parser no longer stores them; the history project is a JSON value and stays.
	for _, name := range []string{"type", "session_id", "file_session"} {
		if n := fieldCount(t, k, name); n != 0 {
			t.Fatalf("%d %q rows stored", n, name)
		}
	}
	if n := fieldCount(t, k, "project"); n != 1 {
		t.Fatalf("project rows %d, want only the history entry's own project", n)
	}

	run := func(e *query.Engine) []string {
		var out []string
		for _, req := range pathFactRequests {
			a := qe(t, e, req)
			out = append(out, js(a.Columns)+js(a.Rows))
		}
		return out
	}
	fromPaths := run(k.Engine)
	for i, req := range pathFactRequests {
		if !strings.Contains(fromPaths[i], "[") {
			t.Fatalf("%s: no answer", req)
		}
	}
	// Non-vacuity: the fixture really answers project and file_session.
	if got := qe(t, k.Engine, pathFactRequests[10]); js(got.Rows) != `[["/example/work",null,1],["example-p1","orig",4],["example-p1","resume",4],["example-p1","sess9",11],["example-p2","fork",2]]` {
		t.Fatalf("project and file_session groups: %s", js(got.Rows))
	}

	legacyRows(t, k)
	if n := fieldCount(t, k, "project"); n < 10 {
		t.Fatalf("legacy baseline not written: %d project rows", n)
	}
	noPaths := *k.Engine
	noPaths.ClaudeDir = ""
	fromStored := run(&noPaths)
	both := run(k.Engine)
	for i, req := range pathFactRequests {
		if fromPaths[i] != fromStored[i] || fromPaths[i] != both[i] {
			t.Errorf("%s\n paths  %s\n stored %s\n both   %s", req, fromPaths[i], fromStored[i], both[i])
		}
	}
}

func qe(t *testing.T, e *query.Engine, req string) *query.Answer {
	t.Helper()
	var r query.Request
	if err := query.Decode([]byte(req), &r); err != nil {
		t.Fatal(err)
	}
	a, err := e.Query(t.Context(), r)
	if err != nil {
		t.Fatalf("%s: %v", req, err)
	}
	return a
}

// TestPathFactsOfEverySourceShape runs each source's own Match over a path of
// every shape that carries a project, and requires the SQL reading to give the
// same project and file_session. It also covers a file whose current path moved
// (the open path wins), a deleted file (its only, closed, path), and a path
// outside the Claude directory (no fact).
func TestPathFactsOfEverySourceShape(t *testing.T) {
	st, err := store.Open(t.TempDir(), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	srcs := sources.All()
	bySrc := map[string]source.Source{}
	for _, s := range srcs {
		bySrc[s.Name()] = s
	}
	cases := []struct {
		src, rel string
	}{
		{"claude-transcript", "projects/proj-a/sess-1.jsonl"},
		{"claude-transcript", "projects/proj-a/sess-1/subagents/agent-a1.jsonl"},
		{"claude-status", "projects/proj-b/sess-2.status.jsonl"},
		{"session-mode", "projects/proj-b/sess-2.session-mode.json"},
		{"session-compaction", "projects/proj-c/sess-3.compaction.json"},
		{"claude-tool-result-body", "projects/proj-c/sess-3/tool-results/dir/toolu_x.txt"},
		{"claude-subagent-meta", "projects/proj-d/sess-4/subagents/agent-a1.meta.json"},
		{"claude-history", "history.jsonl"},
	}
	for _, c := range cases {
		if _, ok := bySrc[c.src]; !ok {
			t.Fatalf("no source named %q", c.src)
		}
	}
	err = st.WithWrite("", func(tx *sql.Tx) error {
		for i, c := range cases {
			id := i + 1
			if _, err := tx.Exec(`INSERT INTO files(id,source,mode,first_seen_ns,cur_gen) VALUES(?,?,'append',1,0)`, id, c.src); err != nil {
				return err
			}
			if _, err := tx.Exec(`INSERT INTO paths(file_id,path,first_seen_ns) VALUES(?,?,1)`, id, filepath.Join("/claude", c.rel)); err != nil {
				return err
			}
			if _, err := tx.Exec(`INSERT INTO records(id,file_id,gen,off,item,len,source,type,parser_version) VALUES(?,?,0,0,-1,1,?,'x',1)`, id, id, c.src); err != nil {
				return err
			}
		}
		n := len(cases)
		// File n+1 moved: an old closed path under another project, and a current one.
		// File n+2 was deleted: only a closed path. File n+3 lives outside the Claude directory.
		for _, q := range []string{
			fmt.Sprintf(`INSERT INTO files(id,source,mode,first_seen_ns,cur_gen) VALUES(%d,'claude-transcript','append',1,0),(%d,'claude-transcript','append',1,0),(%d,'claude-transcript','append',1,0)`, n+1, n+2, n+3),
			fmt.Sprintf(`INSERT INTO paths(file_id,path,first_seen_ns,closed_ns) VALUES(%d,'/claude/projects/old-proj/old.jsonl',1,5)`, n+1),
			fmt.Sprintf(`INSERT INTO paths(file_id,path,first_seen_ns) VALUES(%d,'/claude/projects/new-proj/new.jsonl',6)`, n+1),
			fmt.Sprintf(`INSERT INTO paths(file_id,path,first_seen_ns,closed_ns) VALUES(%d,'/claude/projects/gone-proj/gone.jsonl',1,5)`, n+2),
			fmt.Sprintf(`INSERT INTO paths(file_id,path,first_seen_ns) VALUES(%d,'/elsewhere/projects/out-proj/out.jsonl',1)`, n+3),
		} {
			if _, err := tx.Exec(q); err != nil {
				return err
			}
		}
		for i := n + 1; i <= n+3; i++ {
			if _, err := tx.Exec(`INSERT INTO records(id,file_id,gen,off,item,len,source,type,parser_version) VALUES(?,?,0,0,-1,1,'claude-transcript','x',1)`, i, i); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	e := &query.Engine{DB: st.Read(), Catalog: sources.Catalog(srcs), ClaudeDir: "/claude"}
	a := qe(t, e, `{"select":["project","file_session"],"order":[{"by":"file"}]}`)
	if len(a.Rows) != len(cases)+3 {
		t.Fatalf("rows %s", js(a.Rows))
	}
	for i, c := range cases {
		var wantP, wantS any
		if caps, ok := bySrc[c.src].Match(c.rel); ok {
			wantP, wantS = caps["project"], caps["session"]
			if wantP == "" {
				wantP = nil
			}
			if wantS == "" {
				wantS = nil
			}
		}
		if a.Rows[i][0] != wantP || a.Rows[i][1] != wantS {
			t.Errorf("%s %s: got %v, want [%v %v] from the source's Match", c.src, c.rel, a.Rows[i], wantP, wantS)
		}
	}
	n := len(cases)
	for i, want := range []string{`["new-proj","new"]`, `["gone-proj","gone"]`, `[null,null]`} {
		if got := js(a.Rows[n+i]); got != want {
			t.Errorf("moved/deleted/outside file %d: got %s, want %s", i, got, want)
		}
	}
	// A path-derived value filters like a stored one.
	f := qe(t, e, `{"where":{"field":"project","eq":"proj-b"},"select":["file_session"]}`)
	if js(f.Rows) != `[["sess-2"],["sess-2"]]` {
		t.Fatalf("filter on a path fact: %s", js(f.Rows))
	}
}
