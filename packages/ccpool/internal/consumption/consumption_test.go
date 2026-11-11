package consumption

import (
	"os"
	"path/filepath"
	"testing"
)

// Fixture: msg_1 is two lines (content blocks) repeating usage and must count
// once; msg_2 is a date-suffixed haiku id (prefix match); msg_3 is an unknown
// model (default price); a user line and a malformed line are ignored.
const fixture = `{"type":"user","message":{"role":"user"}}
{"type":"assistant","message":{"id":"msg_1","model":"claude-sonnet-4-6","usage":{"input_tokens":1000000,"output_tokens":100000,"cache_creation_input_tokens":200000,"cache_read_input_tokens":500000}}}
{"type":"assistant","message":{"id":"msg_1","model":"claude-sonnet-4-6","usage":{"input_tokens":1000000,"output_tokens":100000,"cache_creation_input_tokens":200000,"cache_read_input_tokens":500000}}}
not json
{"type":"assistant","message":{"id":"msg_2","model":"claude-haiku-4-5-20251001","usage":{"input_tokens":2000000,"output_tokens":1000000,"cache_creation_input_tokens":0,"cache_read_input_tokens":1000000}}}
{"type":"assistant","message":{"id":"msg_3","model":"mystery-model","usage":{"input_tokens":1000000,"output_tokens":0,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}
`

func write(t *testing.T, s string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "t.jsonl")
	if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRead_fixture(t *testing.T) {
	r, err := Read(write(t, fixture), nil)
	if err != nil {
		t.Fatal(err)
	}
	// sonnet: 3 + 1.5 + 0.75 + 0.15 = 5.40 USD -> 540c
	// haiku (prefix match): 2 + 5 + 0 + 0.1 = 7.10 USD -> 710c
	// mystery (default 15/MTok input): 15.00 USD -> 1500c
	want := map[string]int64{"claude-sonnet-4-6": 540, "claude-haiku-4-5-20251001": 710, "mystery-model": 1500}
	if len(r.Models) != 3 {
		t.Fatalf("models = %+v", r.Models)
	}
	for _, m := range r.Models {
		if m.EstimatedCostCents != want[m.Model] {
			t.Errorf("%s cents = %d, want %d", m.Model, m.EstimatedCostCents, want[m.Model])
		}
	}
	if r.EstimatedCostCents != 2750 {
		t.Errorf("total cents = %d, want 2750", r.EstimatedCostCents)
	}
	if r.Input != 4000000 || r.Output != 1100000 || r.CacheCreation != 200000 || r.CacheRead != 1500000 {
		t.Errorf("totals = %+v", r.Tokens)
	}
	if r.Model != "mystery-model" {
		t.Errorf("model = %q", r.Model)
	}
}

func TestRead_missingAndEmptyPathReadZero(t *testing.T) {
	for _, p := range []string{"", filepath.Join(t.TempDir(), "absent.jsonl")} {
		r, err := Read(p, nil)
		if err != nil || r.EstimatedCostCents != 0 || r.Input != 0 || r.Models == nil || len(r.Models) != 0 {
			t.Errorf("path %q: %+v, %v", p, r, err)
		}
	}
}

func TestRead_overridePrices(t *testing.T) {
	r, _ := Read(write(t, fixture), PriceTable{DefaultKey: {InputPerMTok: 1}})
	if r.EstimatedCostCents != 400 { // 4M input tokens at 1 USD/MTok
		t.Errorf("cents = %d, want 400", r.EstimatedCostCents)
	}
}

func TestDefaultPrices_currentModelsResolveToOwnEntries(t *testing.T) {
	prices := DefaultPrices()
	def := prices[DefaultKey]
	for _, tc := range []struct {
		model string
		key   string
	}{
		{"claude-sonnet-5-5", "claude-sonnet-5-5"},
		{"claude-sonnet-5-5-20261101", "claude-sonnet-5-5"},
		{"claude-opus-5-5", "claude-opus-5-5"},
		{"claude-opus-5-5-20261101", "claude-opus-5-5"},
	} {
		got := priceFor(tc.model, prices)
		if got != prices[tc.key] {
			t.Errorf("%s: got %+v, want entry %s %+v", tc.model, got, tc.key, prices[tc.key])
		}
		if got == def {
			t.Errorf("%s resolved to the default price", tc.model)
		}
	}
	if s := prices["claude-sonnet-5-5"]; s.InputPerMTok != 2 || s.OutputPerMTok != 10 {
		t.Errorf("sonnet-5-5 price = %+v, want 2/10", s)
	}
	if o := prices["claude-opus-5-5"]; o.InputPerMTok != 4 || o.OutputPerMTok != 20 {
		t.Errorf("opus-5-5 price = %+v, want 4/20", o)
	}
}
