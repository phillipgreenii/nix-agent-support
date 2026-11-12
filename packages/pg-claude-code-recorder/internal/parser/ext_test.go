package parser_test

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/parser"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/source"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/sources"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/testkit"
)

// fullUsageLine carries all nine usage values.
const fullUsageLine = `{"type":"assistant","uuid":"u6","parentUuid":"u5","sessionId":"example-session-1","timestamp":"2026-10-29T20:00:05Z","message":{"id":"m3","model":"example-model-a","role":"assistant","usage":{"input_tokens":7,"output_tokens":8,"cache_creation_input_tokens":9,"cache_read_input_tokens":0,"cache_creation":{"ephemeral_1h_input_tokens":4,"ephemeral_5m_input_tokens":5},"service_tier":"standard","inference_geo":"example-geo","speed":"fast"},"content":[{"type":"text","text":"done"}]}}
`

func extKit(t *testing.T) *testkit.Kit {
	t.Helper()
	return testkit.NewWithStoreOptions(t, store.Options{ExtensionTables: true})
}

// extDump lists the ext_assistant_usage rows by the identity of their record
// (the record id differs between two databases), as one text per row.
func extDump(t *testing.T, k *testkit.Kit) []string {
	t.Helper()
	rows, err := k.Store.Read().QueryContext(context.Background(), `SELECT r.off, r.item, e.input_tokens, e.output_tokens, e.cache_creation_input_tokens,
e.cache_read_input_tokens, e.cache_1h_input_tokens, e.cache_5m_input_tokens, e.service_tier, e.inference_geo, e.speed
FROM ext_assistant_usage e JOIN records r ON r.id=e.rec_id ORDER BY r.file_id, r.gen, r.off, r.item`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var off, item int64
		v := make([]sql.NullString, 9)
		if err := rows.Scan(&off, &item, &v[0], &v[1], &v[2], &v[3], &v[4], &v[5], &v[6], &v[7], &v[8]); err != nil {
			t.Fatal(err)
		}
		var parts []string
		for _, x := range v {
			if x.Valid {
				parts = append(parts, x.String)
			} else {
				parts = append(parts, "NULL")
			}
		}
		out = append(out, fmt.Sprintf("%d/%d %s", off, item, strings.Join(parts, ",")))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestUsageIsWrittenToTheExtensionTableNotToFields(t *testing.T) {
	k := extKit(t)
	k.Add("example-proj", "example-session-1.jsonl", transcriptFixture+fullUsageLine)
	k.Parse()
	got := extDump(t, k)
	if len(got) != 4 {
		t.Fatalf("ext rows %d, want 4 (one per assistant line): %v", len(got), got)
	}
	// Absent counters are NULL, never zero; a present zero stays zero.
	if !strings.HasSuffix(got[0], " 3,10,NULL,NULL,NULL,NULL,NULL,NULL,NULL") {
		t.Errorf("first row %q", got[0])
	}
	if !strings.HasSuffix(got[3], " 7,8,9,0,4,5,standard,example-geo,fast") {
		t.Errorf("full row %q", got[3])
	}
	if n := k.Count(`SELECT COUNT(*) FROM fields f JOIN field_names n ON n.id=f.name_id WHERE n.name LIKE 'message.usage.%'`); n != 0 {
		t.Errorf("%d usage rows still in fields", n)
	}
	// Fields that are not declared keep their generic rows.
	if n := k.Count(`SELECT COUNT(*) FROM fields f JOIN field_names n ON n.id=f.name_id WHERE n.name='message.model'`); n == 0 {
		t.Error("an undeclared field lost its fields row")
	}
}

func TestRunningLayoutKeepsUsageInFields(t *testing.T) {
	k := testkit.New(t)
	k.Add("example-proj", "example-session-1.jsonl", transcriptFixture)
	k.Parse()
	if k.Store.HasExt() {
		t.Fatal("the default layout has extension tables")
	}
	if n := k.Count(`SELECT COUNT(*) FROM fields f JOIN field_names n ON n.id=f.name_id WHERE n.name='message.usage.output_tokens'`); n != 3 {
		t.Fatalf("usage.output_tokens rows %d, want 3", n)
	}
}

func TestReparseReplacesExtensionRows(t *testing.T) {
	k := extKit(t)
	f := k.Add("example-proj", "example-session-1.jsonl", transcriptFixture+fullUsageLine)
	k.Parse()
	before := extDump(t, k)
	oldIDs := k.Count(`SELECT MAX(rec_id) FROM ext_assistant_usage`)
	if err := k.Parser.ReparseGen(f, 1); err != nil {
		t.Fatal(err)
	}
	after := extDump(t, k)
	if strings.Join(before, "|") != strings.Join(after, "|") {
		t.Fatalf("reparse changed the rows:\n%v\n%v", before, after)
	}
	if n := k.Count(`SELECT COUNT(*) FROM ext_assistant_usage`); n != 4 {
		t.Fatalf("rows after reparse %d, want 4 (replaced, not added)", n)
	}
	if n := k.Count(`SELECT COUNT(*) FROM ext_assistant_usage WHERE rec_id<=?`, oldIDs); n != 0 {
		t.Fatalf("%d extension rows survived the delete of their records", n)
	}
	if n := k.Count(`SELECT COUNT(*) FROM ext_assistant_usage e LEFT JOIN records r ON r.id=e.rec_id WHERE r.id IS NULL`); n != 0 {
		t.Fatalf("%d extension rows without a record", n)
	}
}

func TestReparseOfOneFileLeavesOtherFilesRows(t *testing.T) {
	k := extKit(t)
	f1 := k.Add("example-proj", "example-session-1.jsonl", transcriptFixture)
	k.Add("example-proj", "example-session-2.jsonl", strings.ReplaceAll(transcriptFixture, "example-session-1", "example-session-2"))
	k.Parse()
	if err := k.Parser.ReparseGen(f1, 1); err != nil {
		t.Fatal(err)
	}
	if n := k.Count(`SELECT COUNT(*) FROM ext_assistant_usage`); n != 6 {
		t.Fatalf("rows %d, want 6 (3 per file)", n)
	}
}

func TestRebuildReproducesExtensionRows(t *testing.T) {
	content := transcriptFixture + fullUsageLine
	a := extKit(t)
	a.Add("example-proj", "example-session-1.jsonl", content)
	a.Parse()
	want := extDump(t, a)
	// A second database parsed from the same copies is what a rebuild is.
	b := extKit(t)
	b.Add("example-proj", "example-session-1.jsonl", content)
	b.Parse()
	if got := extDump(t, b); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("rebuild differs:\n%v\n%v", got, want)
	}
	// A restart of the same database keeps the layout and the rows.
	a.Reopen()
	if !a.Store.HasExt() {
		t.Fatal("a reopened database lost its extension layout")
	}
	if got := extDump(t, a); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("reopen differs:\n%v\n%v", got, want)
	}
}

func TestEveryExtensionDeclarationResolves(t *testing.T) {
	if err := parser.CheckExtDeclarations(sources.All()); err != nil {
		t.Fatal(err)
	}
	// The declarations of the transcript source cover all nine columns.
	seen := map[string]bool{}
	for _, s := range sources.All() {
		if sd, ok := s.(source.Stored); ok {
			for _, st := range sd.Storage() {
				if st.Kind == source.StoreExt {
					seen[st.Table+"."+st.Column] = true
				}
			}
		}
	}
	tbl, _ := store.LookupExt("assistant_usage")
	for _, c := range tbl.Columns {
		if !seen["assistant_usage."+c.Name] {
			t.Errorf("column %s is declared by no field", c.Name)
		}
	}
}

type badStored struct{ source.Source }

func (badStored) Name() string { return "bad" }
func (badStored) Catalog() map[string]source.Kind {
	return map[string]source.Kind{"a": source.KindInt, "b": source.KindKeyword, "c": source.KindInt, "d": source.KindInt}
}

func (badStored) Storage() map[string]source.Storage {
	return map[string]source.Storage{
		"a": {Kind: source.StoreExt, Table: "nope", Column: "x"},
		"b": {Kind: source.StoreExt, Table: "assistant_usage", Column: "output_tokens"},
		"c": {Kind: source.StoreExt, Table: "assistant_usage", Column: "missing"},
		"d": {Kind: source.StoreExt, Table: "assistant_usage", Column: "input_tokens"},
	}
}

func TestBadExtensionDeclarationsAreReported(t *testing.T) {
	err := parser.CheckExtDeclarations([]source.Source{badStored{}})
	if err == nil {
		t.Fatal("no error")
	}
	for _, want := range []string{"unregistered extension table", "is a keyword field but ext:assistant_usage.output_tokens is INTEGER", "no such column"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q: %v", want, err)
		}
	}
	if strings.Contains(err.Error(), "input_tokens") && strings.Contains(err.Error(), "d ") {
		t.Errorf("a valid declaration was reported: %v", err)
	}
}
