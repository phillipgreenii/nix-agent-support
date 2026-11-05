package parser_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/parser"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/source"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/testkit"
)

// wideSource yields one record with more fields rows than one multi-row
// insert carries (500): 1,200 distinct names, then repeats of names whose
// first row is in an earlier batch than the repeat.
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
	// Row 1200.. repeat earlier names: f3 (batch 0, repeat in batch 2), f499
	// (last row of batch 0, repeat in batch 2), f500 (first row of batch 1).
	fs = append(
		fs,
		source.Field{Name: "f3", Kind: source.KindKeyword, S: "later"},
		source.Field{Name: "f499", Kind: source.KindInt, N: -499},
		source.Field{Name: "f500", Kind: source.KindInt, N: -500},
		source.Field{Name: "f3", Kind: source.KindInt, N: 33},
	)
	return []source.Record{{Type: "wide", Item: source.ItemLine, Session: "example-session-1", Fields: fs}}, nil
}

// A record with more rows than one batch keeps one row per name, the last
// value wins across batch boundaries, and names are interned in field order.
func TestFieldsBatchAcrossBoundaryKeepsLastWinsAndOrder(t *testing.T) {
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
	rows, err := k.Store.Read().Query(`SELECT name FROM field_names ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
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

// One batch binds 500 rows x 5 = 2,500 values; the engine accepts at least
// that and rejects a statement over its limit, so the batch size has room.
func TestFieldsBatchUnderVariableLimit(t *testing.T) {
	k := testkit.New(t)
	db := k.Store.Read()
	run := func(n int) error {
		q := "SELECT 1 WHERE 0 IN (" + strings.TrimSuffix(strings.Repeat("?,", n), ",") + ")"
		args := make([]any, n)
		for i := range args {
			args[i] = i
		}
		rows, err := db.Query(q, args...)
		if err != nil {
			return err
		}
		return rows.Close()
	}
	if err := run(500 * 5); err != nil {
		t.Fatalf("one batch (2500 values) rejected: %v", err)
	}
	if err := run(32767); err == nil {
		t.Skip("engine limit above 32766; batch size is still safe")
	}
}
