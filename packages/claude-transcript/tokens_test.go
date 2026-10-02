package claudetranscript

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTranscript(t *testing.T, lines ...string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "t.jsonl")
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// A single assistant turn is written as one JSONL line per content block, all
// repeating the same message id and usage; the total MUST count it once.
func TestOutputTokens_dedupesRepeatedMessageID(t *testing.T) {
	p := writeTranscript(
		t,
		`{"type":"user","message":{"role":"user","content":"hi"}}`,
		`{"type":"assistant","message":{"id":"msg_1","usage":{"output_tokens":100}}}`,
		`{"type":"assistant","message":{"id":"msg_1","usage":{"output_tokens":100}}}`,
		`{"type":"assistant","message":{"id":"msg_2","usage":{"output_tokens":40}}}`,
	)
	got, err := OutputTokens(p)
	if err != nil {
		t.Fatal(err)
	}
	if got != 140 {
		t.Errorf("OutputTokens = %d, want 140 (msg_1 counted once)", got)
	}
}

// Within one message id the LARGEST output_tokens wins, so a streamed partial
// line followed by the final line is not summed.
func TestOutputTokens_takesMaxPerMessageID(t *testing.T) {
	p := writeTranscript(
		t,
		`{"type":"assistant","message":{"id":"msg_1","usage":{"output_tokens":5}}}`,
		`{"type":"assistant","message":{"id":"msg_1","usage":{"output_tokens":90}}}`,
		`{"type":"assistant","message":{"id":"msg_1","usage":{"output_tokens":60}}}`,
	)
	got, err := OutputTokens(p)
	if err != nil {
		t.Fatal(err)
	}
	if got != 90 {
		t.Errorf("OutputTokens = %d, want 90", got)
	}
}

// Lines with no message id cannot be deduped and each count; non-assistant and
// unparseable lines are ignored.
func TestOutputTokens_noIDCountsEachAndIgnoresNoise(t *testing.T) {
	p := writeTranscript(
		t,
		`{"type":"assistant","message":{"usage":{"output_tokens":7}}}`,
		`{"type":"assistant","message":{"usage":{"output_tokens":8}}}`,
		`{"type":"user","message":{"usage":{"output_tokens":1000}}}`,
		`not json`,
	)
	got, err := OutputTokens(p)
	if err != nil {
		t.Fatal(err)
	}
	if got != 15 {
		t.Errorf("OutputTokens = %d, want 15", got)
	}
}

func TestOutputTokens_missingFileIsError(t *testing.T) {
	if _, err := OutputTokens(filepath.Join(t.TempDir(), "nope.jsonl")); err == nil {
		t.Fatal("want error for a missing transcript")
	}
}

func TestOutputTokens_emptyTranscriptIsZero(t *testing.T) {
	p := writeTranscript(t, `{"type":"user","message":{"role":"user","content":"hi"}}`)
	got, err := OutputTokens(p)
	if err != nil || got != 0 {
		t.Errorf("OutputTokens = %d, %v; want 0, nil", got, err)
	}
}

// OutputTally is the incremental form of the same rule OutputTokens applies.
func TestOutputTally_dedupesByIDAndCountsNoID(t *testing.T) {
	var tally OutputTally
	if got := tally.Total(); got != 0 {
		t.Fatalf("zero-value Total = %d, want 0", got)
	}
	tally.Add("msg_1", 5)
	tally.Add("msg_1", 90)
	tally.Add("msg_1", 60)
	tally.Add("msg_2", 40)
	tally.Add("", 7)
	tally.Add("", 8)
	if got := tally.Total(); got != 90+40+15 {
		t.Errorf("Total = %d, want %d", got, 90+40+15)
	}
}
