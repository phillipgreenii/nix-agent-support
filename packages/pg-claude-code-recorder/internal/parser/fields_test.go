package parser_test

import (
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/parser"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/source"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/testkit"
)

// dupSource stands in for the transcript source and yields, per line, one
// record whose field list repeats a name.
type dupSource struct{}

func (dupSource) Name() string      { return "claude-transcript" }
func (dupSource) Version() int      { return 1 }
func (dupSource) Mode() source.Mode { return source.Append }
func (dupSource) Match(path string) (source.Captures, bool) {
	return source.Captures{}, true
}

func (dupSource) Parse(_ source.Captures, raw []byte) ([]source.Record, error) {
	return []source.Record{{
		Type: "dup", Item: source.ItemLine, Session: "example-session-1",
		Fields: []source.Field{
			{Name: "twice", Kind: source.KindKeyword, S: "first"},
			{Name: "once", Kind: source.KindInt, N: 5},
			{Name: "twice", Kind: source.KindInt, N: 7},
		},
	}}, nil
}

func dupKit(t *testing.T) *testkit.Kit {
	t.Helper()
	k := testkit.New(t)
	k.Parser = parser.New(parser.Config{Store: k.Store, Sources: []source.Source{dupSource{}}, ClaudeDir: testkit.ClaudeDir, Obs: obs.New(nil)})
	return k
}

// The duplicate rule: a record holds at most one row per field name; the LAST
// value replaces the whole row, so the earlier string does not survive next to
// the later integer.
func TestDuplicateFieldNameKeepsLastValueAndReplacesWholeRow(t *testing.T) {
	k := dupKit(t)
	k.Add("example-proj", "example-session-1.jsonl", "{}\n{}\n")
	k.Parse()
	if n := k.Count(`SELECT COUNT(*) FROM records`); n != 2 {
		t.Fatalf("records %d, want 2", n)
	}
	// Three fields yielded per record, two distinct names: two rows per record.
	if n := k.Count(`SELECT COUNT(*) FROM fields`); n != 4 {
		t.Fatalf("field rows %d, want 4 (two names x two records)", n)
	}
	if n := k.Count(`SELECT COUNT(*) FROM fields f JOIN field_names n ON n.id=f.name_id WHERE n.name='twice' AND f.n=7 AND f.s IS NULL`); n != 2 {
		t.Fatalf("the last value must win and replace the whole row, got %d matching rows", n)
	}
	if n := k.Count(`SELECT COUNT(*) FROM fields f JOIN field_names n ON n.id=f.name_id WHERE n.name='twice' AND f.s='first'`); n != 0 {
		t.Fatalf("the first value survived %d times", n)
	}
	if n := k.Count(`SELECT COUNT(*) FROM fields f JOIN field_names n ON n.id=f.name_id WHERE n.name='once' AND f.n=5`); n != 2 {
		t.Fatalf("the unrepeated name changed: %d rows", n)
	}
}

// Each field name is stored once, however many rows use it, and a reparse
// reuses the ids instead of adding names.
func TestFieldNamesAreInternedOnceAcrossRowsAndReparse(t *testing.T) {
	k := dupKit(t)
	f := k.Add("example-proj", "example-session-1.jsonl", "{}\n{}\n{}\n")
	k.Parse()
	if n := k.Count(`SELECT COUNT(*) FROM field_names`); n != 2 {
		t.Fatalf("field_names %d, want 2 (once, twice)", n)
	}
	if err := k.Parser.ReparseGen(f, 1); err != nil {
		t.Fatal(err)
	}
	if n := k.Count(`SELECT COUNT(*) FROM field_names`); n != 2 {
		t.Fatalf("field_names after reparse %d, want 2", n)
	}
	if n := k.Count(`SELECT COUNT(*) FROM fields`); n != 6 {
		t.Fatalf("field rows after reparse %d, want 6", n)
	}
	// The stored table has no name text: only the integer id.
	if n := k.Count(`SELECT COUNT(*) FROM pragma_table_info('fields') WHERE name='name'`); n != 0 {
		t.Fatal("fields still has a name column")
	}
}
