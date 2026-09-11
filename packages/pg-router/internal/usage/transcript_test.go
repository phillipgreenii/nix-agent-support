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
	path := writeJSONL(
		t,
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

// pg2-u2sv: a single assistant turn is written as one JSONL line PER content
// block (thinking/text/tool_use…), and every line repeats the SAME cumulative
// usage. Counting each line over-counts the turn N-fold, tripping budgets early.
// The reader must count a non-empty message id only once. (Verified against real
// transcripts: e.g. msg_01Akq… appears 5× with identical usage.)
func TestTranscriptReader_dedupesByMessageID(t *testing.T) {
	const line = `{"type":"assistant","message":{"id":"msg_A","model":"claude-opus-4-8","usage":{"input_tokens":1,"cache_creation_input_tokens":4856,"cache_read_input_tokens":146385,"output_tokens":557}}}`
	path := writeJSONL(
		t,
		line, line, line, line, line, // one 5-block turn, repeated usage
		`{"type":"assistant","message":{"id":"msg_B","model":"claude-opus-4-8","usage":{"input_tokens":2,"output_tokens":3}}}`, // a second distinct turn
	)
	got, err := NewTranscriptReader().Read(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	// msg_A counted once + msg_B once — NOT msg_A five times.
	want := Snapshot{Model: "claude-opus-4-8", InputTokens: 3, CacheCreationTokens: 4856, CacheReadTokens: 146385, OutputTokens: 560}
	if got != want {
		t.Fatalf("got %+v want %+v", got, want)
	}
}

// Lines without a message id (older transcripts) must still each be counted —
// dedupe applies only to non-empty ids, preserving per-turn summing.
func TestTranscriptReader_noIDStillSums(t *testing.T) {
	path := writeJSONL(
		t,
		`{"type":"assistant","message":{"model":"claude-opus-4-8","usage":{"input_tokens":100,"output_tokens":50}}}`,
		`{"type":"assistant","message":{"model":"claude-opus-4-8","usage":{"input_tokens":200,"output_tokens":80}}}`,
	)
	got, err := NewTranscriptReader().Read(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if got.InputTokens != 300 || got.OutputTokens != 130 {
		t.Errorf("got %+v, want input=300 output=130", got)
	}
}

// TestTranscriptReader_sumsCacheCreationTTLSplit proves the per-TTL
// cache_creation breakdown (pg2-xgzen) is summed into Snapshot alongside the
// existing CacheCreationTokens total, following the same accumulation
// pattern as the other three usage fields.
func TestTranscriptReader_sumsCacheCreationTTLSplit(t *testing.T) {
	path := writeJSONL(
		t,
		`{"type":"assistant","message":{"model":"claude-opus-4-8","usage":{"input_tokens":100,"cache_creation_input_tokens":10951,"cache_read_input_tokens":1000,"output_tokens":50,"cache_creation":{"ephemeral_1h_input_tokens":10951,"ephemeral_5m_input_tokens":0}}}}`,
		`{"type":"assistant","message":{"model":"claude-opus-4-8","usage":{"input_tokens":200,"cache_creation_input_tokens":500,"cache_read_input_tokens":2000,"output_tokens":80,"cache_creation":{"ephemeral_1h_input_tokens":0,"ephemeral_5m_input_tokens":500}}}}`,
	)
	got, err := NewTranscriptReader().Read(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	want := Snapshot{
		Model:                          "claude-opus-4-8",
		InputTokens:                    300,
		CacheCreationTokens:            11451,
		CacheReadTokens:                3000,
		OutputTokens:                   130,
		CacheCreationEphemeral1hTokens: 10951,
		CacheCreationEphemeral5mTokens: 500,
	}
	if got != want {
		t.Fatalf("got %+v want %+v", got, want)
	}
}

// TestTranscriptReader_missingCacheCreationIsZero guards backward
// compatibility: a payload with no "cache_creation" object (older
// transcripts, or any usage predating this field) must still sum with no
// error and leave the new fields at zero.
func TestTranscriptReader_missingCacheCreationIsZero(t *testing.T) {
	path := writeJSONL(
		t,
		`{"type":"assistant","message":{"model":"claude-opus-4-8","usage":{"input_tokens":100,"cache_creation_input_tokens":10,"cache_read_input_tokens":1000,"output_tokens":50}}}`,
	)
	got, err := NewTranscriptReader().Read(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if got.CacheCreationEphemeral1hTokens != 0 || got.CacheCreationEphemeral5mTokens != 0 {
		t.Errorf("got %+v, want both TTL fields zero when cache_creation is absent", got)
	}
	if got.CacheCreationTokens != 10 {
		t.Errorf("CacheCreationTokens = %d, want 10 (unaffected)", got.CacheCreationTokens)
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
