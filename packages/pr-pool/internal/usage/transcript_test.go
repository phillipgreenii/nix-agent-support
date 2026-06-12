package usage

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func writeJSONL(t *testing.T, lines ...string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "transcript.jsonl")
	if err := os.WriteFile(p, []byte(joinLines(lines)), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}
func joinLines(ls []string) string {
	out := ""
	for _, l := range ls {
		out += l + "\n"
	}
	return out
}

func TestTranscriptReader_assistantOnlyCumulative(t *testing.T) {
	path := writeJSONL(t,
		`{"type":"assistant","message":{"model":"claude-opus-4-8","usage":{"input_tokens":100,"cache_creation_input_tokens":10,"cache_read_input_tokens":1000,"output_tokens":50}}}`,
		`{"type":"user","message":{"usage":{"input_tokens":99999,"output_tokens":99999}}}`, // STRAY usage — must be excluded
		`{"type":"assistant","message":{"model":"claude-opus-4-8","usage":{"input_tokens":200,"cache_creation_input_tokens":0,"cache_read_input_tokens":2000,"output_tokens":80}}}`,
	)
	r := NewTranscriptReader()
	got, err := r.Read(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	want := Snapshot{Model: "claude-opus-4-8", InputTokens: 300, CacheCreationTokens: 10, CacheReadTokens: 3000, OutputTokens: 130}
	if got != want {
		t.Fatalf("got %+v want %+v", got, want)
	}
	if got.Total() != 3440 {
		t.Errorf("Total = %d, want 3440", got.Total())
	}
}

func TestTranscriptReader_missingFileIsZero(t *testing.T) {
	r := NewTranscriptReader()
	got, err := r.Read(context.Background(), filepath.Join(t.TempDir(), "nope.jsonl"))
	if err != nil {
		t.Fatalf("missing transcript must be (zero, nil), got err %v", err)
	}
	if got != (Snapshot{}) {
		t.Errorf("want zero Snapshot, got %+v", got)
	}
}
