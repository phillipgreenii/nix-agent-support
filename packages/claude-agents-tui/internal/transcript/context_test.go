package transcript

import "testing"

func TestLatestContextFromFixture(t *testing.T) {
	res, err := LatestContext("../../tests/fixtures/transcripts/basic.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if res.Model != "claude-opus-4-7" {
		t.Errorf("Model = %q", res.Model)
	}
	// second assistant message usage: 5 + 0 + 700 = 705
	if res.ContextTokens != 705 {
		t.Errorf("ContextTokens = %d, want 705", res.ContextTokens)
	}
	// output_tokens: 50 (first assistant) + 20 (second) = 70
	if res.TotalTokens != 70 {
		t.Errorf("TotalTokens = %d, want 70", res.TotalTokens)
	}
}

func TestLatestContextEmptyFile(t *testing.T) {
	path := t.TempDir() + "/empty.jsonl"
	if err := writeTestFile(path, ""); err != nil {
		t.Fatal(err)
	}
	res, err := LatestContext(path)
	if err != nil {
		t.Fatal(err)
	}
	if res.Model != "" || res.ContextTokens != 0 {
		t.Errorf("empty file should give zero values, got %+v", res)
	}
}
