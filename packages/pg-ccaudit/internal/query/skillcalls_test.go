package query

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/phillipgreenii/pg-ccaudit/internal/store"
)

// skillCall is one synthetic Skill tool call: the session it belongs to, its
// timestamp, and the skill name carried in the call's input.
type skillCall struct {
	session string
	ts      string
	skill   string
}

// buildSkillDB writes sessions "s1" (first cwd /w/a) and "s2" (first cwd /w/b),
// each with Skill calls, plus a non-Skill call that must never be counted.
func buildSkillDB(t *testing.T, calls []skillCall) *sql.DB {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "skill.db")
	w, err := store.Open(dbPath, false)
	if err != nil {
		t.Fatal(err)
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := w.Exec(q, args...); err != nil {
			t.Fatalf("exec %q: %v", q, err)
		}
	}
	cwds := map[string]string{"s1": "/w/a/proj", "s2": "/w/b/proj"}
	for s, cwd := range cwds {
		path := "/t/" + s + ".jsonl"
		exec(`INSERT INTO files (path, project_dir, size, mtime, lines_ok, lines_bad, ingested_at) VALUES (?, '', 0, 0, 1, 0, 0)`, path)
		// seq 1: a Bash call carrying the session's first cwd.
		exec(`INSERT INTO events (path, seq, session_id, ts, type, is_sidechain, cwd) VALUES (?, 1, ?, '2026-07-22T09:00:00.000Z', 'assistant', 0, ?)`, path, s, cwd)
		exec(`INSERT INTO tool_calls (tool_use_id, path, seq, tool_name, input_json, lead_cmd) VALUES (?, ?, 1, 'Bash', '{"skill":"not-a-skill"}', 'x')`, s+"-bash", path)
	}
	for i, c := range calls {
		path := "/t/" + c.session + ".jsonl"
		seq := i + 2
		exec(`INSERT INTO events (path, seq, session_id, ts, type, is_sidechain, cwd) VALUES (?, ?, ?, ?, 'assistant', 0, ?)`, path, seq, c.session, c.ts, cwds[c.session])
		exec(`INSERT INTO tool_calls (tool_use_id, path, seq, tool_name, input_json, lead_cmd) VALUES (?, ?, ?, 'Skill', ?, '')`,
			fmt.Sprintf("call-%d", i), path, seq, fmt.Sprintf(`{"skill":%q}`, c.skill))
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := store.OpenReadOnly(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func skillCounts(t *testing.T, db *sql.DB, req Request) map[string]int64 {
	t.Helper()
	q, err := Lookup("skill-calls")
	if err != nil {
		t.Fatal(err)
	}
	req.Query = q
	res, err := Run(context.Background(), db, req)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Columns) < 2 || res.Columns[0] != "skill" || res.Columns[1] != "calls" {
		t.Fatalf("columns = %v, want skill, calls first", res.Columns)
	}
	got := map[string]int64{}
	for _, r := range res.Rows {
		got[r[0].(string)] = r[1].(int64)
	}
	return got
}

func TestSkillCallsCountsWindowAndScope(t *testing.T) {
	db := buildSkillDB(t, []skillCall{
		{"s1", "2026-07-22T10:00:00.000Z", "alpha"},
		{"s1", "2026-07-22T11:00:00.000Z", "alpha"},
		{"s1", "2026-07-23T10:00:00.000Z", "beta"},
		{"s2", "2026-07-22T12:00:00.000Z", "alpha"},
		{"s2", "2026-07-24T00:00:00.000Z", "beta"},
	})
	cases := []struct {
		name string
		req  Request
		want map[string]int64
	}{
		{"unfiltered", Request{}, map[string]int64{"alpha": 3, "beta": 2}},
		{"since inclusive", Request{Since: "2026-07-22T11:00:00.000Z"}, map[string]int64{"alpha": 2, "beta": 2}},
		{"until exclusive", Request{Until: "2026-07-24T00:00:00.000Z"}, map[string]int64{"alpha": 3, "beta": 1}},
		{"window both", Request{Since: "2026-07-23", Until: "2026-07-24"}, map[string]int64{"beta": 1}},
		{"cwd-prefix keeps one session", Request{CwdPrefixes: []string{"/w/a"}}, map[string]int64{"alpha": 2, "beta": 1}},
		{"two cwd-prefixes OR", Request{CwdPrefixes: []string{"/w/a", "/w/b"}}, map[string]int64{"alpha": 3, "beta": 2}},
		{"cwd-prefix excludes outside", Request{CwdPrefixes: []string{"/w/zzz"}}, map[string]int64{}},
		{"exclude-cwd-prefix", Request{ExcludeCwdPrefixes: []string{"/w/a"}}, map[string]int64{"alpha": 1, "beta": 1}},
		{"prefix and window", Request{CwdPrefixes: []string{"/w/b"}, Until: "2026-07-23"}, map[string]int64{"alpha": 1}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := skillCounts(t, db, c.req); !reflect.DeepEqual(got, c.want) {
				t.Errorf("got %v, want %v", got, c.want)
			}
		})
	}
}
