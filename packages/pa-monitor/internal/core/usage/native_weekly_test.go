package usage

import (
	"testing"
	"time"
)

// TestCurrentWeeklyCost proves the native weekly entry sums every record in the
// current Monday-anchored week and prices it per model, matching what ccusage
// weekly reported (ADR 0021 §3; the week tracker consumes WeeklyEntry.TotalCost).
func TestCurrentWeeklyCost(t *testing.T) {
	// Wednesday 2026-04-22; the week's Monday anchor is 2026-04-20.
	now := time.Date(2026, 4, 22, 12, 0, 0, 0, time.UTC)
	monday := time.Date(2026, 4, 20, 0, 0, 0, 0, time.UTC)
	recs := []Record{
		// In-week: Monday and Wednesday.
		{Timestamp: monday.Add(time.Hour), Model: "claude-opus-4-7", Tokens: ModelTokens{Output: 1_000_000}}, // $25
		{Timestamp: now.Add(-time.Hour), Model: "claude-sonnet-4-6", Tokens: ModelTokens{Output: 1_000_000}}, // $15
		// Previous week — excluded.
		{Timestamp: monday.Add(-24 * time.Hour), Model: "claude-opus-4-7", Tokens: ModelTokens{Output: 4_000_000}}, // $100 excluded
	}
	entry := CurrentWeekly(recs, stdPrices, now)
	if entry == nil {
		t.Fatal("CurrentWeekly = nil, want current-week entry")
	}
	if entry.Period != "2026-04-20" {
		t.Errorf("Period = %q, want 2026-04-20 (Monday anchor)", entry.Period)
	}
	if diff := entry.TotalCost - 40.0; diff > epsilon || diff < -epsilon {
		t.Errorf("TotalCost = %.4f, want 40.00 (25 opus + 15 sonnet, prev week excluded)", entry.TotalCost)
	}
}

// TestCurrentWeeklyEmpty proves no in-week records yields nil (no entry),
// matching ccusage weekly producing no current row.
func TestCurrentWeeklyEmpty(t *testing.T) {
	now := time.Date(2026, 4, 22, 12, 0, 0, 0, time.UTC)
	if entry := CurrentWeekly(nil, stdPrices, now); entry != nil {
		t.Errorf("CurrentWeekly(nil) = %+v, want nil", entry)
	}
}
