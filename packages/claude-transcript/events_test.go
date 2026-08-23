package claudetranscript

import (
	"encoding/json"
	"testing"
)

// TestUsage_UnmarshalJSON_parsesCacheCreationSplit proves the per-TTL cache
// creation breakdown parses out of the raw shape real transcripts carry
// (confirmed against a live ~/.claude transcript, pg2-xgzen):
//
//	"usage":{"cache_creation_input_tokens":10951,"cache_creation":{"ephemeral_1h_input_tokens":10951,"ephemeral_5m_input_tokens":0}}
//
// The existing flat fields must keep parsing unchanged alongside the new
// nested struct.
func TestUsage_UnmarshalJSON_parsesCacheCreationSplit(t *testing.T) {
	raw := `{
		"input_tokens": 6,
		"cache_creation_input_tokens": 10951,
		"cache_read_input_tokens": 26443,
		"output_tokens": 136,
		"cache_creation": {
			"ephemeral_1h_input_tokens": 10951,
			"ephemeral_5m_input_tokens": 0
		}
	}`

	var u Usage
	if err := json.Unmarshal([]byte(raw), &u); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if u.InputTokens != 6 {
		t.Errorf("InputTokens = %d, want 6", u.InputTokens)
	}
	if u.CacheCreationInputTokens != 10951 {
		t.Errorf("CacheCreationInputTokens = %d, want 10951", u.CacheCreationInputTokens)
	}
	if u.CacheReadInputTokens != 26443 {
		t.Errorf("CacheReadInputTokens = %d, want 26443", u.CacheReadInputTokens)
	}
	if u.OutputTokens != 136 {
		t.Errorf("OutputTokens = %d, want 136", u.OutputTokens)
	}
	if u.CacheCreation.Ephemeral1hInputTokens != 10951 {
		t.Errorf("CacheCreation.Ephemeral1hInputTokens = %d, want 10951", u.CacheCreation.Ephemeral1hInputTokens)
	}
	if u.CacheCreation.Ephemeral5mInputTokens != 0 {
		t.Errorf("CacheCreation.Ephemeral5mInputTokens = %d, want 0", u.CacheCreation.Ephemeral5mInputTokens)
	}
}

// TestUsage_UnmarshalJSON_missingCacheCreationIsZeroValue guards backward
// compatibility: transcripts written before this field existed have no
// "cache_creation" key at all. Every existing consumer must keep parsing
// those lines with no error and a zero-value split, never a panic.
func TestUsage_UnmarshalJSON_missingCacheCreationIsZeroValue(t *testing.T) {
	raw := `{
		"input_tokens": 6,
		"cache_creation_input_tokens": 0,
		"cache_read_input_tokens": 26443,
		"output_tokens": 136
	}`

	var u Usage
	if err := json.Unmarshal([]byte(raw), &u); err != nil {
		t.Fatalf("Unmarshal of a pre-field payload: %v", err)
	}
	if u.CacheCreation != (CacheCreation{}) {
		t.Errorf("CacheCreation = %+v, want zero value when \"cache_creation\" is absent", u.CacheCreation)
	}
	if u.CacheReadInputTokens != 26443 {
		t.Errorf("CacheReadInputTokens = %d, want 26443 (unaffected by the missing field)", u.CacheReadInputTokens)
	}
}

// TestMessage_UnmarshalJSON_cacheCreationNestedUnderUsage proves the split
// round-trips through the full Message envelope (message.usage.cache_creation),
// matching how it actually appears inside a transcript JSONL line.
func TestMessage_UnmarshalJSON_cacheCreationNestedUnderUsage(t *testing.T) {
	raw := `{
		"role": "assistant",
		"model": "claude-opus-4-8",
		"content": "hi",
		"usage": {
			"input_tokens": 6,
			"cache_creation_input_tokens": 10951,
			"cache_read_input_tokens": 26443,
			"output_tokens": 136,
			"cache_creation": {
				"ephemeral_5m_input_tokens": 0,
				"ephemeral_1h_input_tokens": 10951
			}
		}
	}`

	var m Message
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if m.Usage.CacheCreation.Ephemeral1hInputTokens != 10951 {
		t.Errorf("Usage.CacheCreation.Ephemeral1hInputTokens = %d, want 10951", m.Usage.CacheCreation.Ephemeral1hInputTokens)
	}
	if m.Usage.CacheCreation.Ephemeral5mInputTokens != 0 {
		t.Errorf("Usage.CacheCreation.Ephemeral5mInputTokens = %d, want 0", m.Usage.CacheCreation.Ephemeral5mInputTokens)
	}
}
