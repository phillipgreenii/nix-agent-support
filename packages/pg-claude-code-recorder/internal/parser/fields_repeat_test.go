package parser_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/parser"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/source"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/testkit"
)

// wideSource yields one record with 1,200 distinct names, then repeats of
// names whose first row is far earlier than the repeat (a wide record that an
// insert strategy may split into batches).
type wideSource struct{}

func (wideSource) Name() string      { return "claude-transcript" }
func (wideSource) Version() int      { return 1 }
func (wideSource) Mode() source.Mode { return source.Append }
func (wideSource) Match(string) (source.Captures, bool) {
	return source.Captures{}, true
}

func (wideSource) Parse(_ source.Captures, _ []byte) ([]source.Record, error) {
	var fs []source.Field
	for i := 0; i < 1200; i++ {
		fs = append(fs, source.Field{Name: fmt.Sprintf("f%d", i), Kind: source.KindInt, N: int64(i)})
	}
	// Rows 1200.. repeat earlier names: f3 (twice), f499 and f500.
	fs = append(
		fs,
		source.Field{Name: "f3", Kind: source.KindKeyword, S: "later"},
		source.Field{Name: "f499", Kind: source.KindInt, N: -499},
		source.Field{Name: "f500", Kind: source.KindInt, N: -500},
		source.Field{Name: "f3", Kind: source.KindInt, N: 33},
	)
	return []source.Record{{Type: "wide", Item: source.ItemLine, Session: "example-session-1", Fields: fs}}, nil
}

// A wide record keeps one row per name, the last value wins however far apart
// the repeats are, whole-row replacement, and names are interned in field order.
func TestFieldsRepeatedNameKeepsLastWinsAndOrder(t *testing.T) {
	k := testkit.New(t)
	k.Parser = parser.New(parser.Config{Store: k.Store, Sources: []source.Source{wideSource{}}, ClaudeDir: testkit.ClaudeDir, Obs: obs.New(nil)})
	k.Add("example-proj", "example-session-1.jsonl", "{}\n")
	k.Parse()
	if n := k.Count(`SELECT COUNT(*) FROM fields`); n != 1200 {
		t.Fatalf("field rows %d, want 1200", n)
	}
	for _, c := range []struct {
		name string
		want int
	}{{"f3", 33}, {"f499", -499}, {"f500", -500}, {"f501", 501}, {"f1199", 1199}} {
		if n := k.Count(`SELECT COUNT(*) FROM fields f JOIN field_names n ON n.id=f.name_id WHERE n.name=? AND f.n=? AND f.s IS NULL`, c.name, c.want); n != 1 {
			t.Fatalf("%s: want n=%d with s NULL, matching rows %d", c.name, c.want, n)
		}
	}
	rows, err := k.Store.Read().QueryContext(context.Background(), `SELECT name FROM field_names ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	i := 0
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		if want := fmt.Sprintf("f%d", i); name != want {
			t.Fatalf("field_names id order: position %d is %q, want %q", i, name, want)
		}
		i++
	}
	if i != 1200 {
		t.Fatalf("field_names %d, want 1200", i)
	}
}
