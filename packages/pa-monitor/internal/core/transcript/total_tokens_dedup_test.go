package transcript

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A multi-block assistant turn is written as one JSONL line per content block,
// every line repeating the same message id and usage. TotalTokens (the source
// of pa_monitor_session_tokens) MUST count that turn once.
const dupAssistantTranscript = `{"type":"user","message":{"role":"user","content":"hi"}}
{"type":"assistant","message":{"id":"msg_1","model":"claude-x","usage":{"input_tokens":1,"output_tokens":100}}}
{"type":"assistant","message":{"id":"msg_1","model":"claude-x","usage":{"input_tokens":1,"output_tokens":100}}}
{"type":"assistant","message":{"id":"msg_1","model":"claude-x","usage":{"input_tokens":1,"output_tokens":100}}}
{"type":"assistant","message":{"id":"msg_2","model":"claude-x","usage":{"input_tokens":1,"output_tokens":40}}}
{"type":"assistant","message":{"id":"msg_2","model":"claude-x","usage":{"input_tokens":1,"output_tokens":40}}}
{"type":"assistant","message":{"model":"claude-x","usage":{"input_tokens":1,"output_tokens":7}}}
`

// 100 (msg_1 once) + 40 (msg_2 once) + 7 (no id, cannot be deduplicated).
const dupAssistantWant = 147

func writeDupTranscript(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "t.jsonl")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestScan_TotalTokensDedupesByMessageID(t *testing.T) {
	snap, err := Scan(writeDupTranscript(t, dupAssistantTranscript))
	if err != nil {
		t.Fatal(err)
	}
	if snap.TotalTokens != dupAssistantWant {
		t.Errorf("Scan TotalTokens = %d, want %d (each message id once)", snap.TotalTokens, dupAssistantWant)
	}
}

func TestLatestContext_TotalTokensDedupesByMessageID(t *testing.T) {
	res, err := LatestContext(writeDupTranscript(t, dupAssistantTranscript))
	if err != nil {
		t.Fatal(err)
	}
	if res.TotalTokens != dupAssistantWant {
		t.Errorf("LatestContext TotalTokens = %d, want %d", res.TotalTokens, dupAssistantWant)
	}
}

// The incremental path carries the per-id tally in the accumulator, so a
// message whose lines straddle two polls is still counted once.
func TestScanIncremental_TotalTokensDedupesAcrossPolls(t *testing.T) {
	lines := strings.SplitAfter(dupAssistantTranscript, "\n")
	split := 3 // user line + first two msg_1 lines in the first poll
	path := filepath.Join(t.TempDir(), "t.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines[:split], "")), 0o600); err != nil {
		t.Fatal(err)
	}
	_, acc, _, err := ScanIncremental(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(strings.Join(lines[split:], "")); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	snap, _, stats, err := ScanIncremental(path, acc)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Mode != ScanModeIncremental {
		t.Fatalf("second poll mode = %q, want incremental", stats.Mode)
	}
	if snap.TotalTokens != dupAssistantWant {
		t.Errorf("incremental TotalTokens = %d, want %d", snap.TotalTokens, dupAssistantWant)
	}
}
