package transcript

import (
	"testing"

	"github.com/phillipgreenii/pa-monitor/internal/core/usage"
)

// TestScanModelTokensAccumulates proves Scan sums each token category per model
// across all non-error assistant usage records in one pass (ADR 0021 §6
// "cumulative per-category / per-model token sums"). This is the ingestion the
// native CostPricer prices.
func TestScanModelTokensAccumulates(t *testing.T) {
	path := t.TempDir() + "/tokens.jsonl"
	body := `{"type":"assistant","message":{"model":"claude-opus-4-7","usage":{"input_tokens":10,"cache_creation_input_tokens":100,"cache_read_input_tokens":500,"output_tokens":50}}}
{"type":"assistant","message":{"model":"claude-opus-4-7","usage":{"input_tokens":1,"cache_creation_input_tokens":2,"cache_read_input_tokens":3,"output_tokens":4}}}
{"type":"assistant","message":{"model":"claude-sonnet-4-6","usage":{"input_tokens":7,"cache_creation_input_tokens":0,"cache_read_input_tokens":9,"output_tokens":11}}}
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
		t.Errorf("opus tokens = %+v, want input11 cc102 cr503 out54", opus)
	}
	sonnet := snap.ModelTokens["claude-sonnet-4-6"]
	if sonnet != (usage.ModelTokens{Input: 7, CacheCreation: 0, CacheRead: 9, Output: 11}) {
		t.Errorf("sonnet tokens = %+v, want input7 cc0 cr9 out11", sonnet)
	}
	if len(snap.ModelTokens) != 2 {
		t.Errorf("ModelTokens has %d models, want 2", len(snap.ModelTokens))
	}
}

// TestScanModelTokensAccumulatesCacheCreationTTLSplit proves the per-TTL
// cache_creation breakdown (pg2-xgzen) is summed into ModelTokens alongside
// the existing CacheCreation total, following the exact same per-category
// accumulation this file already pins for the other fields.
func TestScanModelTokensAccumulatesCacheCreationTTLSplit(t *testing.T) {
	path := t.TempDir() + "/ttl.jsonl"
	body := `{"type":"assistant","message":{"model":"claude-opus-4-7","usage":{"input_tokens":10,"cache_creation_input_tokens":100,"cache_read_input_tokens":500,"output_tokens":50,"cache_creation":{"ephemeral_1h_input_tokens":100,"ephemeral_5m_input_tokens":0}}}}
{"type":"assistant","message":{"model":"claude-opus-4-7","usage":{"input_tokens":1,"cache_creation_input_tokens":2,"cache_read_input_tokens":3,"output_tokens":4,"cache_creation":{"ephemeral_1h_input_tokens":0,"ephemeral_5m_input_tokens":2}}}}
{"type":"assistant","message":{"model":"claude-opus-4-7","usage":{"input_tokens":1,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":1}}}
`
	if err := writeTestFile(path, body); err != nil {
		t.Fatal(err)
	}
	snap, err := Scan(path)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	opus := snap.ModelTokens["claude-opus-4-7"]
	want := usage.ModelTokens{
		Input: 12, Output: 55, CacheCreation: 102, CacheRead: 503,
		CacheCreationEphemeral1h: 100, CacheCreationEphemeral5m: 2,
	}
	if opus != want {
		t.Errorf("opus tokens = %+v, want %+v (third line has no \"cache_creation\" object at all — must still sum with zero split)", opus, want)
	}
}

// TestScanModelTokensExcludesErrorMessages proves synthetic api-error assistant
// records (isApiErrorMessage) are NOT counted — they carry zero real usage and
// pricing them would inflate cost, exactly as the existing TotalTokens path
// already skips them.
func TestScanModelTokensExcludesErrorMessages(t *testing.T) {
	path := t.TempDir() + "/err.jsonl"
	body := `{"type":"assistant","error":"rate_limit","isApiErrorMessage":true,"message":{"model":"claude-opus-4-7","content":[{"type":"text","text":"Retrying in 5m"}],"usage":{"input_tokens":9999,"output_tokens":9999}}}
{"type":"assistant","message":{"model":"claude-opus-4-7","usage":{"input_tokens":10,"output_tokens":20}}}
`
	if err := writeTestFile(path, body); err != nil {
		t.Fatal(err)
	}
	snap, err := Scan(path)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	opus := snap.ModelTokens["claude-opus-4-7"]
	if opus.Input != 10 || opus.Output != 20 {
		t.Errorf("opus tokens = %+v, want only the non-error record (input10 out20)", opus)
	}
}

// TestScanModelTokensEmpty proves an empty transcript yields no model rows (nil
// or empty map), never a phantom entry.
func TestScanModelTokensEmpty(t *testing.T) {
	path := t.TempDir() + "/empty.jsonl"
	if err := writeTestFile(path, ""); err != nil {
		t.Fatal(err)
	}
	snap, err := Scan(path)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(snap.ModelTokens) != 0 {
		t.Errorf("empty transcript ModelTokens = %+v, want empty", snap.ModelTokens)
	}
}
