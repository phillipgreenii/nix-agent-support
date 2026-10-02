package transcript

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/usage"
)

// A multi-block assistant turn is written as one JSONL line per content block,
// every line repeating the same message id and full usage. ModelTokens (priced
// into session cost) and the usage.Record ingestion MUST count that turn once,
// the same rule as TotalTokens (pg2-e0zo3) and ccusage's per-message dedup.
const dupModelTranscript = `{"type":"user","message":{"role":"user","content":"hi"}}
{"type":"assistant","timestamp":"2026-10-02T10:00:00Z","message":{"id":"msg_1","model":"claude-x","usage":{"input_tokens":3,"cache_creation_input_tokens":200,"cache_read_input_tokens":1000,"output_tokens":100,"cache_creation":{"ephemeral_1h_input_tokens":200,"ephemeral_5m_input_tokens":0}}}}
{"type":"assistant","timestamp":"2026-10-02T10:00:01Z","message":{"id":"msg_1","model":"claude-x","usage":{"input_tokens":3,"cache_creation_input_tokens":200,"cache_read_input_tokens":1000,"output_tokens":100,"cache_creation":{"ephemeral_1h_input_tokens":200,"ephemeral_5m_input_tokens":0}}}}
{"type":"assistant","timestamp":"2026-10-02T10:00:02Z","message":{"id":"msg_1","model":"claude-x","usage":{"input_tokens":3,"cache_creation_input_tokens":200,"cache_read_input_tokens":1000,"output_tokens":100,"cache_creation":{"ephemeral_1h_input_tokens":200,"ephemeral_5m_input_tokens":0}}}}
{"type":"assistant","timestamp":"2026-10-02T10:00:03Z","message":{"id":"msg_2","model":"claude-x","usage":{"input_tokens":1,"cache_creation_input_tokens":0,"cache_read_input_tokens":1200,"output_tokens":40}}}
{"type":"assistant","timestamp":"2026-10-02T10:00:04Z","message":{"id":"msg_2","model":"claude-x","usage":{"input_tokens":1,"cache_creation_input_tokens":0,"cache_read_input_tokens":1200,"output_tokens":40}}}
{"type":"assistant","timestamp":"2026-10-02T10:00:05Z","message":{"model":"claude-x","usage":{"input_tokens":1,"cache_creation_input_tokens":5,"cache_read_input_tokens":7,"output_tokens":7}}}
`

// msg_1 once + msg_2 once + the id-less line (cannot be deduplicated).
var dupModelWant = usage.ModelTokens{
	Input:                    3 + 1 + 1,
	Output:                   100 + 40 + 7,
	CacheCreation:            200 + 0 + 5,
	CacheRead:                1000 + 1200 + 7,
	CacheCreationEphemeral1h: 200,
}

func TestScan_ModelTokensDedupesByMessageID(t *testing.T) {
	snap, err := Scan(writeDupTranscript(t, dupModelTranscript))
	if err != nil {
		t.Fatal(err)
	}
	if got := snap.ModelTokens["claude-x"]; got != dupModelWant {
		t.Errorf("ModelTokens = %+v, want %+v (each message id once)", got, dupModelWant)
	}
}

func TestScanIncremental_RecordsOnePerMessageID(t *testing.T) {
	_, acc, _, err := ScanIncremental(writeDupTranscript(t, dupModelTranscript), nil)
	if err != nil {
		t.Fatal(err)
	}
	recs := acc.Records()
	if len(recs) != 3 {
		t.Fatalf("len(Records) = %d, want 3 (msg_1, msg_2, id-less)", len(recs))
	}
	var sum usage.ModelTokens
	for _, r := range recs {
		sum.Input += r.Tokens.Input
		sum.Output += r.Tokens.Output
		sum.CacheCreation += r.Tokens.CacheCreation
		sum.CacheRead += r.Tokens.CacheRead
		sum.CacheCreationEphemeral1h += r.Tokens.CacheCreationEphemeral1h
		sum.CacheCreationEphemeral5m += r.Tokens.CacheCreationEphemeral5m
	}
	if sum != dupModelWant {
		t.Errorf("sum of Records = %+v, want %+v", sum, dupModelWant)
	}
	// The record keeps the FIRST line's timestamp for its id.
	if want := time.Date(2026, 10, 2, 10, 0, 0, 0, time.UTC); !recs[0].Timestamp.Equal(want) {
		t.Errorf("msg_1 record timestamp = %v, want %v", recs[0].Timestamp, want)
	}
}

// A streamed partial line followed by the final one for the same id: the larger
// value wins, and the record/ModelTokens end at the final figures, not the sum.
func TestScan_ModelTokensStreamedPartialThenFinal(t *testing.T) {
	body := `{"type":"assistant","timestamp":"2026-10-02T10:00:00Z","message":{"id":"msg_1","model":"claude-x","usage":{"input_tokens":3,"cache_read_input_tokens":1000,"output_tokens":2}}}
{"type":"assistant","timestamp":"2026-10-02T10:00:01Z","message":{"id":"msg_1","model":"claude-x","usage":{"input_tokens":3,"cache_read_input_tokens":1000,"output_tokens":90}}}
`
	path := writeDupTranscript(t, body)
	snap, acc, _, err := ScanIncremental(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := usage.ModelTokens{Input: 3, Output: 90, CacheRead: 1000}
	if got := snap.ModelTokens["claude-x"]; got != want {
		t.Errorf("ModelTokens = %+v, want %+v", got, want)
	}
	recs := acc.Records()
	if len(recs) != 1 || recs[0].Tokens != want {
		t.Errorf("Records = %+v, want one record with %+v", recs, want)
	}
}

// The incremental path carries the per-id state in the accumulator, so a message
// whose lines straddle two polls is still counted once.
func TestScanIncremental_ModelTokensDedupesAcrossPolls(t *testing.T) {
	lines := strings.SplitAfter(dupModelTranscript, "\n")
	split := 3 // user line + first two msg_1 lines in the first poll
	path := writeDupTranscript(t, strings.Join(lines[:split], ""))
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
	snap, acc, stats, err := ScanIncremental(path, acc)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Mode != ScanModeIncremental {
		t.Fatalf("second poll mode = %q, want incremental", stats.Mode)
	}
	if got := snap.ModelTokens["claude-x"]; got != dupModelWant {
		t.Errorf("incremental ModelTokens = %+v, want %+v", got, dupModelWant)
	}
	if n := len(acc.Records()); n != 3 {
		t.Errorf("incremental len(Records) = %d, want 3", n)
	}
}
