package ccusage

import (
	"os"
	"testing"
)

func TestParseActiveBlock(t *testing.T) {
	body, err := os.ReadFile("../../../tests/fixtures/ccusage/active_block.json")
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

func TestParseWeekly_LastEntryIsCurrent(t *testing.T) {
	body := []byte(`{
		"totals": {"totalCost": 100.0},
		"weekly": [
			{"period": "2026-05-11", "totalCost": 10.0, "agent": "all"},
			{"period": "2026-05-18", "totalCost": 90.0, "agent": "all"}
		]
	}`)
	got, err := ParseWeekly(body)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("nil")
	}
	if got.Period != "2026-05-18" {
		t.Errorf("Period = %q", got.Period)
	}
	if got.TotalCost != 90.0 {
		t.Errorf("TotalCost = %v", got.TotalCost)
	}
}

func TestParseWeekly_EmptyReturnsNil(t *testing.T) {
	body := []byte(`{"totals":{"totalCost":0.0},"weekly":[]}`)
	got, err := ParseWeekly(body)
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Errorf("expected nil, got %+v", got)
	}
}
