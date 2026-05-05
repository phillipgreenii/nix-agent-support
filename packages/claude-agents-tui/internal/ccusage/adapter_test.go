package ccusage

import (
	"os"
	"testing"
)

func TestParseActiveBlock(t *testing.T) {
	body, err := os.ReadFile("../../tests/fixtures/ccusage/active_block.json")
	if err != nil {
		t.Fatal(err)
	}
	b, err := ParseActiveBlock(body)
	if err != nil {
		t.Fatal(err)
	}
	if b == nil {
		t.Fatal("want non-nil active block")
	}
	if b.CostUSD != 11.12 {
		t.Errorf("CostUSD = %v", b.CostUSD)
	}
	if b.BurnRate.TokensPerMinute != 208897.48 {
		t.Errorf("TokensPerMinute = %v", b.BurnRate.TokensPerMinute)
	}
	if b.Projection.RemainingMinutes != 184 {
		t.Errorf("RemainingMinutes = %v", b.Projection.RemainingMinutes)
	}
}

func TestParseActiveBlockNoActive(t *testing.T) {
	body := []byte(`{"blocks":[{"isActive":false,"costUSD":5.0}]}`)
	b, err := ParseActiveBlock(body)
	if err != nil {
		t.Fatal(err)
	}
	if b != nil {
		t.Errorf("expected nil when no active block, got %+v", b)
	}
}
