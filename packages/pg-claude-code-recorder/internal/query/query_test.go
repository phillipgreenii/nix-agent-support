package query_test

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/query"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/testkit"
)

// fixture: two assistant messages. m1 is written on two lines (a replayed
// message, same usage), m2 on one. Hand-computed: model a has output tokens
// 10 (distinct m1) over 2 records, model b 5 over 1.
const fixture = `{"type":"user","uuid":"u1","sessionId":"example-session-1","timestamp":"2026-10-29T20:00:00Z","cwd":"/example/work/sub","message":{"role":"user","content":"find the registry file"}}
{"type":"assistant","uuid":"u2","parentUuid":"u1","sessionId":"example-session-1","timestamp":"2026-10-29T20:00:01Z","message":{"id":"m1","model":"example-model-a","role":"assistant","usage":{"output_tokens":10},"content":[{"type":"text","text":"the registry file was deleted"},{"type":"tool_use","id":"tu_1","name":"Bash","input":{"command":"git commit --no-verify -m wip"}}]}}
{"type":"user","uuid":"u3","parentUuid":"u2","sessionId":"example-session-1","timestamp":"2026-10-29T20:00:02Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu_1","content":"ok done","is_error":true}]}}
{"type":"assistant","uuid":"u4","parentUuid":"u3","sessionId":"example-session-1","timestamp":"2026-10-29T20:00:03Z","message":{"id":"m1","model":"example-model-a","role":"assistant","usage":{"output_tokens":10},"content":[{"type":"text","text":"Registry cleanup, deleted: done"}]}}
{"type":"assistant","uuid":"u5","parentUuid":"u4","sessionId":"example-session-1","timestamp":"2026-10-29T20:00:05Z","message":{"id":"m2","model":"example-model-b","role":"assistant","usage":{"output_tokens":5},"content":[{"type":"text","text":"other words about NEAR and \"quotes\" and star*"}]}}
`

func kit(t *testing.T) *testkit.Kit {
	t.Helper()
	k := testkit.New(t)
	k.Add("example-proj", "example-session-1.jsonl", fixture)
	k.Parse()
	return k
}

func q(t *testing.T, k *testkit.Kit, req string) *query.Answer {
	t.Helper()
	var r query.Request
	if err := json.Unmarshal([]byte(req), &r); err != nil {
		t.Fatal(err)
	}
	a, err := k.Engine.Query(context.Background(), r)
	if err != nil {
		t.Fatalf("%s: %v", req, err)
	}
	return a
}

func s(t *testing.T, k *testkit.Kit, req string) *query.SearchAnswer {
	t.Helper()
	var r query.SearchRequest
	if err := json.Unmarshal([]byte(req), &r); err != nil {
		t.Fatal(err)
	}
	a, err := k.Engine.Search(context.Background(), r)
	if err != nil {
		t.Fatalf("%s: %v", req, err)
	}
	return a
}

func js(v any) string { b, _ := json.Marshal(v); return string(b) }

func TestOutputTokensPerModelShape(t *testing.T) {
	k := kit(t)
	a := q(t, k, `{"op":"query","from":"records","where":{"all":[{"field":"type","eq":"assistant"},
{"field":"time","range":{"from":"2026-10-29T00:00:00Z","to":"2026-10-30T00:00:00Z"}}]},
"group":["model"],"aggregate":[{"fn":"sum","field":"usage.output_tokens","distinct":"message.id","as":"output_tokens"},{"fn":"count","as":"records"}],
"order":[{"by":"output_tokens","dir":"desc"}],"limit":5}`)
	if js(a.Columns) != `["model","output_tokens","records"]` {
		t.Fatalf("columns %s", js(a.Columns))
	}
	if js(a.Rows) != `[["example-model-a",10,2],["example-model-b",5,1]]` {
		t.Fatalf("rows %s", js(a.Rows))
	}
	if a.More || !strings.Contains(a.AsOf, ":") {
		t.Fatalf("as_of %q more %v", a.AsOf, a.More)
	}
	var m map[string]any
	_ = json.Unmarshal([]byte(js(a)), &m)
	for _, key := range []string{"as_of", "columns", "rows", "more"} {
		if _, ok := m[key]; !ok {
			t.Fatalf("answer lacks %q", key)
		}
	}
}

func TestFilterOperators(t *testing.T) {
	k := kit(t)
	cases := []struct {
		name, where string
		want        int
	}{
		{"eq", `{"field":"type","eq":"user"}`, 2},
		{"in", `{"field":"type","in":["user","tool_use"]}`, 3},
		{"range ints", `{"field":"usage.output_tokens","range":{"gte":10}}`, 2},
		{"under", `{"field":"cwd","under":"/example/work"}`, 1},
		{"under not a prefix", `{"field":"cwd","under":"/example/wor"}`, 0},
		{"exists", `{"field":"parent_uuid","exists":true}`, 4},
		{"not exists", `{"field":"parent_uuid","exists":false}`, 7},
		{"contains", `{"field":"cwd","contains":"work/s"}`, 1},
		{"match", `{"field":"assistant_text","match":"deleted"}`, 2},
		{"bool", `{"field":"is_error","eq":true}`, 1},
		{"any", `{"any":[{"field":"type","eq":"tool_use"},{"field":"type","eq":"tool_result"}]}`, 2},
		{"not", `{"not":{"field":"type","in":["user","assistant"]}}`, 6},
	}
	for _, c := range cases {
		a := q(t, k, `{"where":`+c.where+`,"limit":100}`)
		if len(a.Rows) != c.want {
			t.Errorf("%s: %d rows, want %d", c.name, len(a.Rows), c.want)
		}
	}
	if _, err := k.Engine.Query(context.Background(), query.Request{Where: &query.Filter{Field: "nope", Exists: ptr(true)}}); err == nil {
		t.Fatal("an unknown field was accepted")
	}
}

func ptr[T any](v T) *T { return &v }

func TestSelectRawOrderLimitAndMore(t *testing.T) {
	k := kit(t)
	a := q(t, k, `{"where":{"field":"type","eq":"assistant"},"select":["uuid","time"],"raw":true,"order":[{"by":"time","dir":"desc"}],"limit":2}`)
	if len(a.Rows) != 2 || !a.More || a.Rows[0][0] != "u5" {
		t.Fatalf("rows %s more %v", js(a.Rows), a.More)
	}
	if a.Rows[0][1] != "2026-10-29T20:00:05Z" {
		t.Fatalf("time %v", a.Rows[0][1])
	}
	if !strings.Contains(a.Raw[0], `"uuid":"u5"`) || len(a.Records) != 2 || a.Records[0].File == "" {
		t.Fatalf("raw %q records %v", a.Raw, a.Records)
	}
}

func TestFollowLinksWithinFile(t *testing.T) {
	k := kit(t)
	a := q(t, k, `{"where":{"field":"type","eq":"tool_use"},"select":["tool_use_id","type"],"follow":"result"}`)
	if len(a.Links) != 1 || len(a.Links[0]) != 1 || a.Links[0][0].Row[1] != "tool_result" {
		t.Fatalf("result link %s", js(a.Links))
	}
	p := q(t, k, `{"where":{"field":"uuid","eq":"u3"},"select":["uuid"],"follow":"parent"}`)
	if len(p.Links) != 1 || len(p.Links[0]) != 1 || p.Links[0][0].Row[0] != "u2" {
		t.Fatalf("parent link %s", js(p.Links))
	}
}

// resumeTrio: an original (project p1), a resume that replays u1 and u2 under
// a new sessionId and adds u3, and a fork in project p2 that keeps the
// original's sessionId and replays u1. Hand-computed: lines 2 + 3 + 1 = 6,
// distinct uuids 3, files for sessionId S1: original and fork = 2.
func TestResumeAndForkTrio(t *testing.T) {
	k := testkit.New(t)
	line := func(uuid, parent, sess string) string {
		p := ""
		if parent != "" {
			p = `"parentUuid":"` + parent + `",`
		}
		return `{"type":"user","uuid":"` + uuid + `",` + p + `"sessionId":"` + sess + `","message":{"role":"user","content":"hello"}}` + "\n"
	}
	k.Add("example-p1", "orig.jsonl", line("u1", "", "S1")+line("u2", "u1", "S1"))
	k.Add("example-p1", "resume.jsonl", line("u1", "", "S2")+line("u2", "u1", "S2")+line("u3", "u2", "S2"))
	k.Add("example-p2", "fork.jsonl", line("u1", "", "S1"))
	k.Parse()
	all := q(t, k, `{"where":{"field":"item","eq":-1},"aggregate":[{"fn":"count","as":"lines"},{"fn":"count","field":"uuid","distinct":"uuid","as":"uuids"}]}`)
	if js(all.Rows) != `[[6,3]]` {
		t.Fatalf("lines and distinct uuids %s", js(all.Rows))
	}
	files := q(t, k, `{"where":{"all":[{"field":"session_id","eq":"S1"},{"field":"item","eq":-1}]},"group":["file","project"],"aggregate":[{"fn":"count"}],"order":[{"by":"project"}]}`)
	if len(files.Rows) != 2 || files.Rows[0][1] != "example-p1" || files.Rows[1][1] != "example-p2" {
		t.Fatalf("the same sessionId must be two files in two projects, no merge: %s", js(files.Rows))
	}
	sess := q(t, k, `{"where":{"field":"item","eq":-1},"group":["file_session","session_id"],"aggregate":[{"fn":"count"}],"order":[{"by":"file_session"}]}`)
	// The file-name session and the line's own sessionId are separate facts; the
	// original and the fork both carry S1 yet stay two groups (no merge).
	if js(sess.Rows) != `[["fork","S1",1],["orig","S1",2],["resume","S2",3]]` {
		t.Fatalf("file session vs line session facts: %s", js(sess.Rows))
	}
	parent := q(t, k, `{"where":{"all":[{"field":"uuid","eq":"u3"}]},"select":["file","uuid"],"follow":"parent"}`)
	if len(parent.Links[0]) != 1 {
		t.Fatalf("parent of u3 must resolve to one record in the resume file, got %s", js(parent.Links))
	}
	if parent.Links[0][0].Record.File != parent.Records[0].File {
		t.Fatalf("parentUuid resolved outside the same file: %s vs %s", parent.Links[0][0].Record.File, parent.Records[0].File)
	}
}

func TestWordSearchRankedWithSnippet(t *testing.T) {
	k := kit(t)
	a := s(t, k, `{"mode":"word","q":"registry file deleted","limit":5}`)
	if len(a.Hits) != 1 {
		t.Fatalf("hits %s", js(a.Hits))
	}
	h := a.Hits[0]
	if h.Field != "assistant_text" || h.SessionID != "example-session-1" || h.Score <= 0 {
		t.Fatalf("hit %s", js(h))
	}
	if h.Snippet != "the [registry file] was [deleted]" {
		t.Fatalf("snippet %q", h.Snippet)
	}
	if h.Time != "2026-10-29T20:00:01Z" || h.Record.File == "" {
		t.Fatalf("hit %s", js(h))
	}
	two := s(t, k, `{"q":"deleted","where":{"field":"source","eq":"claude-transcript"}}`)
	if len(two.Hits) != 2 {
		t.Fatalf("hits for deleted: %d, want 2", len(two.Hits))
	}
	if page := s(t, k, `{"q":"deleted","limit":1}`); len(page.Hits) != 1 || !page.More {
		t.Fatal("limit and more")
	}
}

func TestWordSearchSpecialCharactersAreLiteral(t *testing.T) {
	k := kit(t)
	for _, c := range []string{`"quotes"`, `star*`, `NEAR`, `NEAR(a b)`, `a OR b`, `(`, `'x'`, `-deleted`} {
		a := s(t, k, js(map[string]any{"q": c}))
		_ = a // none may error; the next assertions check what they find
	}
	if a := s(t, k, `{"q":"NEAR"}`); len(a.Hits) != 1 {
		t.Fatalf("NEAR as a word: %s", js(a.Hits))
	}
	if a := s(t, k, `{"q":"star*"}`); len(a.Hits) != 1 {
		t.Fatalf("star*: %s", js(a.Hits))
	}
	if a := s(t, k, `{"q":"\"registry file\""}`); len(a.Hits) != 2 {
		t.Fatalf("phrase: %s", js(a.Hits))
	}
	if a := s(t, k, `{"q":"   "}`); len(a.Hits) != 0 || a.Note == "" {
		t.Fatal("an empty query must say so")
	}
}

func TestSubstringSearchLiteralCaseSensitiveVerified(t *testing.T) {
	k := kit(t)
	a := s(t, k, `{"mode":"substring","q":"--no-verify","fields":["tool_use.input"],"limit":1}`)
	if len(a.Hits) != 1 {
		t.Fatalf("hits %s", js(a.Hits))
	}
	h := a.Hits[0]
	if h.Field != "tool_use.input" || h.MatchAt == nil || *h.MatchAt != strings.Index(`{"command":"git commit --no-verify -m wip"}`, "--no-verify") {
		t.Fatalf("hit %s", js(h))
	}
	if !strings.Contains(h.Snippet, "--no-verify") {
		t.Fatalf("snippet %q", h.Snippet)
	}
	if n := len(s(t, k, `{"mode":"substring","q":"--NO-VERIFY"}`).Hits); n != 0 {
		t.Fatalf("case-folded hit: %d", n)
	}
	// A substring of another word is found (a word search would not).
	if n := len(s(t, k, `{"mode":"substring","q":"it comm"}`).Hits); n != 1 {
		t.Fatalf("substring inside words: %d", n)
	}
	if n := len(s(t, k, `{"mode":"word","q":"commi"}`).Hits); n != 0 {
		t.Fatal("word search matched a partial word")
	}
	// Tool results are opaque, so a body is found by substring only.
	if n := len(s(t, k, `{"mode":"substring","q":"ok don"}`).Hits); n != 1 {
		t.Fatalf("result body: %d", n)
	}
}

func TestShortSubstringQuerySaysSo(t *testing.T) {
	k := kit(t)
	for _, q := range []string{"ab", "é", "--"} {
		a := s(t, k, js(map[string]any{"mode": "substring", "q": q}))
		if len(a.Hits) != 0 || !a.TooShort || a.Note == "" {
			t.Fatalf("%q: %s", q, js(a))
		}
	}
	// Three runes, each multi-byte, is long enough.
	if a := s(t, k, `{"mode":"substring","q":"ééé"}`); a.TooShort {
		t.Fatal("three runes were called too short")
	}
}

func TestOnlyTextAndOpaqueAreIndexedAndNoContentsStored(t *testing.T) {
	k := kit(t)
	if n := k.Count(`SELECT COUNT(*) FROM fields WHERE name IN ('assistant_text','user_text','tool_use.input','tool_result.body')`); n != 0 {
		t.Fatalf("%d text or opaque values are stored as typed fields", n)
	}
	if n := k.Count(`SELECT COUNT(*) FROM texts WHERE kind='text'`); n != 4 {
		t.Fatalf("text rows %d, want 4", n)
	}
	if n := k.Count(`SELECT COUNT(*) FROM texts WHERE kind='opaque'`); n != 2 {
		t.Fatalf("opaque rows %d, want 2 (tool input, result body)", n)
	}
	// No raw line text sits in any ordinary table.
	for _, tbl := range []string{"records", "fields", "texts"} {
		rows, err := k.Store.Read().Query(`SELECT * FROM ` + tbl)
		if err != nil {
			t.Fatal(err)
		}
		cols, _ := rows.Columns()
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		for rows.Next() {
			_ = rows.Scan(ptrs...)
			for _, v := range vals {
				if str, ok := v.(string); ok && (strings.Contains(str, "registry") || strings.Contains(str, "no-verify")) {
					t.Fatalf("%s stores file text %q", tbl, str)
				}
			}
		}
		_ = rows.Close()
	}
}

func TestResetLeavesNoStaleHit(t *testing.T) {
	k := kit(t)
	if n := len(s(t, k, `{"q":"registry"}`).Hits); n != 3 {
		t.Fatalf("before: %d", n)
	}
	// Change the bytes under the copy (same length) and re-parse.
	cp := k.Store.CopyPath(1, 1)
	b, err := os.ReadFile(cp)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cp, []byte(strings.ReplaceAll(strings.ReplaceAll(string(b), "registry", "regimens"), "Registry", "Regimens")), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := k.Parser.ReparseGen(1, 1); err != nil {
		t.Fatal(err)
	}
	if n := len(s(t, k, `{"q":"registry"}`).Hits); n != 0 {
		t.Fatalf("stale hits after a reset: %d", n)
	}
	if n := len(s(t, k, `{"q":"regimens"}`).Hits); n != 3 {
		t.Fatalf("new text not found: %d", n)
	}
	if n := k.Count(`SELECT COUNT(*) FROM texts t LEFT JOIN records r ON r.id=t.rec_id WHERE r.id IS NULL`); n != 0 {
		t.Fatalf("%d orphan text rows", n)
	}
}

func TestQueryIsDegradedWhileBehind(t *testing.T) {
	k := kit(t)
	k.Engine.Behind = func() bool { return true }
	if a := q(t, k, `{"limit":1}`); !a.Degraded {
		t.Fatal("not marked degraded while behind")
	}
}
