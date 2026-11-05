package claudetranscript

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/source"
)

var update = flag.Bool("update", false, "rewrite testdata/parse.golden")

func fixtureLines(tb testing.TB) [][]byte {
	tb.Helper()
	raw, err := os.ReadFile("testdata/lines.jsonl")
	if err != nil {
		tb.Fatal(err)
	}
	var out [][]byte
	sc := bufio.NewScanner(bytes.NewReader(raw))
	sc.Buffer(nil, 4<<20) // one fixture line is a large tool_result
	for sc.Scan() {
		out = append(out, append([]byte(nil), sc.Bytes()...))
	}
	return out
}

var fixtureCaptures = source.Captures{"project": "-work-p", "session": "s1"}

// TestParseMatchesGolden pins the records and fields Parse yields for the
// fixture lines, so a change to how fields are extracted cannot alter them.
func TestParseMatchesGolden(t *testing.T) {
	var got bytes.Buffer
	enc := json.NewEncoder(&got)
	enc.SetIndent("", " ")
	s := New()
	for _, line := range fixtureLines(t) {
		recs, err := s.Parse(fixtureCaptures, line)
		if err != nil {
			t.Fatal(err)
		}
		if err := enc.Encode(recs); err != nil {
			t.Fatal(err)
		}
	}
	if *update {
		if err := os.WriteFile("testdata/parse.golden", got.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile("testdata/parse.golden")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Bytes(), want) {
		t.Fatalf("Parse output differs from testdata/parse.golden:\n%s", got.String())
	}
}

// BenchmarkParse measures one transcript line (the fixture's lines in turn):
// run `go test -bench Parse -benchmem ./internal/source/claudetranscript/`.
// ns/op and allocs/op are per line.
func BenchmarkParse(b *testing.B) {
	lines := fixtureLines(b)
	s := New()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := s.Parse(fixtureCaptures, lines[i%len(lines)]); err != nil {
			b.Fatal(err)
		}
	}
}
