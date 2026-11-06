package parser_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/faults"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/parser"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/query"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/testkit"
)

// transcriptFixture holds six lines. Hand-computed counts:
//
//	L1 user, string content            : 1 line record + 1 text child   = 2
//	L2 assistant m1, text + tool_use   : 1 + 2                          = 3
//	L3 user, tool_result (futureKey, weird): 1 + 1                      = 2
//	L4 not JSON                        : 1 bad line, no records
//	L5 assistant m1 replayed, 1 text   : 1 + 1                          = 2
//	L6 assistant m2, 1 text            : 1 + 1                          = 2
//
// 11 records, 1 bad line, unknown keys: futureKey (user, 1) and
// message.content[].weird (tool_result, 1).
const transcriptFixture = `{"type":"user","uuid":"u1","sessionId":"example-session-1","timestamp":"2026-10-29T20:00:00Z","cwd":"/example/work","message":{"role":"user","content":"find the registry file"}}
{"type":"assistant","uuid":"u2","parentUuid":"u1","sessionId":"example-session-1","timestamp":"2026-10-29T20:00:01Z","message":{"id":"m1","model":"example-model-a","role":"assistant","usage":{"input_tokens":3,"output_tokens":10},"content":[{"type":"text","text":"the registry file was deleted"},{"type":"tool_use","id":"tu_1","name":"Bash","input":{"command":"git commit --no-verify -m wip"}}]}}
{"type":"user","uuid":"u3","parentUuid":"u2","sessionId":"example-session-1","timestamp":"2026-10-29T20:00:02Z","futureKey":1,"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu_1","content":"ok done","is_error":false,"weird":true}]}}
{not json
{"type":"assistant","uuid":"u4","parentUuid":"u3","sessionId":"example-session-1","timestamp":"2026-10-29T20:00:03Z","message":{"id":"m1","model":"example-model-a","role":"assistant","usage":{"output_tokens":10},"content":[{"type":"text","text":"again"}]}}
{"type":"assistant","uuid":"u5","parentUuid":"u4","sessionId":"example-session-1","timestamp":"2026-10-29T20:00:04Z","message":{"id":"m2","model":"example-model-b","role":"assistant","usage":{"output_tokens":5},"content":[{"type":"text","text":"other words"}]}}
`

func TestTranscriptParsesIntoHandComputedRecords(t *testing.T) {
	k := testkit.New(t)
	f := k.Add("example-proj", "example-session-1.jsonl", transcriptFixture)
	k.Parse()
	if n := k.Count(`SELECT COUNT(*) FROM records`); n != 11 {
		t.Fatalf("records %d, want 11", n)
	}
	if n := k.Count(`SELECT COUNT(*) FROM records WHERE item=-1`); n != 5 {
		t.Fatalf("line records %d, want 5", n)
	}
	if n := k.Count(`SELECT COUNT(*) FROM records WHERE type='tool_use'`); n != 1 {
		t.Fatalf("tool_use %d", n)
	}
	if n := k.Count(`SELECT COUNT(*) FROM records WHERE type='text'`); n != 4 {
		t.Fatalf("text children %d, want 4", n)
	}
	bl, err := parser.BadLines(k.Store, f)
	if err != nil || len(bl) != 1 {
		t.Fatalf("bad lines %v %v", bl, err)
	}
	want := int64(strings.Index(transcriptFixture, "{not json"))
	if bl[0].Offset != want || bl[0].Len != len("{not json\n") {
		t.Fatalf("bad line at %d len %d, want %d", bl[0].Offset, bl[0].Len, want)
	}
	sum, err := parser.Summarize(k.Store)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int{}
	for _, u := range sum.UnknownKeys {
		got[u.Type+"|"+u.Path] = u.Count
	}
	if len(got) != 2 || got["user|futureKey"] != 1 || got["tool_result|message.content[].weird"] != 1 {
		t.Fatalf("unknown keys %v", got)
	}
	g, _ := k.Store.Generation(f, 1)
	if g.ParseOffset != g.CopyOffset || g.ParseOffset != int64(len(transcriptFixture)) {
		t.Fatalf("parse offset %d copy %d", g.ParseOffset, g.CopyOffset)
	}
}

func TestSubagentAndHistorySources(t *testing.T) {
	k := testkit.New(t)
	k.AddAgent("example-proj", "example-session-1", "a1", `{"type":"assistant","uuid":"x1","isSidechain":true,"sessionId":"example-session-1","message":{"id":"m9","role":"assistant","content":[{"type":"thinking","thinking":"pondering the plan"}]}}`+"\n")
	k.Add("example-proj", "history.jsonl", `{"display":"fix the example bug","timestamp":1761768000000,"project":"/example/work","sessionId":"example-session-1"}`+"\n"+`{"display":"second prompt","timestamp":1761768001000,"project":"/example/work"}`+"\n")
	k.Parse()
	if n := k.Count(`SELECT COUNT(*) FROM fields f JOIN field_names n ON n.id=f.name_id WHERE n.name='agent_id' AND f.s='a1'`); n != 1 {
		t.Fatalf("agent_id on %d records, want 1 (on the line record)", n)
	}
	if n := k.Count(`SELECT COUNT(*) FROM records WHERE type='thinking'`); n != 1 {
		t.Fatal("thinking child missing")
	}
	if n := k.Count(`SELECT COUNT(*) FROM records WHERE source='claude-history' AND type='history'`); n != 2 {
		t.Fatalf("history records %d, want 2", n)
	}
	// 1761768000000 ms is 2025-10-29T20:00:00Z.
	if n := k.Count(`SELECT COUNT(*) FROM records WHERE source='claude-history' AND time_ns=1761768000000000000`); n != 1 {
		t.Fatal("history timestamp not read as milliseconds")
	}
	if n := k.Count(`SELECT COUNT(*) FROM records WHERE session='example-session-1'`); n != 3 {
		t.Fatalf("session records %d, want 3", n)
	}
}

func TestTornLineCompletedLaterYieldsOneRecordAtItsOffset(t *testing.T) {
	k := testkit.New(t)
	l1 := `{"type":"user","uuid":"u1","sessionId":"example-session-1","message":{"role":"user","content":"first"}}` + "\n"
	l2 := `{"type":"user","uuid":"u2","sessionId":"example-session-1","message":{"role":"user","content":"second"}}` + "\n"
	f := k.Add("example-proj", "example-session-1.jsonl", l1+l2[:30])
	k.Parse()
	g, _ := k.Store.Generation(f, 1)
	if g.ParseOffset != int64(len(l1)) || g.ParseOffset > g.CopyOffset {
		t.Fatalf("parse offset %d, want %d (the last newline)", g.ParseOffset, len(l1))
	}
	if n := k.Count(`SELECT COUNT(*) FROM records WHERE item=-1`); n != 1 {
		t.Fatalf("records before completion %d", n)
	}
	k.Append(f, l2[30:])
	k.Parse()
	if n := k.Count(`SELECT COUNT(*) FROM records WHERE item=-1`); n != 2 {
		t.Fatalf("records after completion %d, want 2", n)
	}
	if n := k.Count(`SELECT COUNT(*) FROM records WHERE item=-1 AND off=?`, len(l1)); n != 1 {
		t.Fatal("the completed line is not at its original offset")
	}
	if bl, _ := parser.BadLines(k.Store, f); len(bl) != 0 {
		t.Fatalf("a torn line was counted bad: %v", bl)
	}
}

func TestSteadyStateDoesNoRework(t *testing.T) {
	k := testkit.New(t)
	k.Add("example-proj", "example-session-1.jsonl", transcriptFixture)
	k.Parse()
	seq := k.Count(`SELECT CAST(value AS INTEGER) FROM meta WHERE key='parse_seq'`)
	maxID := k.Count(`SELECT MAX(id) FROM records`)
	k.Parse()
	k.Parse()
	if got := k.Count(`SELECT CAST(value AS INTEGER) FROM meta WHERE key='parse_seq'`); got != seq {
		t.Fatalf("a parse with nothing new committed (%d -> %d)", seq, got)
	}
	if got := k.Count(`SELECT MAX(id) FROM records`); got != maxID {
		t.Fatalf("records were re-inserted (%d -> %d)", maxID, got)
	}
	if n := k.Count(`SELECT COUNT(*) FROM parse_resets`); n != 0 {
		t.Fatalf("a reset was recorded without a re-parse")
	}
}

func TestCrashBeforeParseCommitLeavesNothingAndRetriesOnce(t *testing.T) {
	k := testkit.New(t)
	f := k.Add("example-proj", "example-session-1.jsonl", transcriptFixture)
	disarm := faults.ArmPanic("parse.before-commit")
	crashed := faults.Recover(func() { _, _ = k.Parser.ParseFile(f) })
	disarm()
	if !crashed {
		t.Fatal("the crash point was not reached")
	}
	if n := k.Count(`SELECT COUNT(*) FROM records`); n != 0 {
		t.Fatalf("%d records survived a crash before the commit", n)
	}
	if n := k.Count(`SELECT COUNT(*) FROM bad_lines`); n != 0 {
		t.Fatal("bad lines survived a crash before the commit")
	}
	g, _ := k.Store.Generation(f, 1)
	if g.ParseOffset != 0 {
		t.Fatalf("parse offset moved to %d without a commit", g.ParseOffset)
	}
	k.Reopen()
	k.Parse()
	if n := k.Count(`SELECT COUNT(*) FROM records`); n != 11 {
		t.Fatalf("records after retry %d, want 11", n)
	}
}

func TestParseOffsetNeverPassesCopyOffset(t *testing.T) {
	k := testkit.New(t)
	f := k.Add("example-proj", "example-session-1.jsonl", transcriptFixture)
	if err := k.Store.SetParseOffset(f, 1, int64(len(transcriptFixture))+1); err == nil {
		t.Fatal("a parse offset beyond the copy offset was accepted")
	}
	k.Parse()
	g, _ := k.Store.Generation(f, 1)
	if g.ParseOffset > g.CopyOffset {
		t.Fatalf("parse %d > copy %d", g.ParseOffset, g.CopyOffset)
	}
}

func TestReparseReplacesAtomicallyWithResetMarker(t *testing.T) {
	k := testkit.New(t)
	f := k.Add("example-proj", "example-session-1.jsonl", transcriptFixture)
	k.Parse()
	before := k.Count(`SELECT COUNT(*) FROM records`)
	minBefore := k.Count(`SELECT MIN(id) FROM records`)
	if err := k.Parser.ReparseGen(f, 1); err != nil {
		t.Fatal(err)
	}
	if got := k.Count(`SELECT COUNT(*) FROM records`); got != before {
		t.Fatalf("records %d after re-parse, want %d", got, before)
	}
	if got := k.Count(`SELECT MIN(id) FROM records`); got <= minBefore {
		t.Fatal("re-parse reused old record rows")
	}
	if n := k.Count(`SELECT COUNT(*) FROM parse_resets WHERE file_id=? AND gen=1`, f); n != 1 {
		t.Fatalf("reset markers %d, want 1", n)
	}
	// The marker precedes the new records: its next_rec is the first new id.
	if k.Count(`SELECT next_rec FROM parse_resets`) != k.Count(`SELECT MIN(id) FROM records`) {
		t.Fatal("the reset marker does not precede the new records")
	}
	if n := k.Count(`SELECT COUNT(*) FROM bad_lines`); n != 1 {
		t.Fatalf("bad lines %d after re-parse, want 1 (not doubled)", n)
	}
	if n := k.Count(`SELECT SUM(count) FROM unknown_keys`); n != 2 {
		t.Fatalf("unknown key counts %d after re-parse, want 2 (not doubled)", n)
	}
	if k.Obs.EventsNamed("parser_reset") == nil {
		t.Fatal("no parser_reset event")
	}
}

// parser_reset is a debug event: a parser-version re-parse emits one per file,
// a burst that must not fill the info-level log, yet tests still see it.
func TestParserResetIsDebugLevel(t *testing.T) {
	k := testkit.New(t)
	f := k.Add("example-proj", "example-session-1.jsonl", transcriptFixture)
	k.Parse()
	var buf bytes.Buffer
	k.Obs.Log = slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if err := k.Parser.ReparseGen(f, 1); err != nil {
		t.Fatal(err)
	}
	if len(k.Obs.EventsNamed("parser_reset")) == 0 {
		t.Fatal("parser_reset not recorded for EventsNamed")
	}
	if strings.Contains(buf.String(), "parser_reset") {
		t.Fatalf("parser_reset reached the info-level log: %s", buf.String())
	}
}

func TestRebuildEqualityAfterReparse(t *testing.T) {
	k := testkit.New(t)
	k.Add("example-proj", "example-session-1.jsonl", transcriptFixture)
	k.Parse()
	dump := func() string {
		rows, err := k.Store.Read().Query(`SELECT r.type||'|'||r.item||'|'||r.off||'|'||COALESCE(fn.name,'')||'='||COALESCE(f.s,f.n,f.f,'')
FROM records r LEFT JOIN fields f ON f.rec_id=r.id LEFT JOIN field_names fn ON fn.id=f.name_id ORDER BY r.off,r.item,fn.name`)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = rows.Close() }()
		var b strings.Builder
		for rows.Next() {
			var s string
			_ = rows.Scan(&s)
			b.WriteString(s + "\n")
		}
		return b.String()
	}
	first := dump()
	if err := k.Parser.Reparse("claude-transcript"); err != nil {
		t.Fatal(err)
	}
	if second := dump(); second != first {
		t.Fatalf("a re-parse changed the records:\n%s\n---\n%s", first, second)
	}
}

func run(t *testing.T, k *testkit.Kit, req string) *query.Answer {
	t.Helper()
	var r query.Request
	if err := json.Unmarshal([]byte(req), &r); err != nil {
		t.Fatal(err)
	}
	a, err := k.Engine.Query(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestSupersededGenerationHiddenUnlessAsked(t *testing.T) {
	k := testkit.New(t)
	f := k.Add("example-proj", "example-session-1.jsonl", `{"type":"user","uuid":"old1","sessionId":"example-session-1"}`+"\n")
	k.Parse()
	k.NewGeneration(f, `{"type":"user","uuid":"new1","sessionId":"example-session-1"}`+"\n"+`{"type":"user","uuid":"new2","sessionId":"example-session-1"}`+"\n")
	k.Parse()
	cur := run(t, k, `{"where":{"field":"type","eq":"user"},"select":["uuid"]}`)
	if len(cur.Rows) != 2 {
		t.Fatalf("current rows %v, want the 2 of generation 2", cur.Rows)
	}
	all := run(t, k, `{"where":{"field":"type","eq":"user"},"select":["uuid"],"old_generations":true}`)
	if len(all.Rows) != 3 {
		t.Fatalf("with old generations %d rows, want 3", len(all.Rows))
	}
}
