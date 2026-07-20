package transcript

import (
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/usage"
)

// TestAccumulatorRecordsMatchesPricerSkip proves Accumulator.Records() applies
// the NativePricer's exact per-line skip (native_pricer.go:218-224): keep only
// assistant records that are NOT api-error, have a non-empty model, and carry
// non-zero usage. This is the single-decode pricing ingestion that replaces the
// pricer's separate scanFile pass — one json.Unmarshal per line now feeds both
// the Snapshot and the timestamped pricing records.
func TestAccumulatorRecordsMatchesPricerSkip(t *testing.T) {
	ts := time.Date(2026, 7, 20, 10, 0, 0, 0, time.UTC)
	path := t.TempDir() + "/rec.jsonl"
	body := `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"hi"}]}}
{"type":"assistant","timestamp":"` + ts.Format(time.RFC3339Nano) + `","message":{"model":"claude-opus-4-8","usage":{"input_tokens":10,"cache_creation_input_tokens":100,"cache_read_input_tokens":500,"output_tokens":50}}}
{"type":"assistant","error":"rate_limit","isApiErrorMessage":true,"message":{"model":"claude-opus-4-8","content":[{"type":"text","text":"Retrying"}],"usage":{"input_tokens":9999,"output_tokens":9999}}}
{"type":"assistant","message":{"model":"claude-opus-4-8","usage":{"input_tokens":0,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":0}}}
{"type":"assistant","message":{"usage":{"input_tokens":5,"output_tokens":5}}}
`
	if err := writeTestFile(path, body); err != nil {
		t.Fatal(err)
	}
	_, acc, _, err := ScanIncremental(path, nil)
	if err != nil {
		t.Fatalf("ScanIncremental: %v", err)
	}
	recs := acc.Records()
	if len(recs) != 1 {
		t.Fatalf("Records() len = %d, want 1 (only the non-error, non-zero, modeled assistant record)", len(recs))
	}
	want := usage.Record{
		Timestamp: ts,
		Model:     "claude-opus-4-8",
		Tokens:    usage.ModelTokens{Input: 10, CacheCreation: 100, CacheRead: 500, Output: 50},
	}
	if !recs[0].Timestamp.Equal(want.Timestamp) || recs[0].Model != want.Model || recs[0].Tokens != want.Tokens {
		t.Errorf("Records()[0] = %+v, want %+v", recs[0], want)
	}
}

// TestSnapshotUnaffectedByRecords proves adding pricing-record accumulation to
// the single decode leaves the Snapshot's ModelTokens (and the rest of the fold)
// byte-identical — the decode split is behavior-preserving for existing callers.
func TestSnapshotUnaffectedByRecords(t *testing.T) {
	path := t.TempDir() + "/tokens.jsonl"
	body := `{"type":"assistant","message":{"model":"claude-opus-4-7","usage":{"input_tokens":10,"cache_creation_input_tokens":100,"cache_read_input_tokens":500,"output_tokens":50}}}
{"type":"assistant","message":{"model":"claude-opus-4-7","usage":{"input_tokens":1,"cache_creation_input_tokens":2,"cache_read_input_tokens":3,"output_tokens":4}}}
`
	if err := writeTestFile(path, body); err != nil {
		t.Fatal(err)
	}
	snap, err := Scan(path)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	opus := snap.ModelTokens["claude-opus-4-7"]
	if opus != (usage.ModelTokens{Input: 11, CacheCreation: 102, CacheRead: 503, Output: 54}) {
		t.Errorf("opus tokens = %+v, want input11 cc102 cr503 out54 (Snapshot fold unchanged)", opus)
	}
}

// TestAccumulatorRecordsIncrementalEqualsCold proves an append + incremental
// scan yields the same Records() as a fresh cold full parse (the offset tail and
// the cold path share the same fold, so records cannot diverge).
func TestAccumulatorRecordsIncrementalEqualsCold(t *testing.T) {
	ts1 := time.Date(2026, 7, 20, 10, 0, 0, 0, time.UTC)
	ts2 := time.Date(2026, 7, 20, 10, 5, 0, 0, time.UTC)
	line1 := `{"type":"assistant","timestamp":"` + ts1.Format(time.RFC3339Nano) + `","message":{"model":"m","usage":{"input_tokens":1,"output_tokens":2}}}` + "\n"
	line2 := `{"type":"assistant","timestamp":"` + ts2.Format(time.RFC3339Nano) + `","message":{"model":"m","usage":{"input_tokens":3,"output_tokens":4}}}` + "\n"

	warmPath := t.TempDir() + "/inc.jsonl"
	if err := writeTestFile(warmPath, line1); err != nil {
		t.Fatal(err)
	}
	_, acc, _, err := ScanIncremental(warmPath, nil)
	if err != nil {
		t.Fatalf("cold seed: %v", err)
	}
	f, err := os.OpenFile(warmPath, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(line2); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	_, acc2, _, err := ScanIncremental(warmPath, acc)
	if err != nil {
		t.Fatalf("incremental: %v", err)
	}
	warm := acc2.Records()

	coldPath := t.TempDir() + "/cold.jsonl"
	if err := writeTestFile(coldPath, line1+line2); err != nil {
		t.Fatal(err)
	}
	_, coldAcc, _, err := ScanIncremental(coldPath, nil)
	if err != nil {
		t.Fatalf("cold: %v", err)
	}
	cold := coldAcc.Records()

	if !reflect.DeepEqual(warm, cold) {
		t.Errorf("incremental Records()=%+v != cold Records()=%+v", warm, cold)
	}
	if len(cold) != 2 {
		t.Errorf("cold Records() len = %d, want 2", len(cold))
	}
}
