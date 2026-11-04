package query_test

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/query"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/testkit"
)

const (
	bodyRelA = "projects/proj-x/sess-1/tool-results/hook-alpha.txt"
	bodyRelB = "projects/proj-x/sess-1/tool-results/hook-beta.txt"
	bodyRelC = "projects/proj-x/sess-1/tool-results/hook-unreferenced.txt"
)

func resultLine(uuid, id, content string) string {
	return `{"type":"user","uuid":"` + uuid + `","sessionId":"sess-1","timestamp":"2026-10-29T20:00:00Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"` + id + `","content":"` + content + `"}]}}` + "\n"
}

func bodyFixture(t *testing.T) *testkit.Kit {
	t.Helper()
	k := testkit.New(t)
	k.Add("proj-x", "sess-1.jsonl",
		resultLine("r1", "t1", "Output too large. Full output saved to: /example/claude/"+bodyRelA)+
			resultLine("r2", "t2", "Output too large. Full output saved to: /other/home/"+bodyRelB)+
			resultLine("r3", "t3", "Output too large. Full output saved to: /example/claude/"+bodyRelA)+
			resultLine("r4", "t4", "plain output, nothing saved"))
	k.AddToolResultBody("proj-x", "sess-1", "hook-alpha.txt", "alpha output\n")
	k.AddToolResultBody("proj-x", "sess-1", "hook-beta.txt", "beta output\n")
	k.AddToolResultBody("proj-x", "sess-1", "hook-unreferenced.txt", "nobody names me\n")
	k.Parse()
	return k
}

// legacyBodyToResults is the former trigram phrase match of a body's rel_path
// against tool_result.body, verified with strings.Contains. It is kept here only
// as the baseline the stored-field link is compared with.
func legacyBodyToResults(t *testing.T, k *testkit.Kit, rel string) []string {
	t.Helper()
	rs, err := k.Store.Read().Query(`SELECT r.id,r.file_id,r.gen,r.off,r.item,r.source,r.len FROM fts_tri JOIN texts t ON t.id=fts_tri.rowid JOIN records r ON r.id=t.rec_id JOIN files fl ON fl.id=r.file_id AND r.gen=fl.cur_gen WHERE fts_tri MATCH ? AND t.field='tool_result.body' AND r.type='tool_result' ORDER BY r.id`,
		`"`+strings.ReplaceAll(rel, `"`, `""`)+`"`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rs.Close() }()
	var out []string
	for rs.Next() {
		var id, file, off, ln int64
		var gen, item int
		var src string
		if err := rs.Scan(&id, &file, &gen, &off, &item, &src, &ln); err != nil {
			t.Fatal(err)
		}
		text, err := k.Parser.Value(file, gen, off, int(ln), src, item, "tool_result.body")
		if err == nil && strings.Contains(text, rel) {
			out = append(out, js(query.RecordRef{File: query.FileRef(file), Gen: gen, Offset: off, Item: item}))
		}
	}
	return out
}

// TestFollowResultFromBodyMatchesTrigramLink compares, per body record, the
// results the stored saved_to link returns with the former trigram match,
// exactly, then drops the trigram table and asks again to show it is unread.
func TestFollowResultFromBodyMatchesTrigramLink(t *testing.T) {
	k := bodyFixture(t)
	ask := func() map[string][]string {
		a := q(t, k, `{"where":{"field":"type","eq":"tool_result_body"},"select":["rel_path"],"follow":"result"}`)
		got := map[string][]string{}
		for i, row := range a.Rows {
			rel := row[0].(string)
			for _, l := range a.Links[i] {
				got[rel] = append(got[rel], js(l.Record))
			}
		}
		if len(a.Rows) != 3 {
			t.Fatalf("rows %s", js(a.Rows))
		}
		return got
	}
	got := ask()
	for rel, want := range map[string]int{bodyRelA: 2, bodyRelB: 1, bodyRelC: 0} {
		old := legacyBodyToResults(t, k, rel)
		if len(old) != want {
			t.Fatalf("baseline for %s has %d links, want %d", rel, len(old), want)
		}
		if js(old) != js(append([]string(nil), got[rel]...)) && !(len(old) == 0 && len(got[rel]) == 0) {
			t.Errorf("%s: new %v, old %v", rel, got[rel], old)
		}
	}
	err := k.Store.WithWrite("", func(tx *sql.Tx) error {
		_, err := tx.Exec(`DROP TABLE fts_tri`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if again := ask(); js(again) != js(got) {
		t.Fatalf("answer changed without the trigram table: %v vs %v", again, got)
	}
}
