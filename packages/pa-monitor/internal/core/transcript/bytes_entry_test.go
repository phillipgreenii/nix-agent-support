package transcript

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The bytes entry point must fold exactly what the file path folds.
func TestAccumulatorFoldLineMatchesScan(t *testing.T) {
	lines := []string{
		`{"type":"assistant","timestamp":"2026-07-20T10:00:00Z","message":{"model":"claude-x","usage":{"input_tokens":10,"output_tokens":50},"content":[{"type":"text","text":"hi"}]}}`,
		`{"type":"user","timestamp":"2026-07-20T10:01:00Z","message":{"content":"example prompt"}}`,
	}
	p := filepath.Join(t.TempDir(), "s.jsonl")
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	want, err := Scan(p)
	if err != nil {
		t.Fatal(err)
	}
	acc := NewAccumulator()
	for _, l := range lines {
		acc.FoldLine([]byte(l))
	}
	got := acc.Snapshot()
	if got.TotalTokens != want.TotalTokens || got.Model != want.Model || got.FirstPrompt != want.FirstPrompt {
		t.Fatalf("bytes fold %+v != file fold %+v", got, want)
	}
}
