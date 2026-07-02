package usage

import (
	"context"
	"os"
	"path/filepath"
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
		{Timestamp: monday.Add(time.Hour), Model: "claude-opus-4-7", Tokens: ModelTokens{Output: 1_000_000}},   // $25
		{Timestamp: now.Add(-time.Hour), Model: "claude-sonnet-4-6", Tokens: ModelTokens{Output: 1_000_000}},   // $15
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

// TestNativePricerCurrentWeeklyFromTranscripts proves the pricer's CurrentWeekly
// walks transcripts the same way ActiveBlock does.
func TestNativePricerCurrentWeeklyFromTranscripts(t *testing.T) {
	home := t.TempDir()
	projDir := filepath.Join(home, "projects", "-tmp-proj")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 4, 22, 12, 0, 0, 0, time.UTC)
	ts := now.Add(-2 * time.Hour).Format(time.RFC3339)
	transcript := `{"type":"assistant","timestamp":"` + ts + `","message":{"model":"claude-opus-4-7","usage":{"output_tokens":1000000}}}
`
	if err := os.WriteFile(filepath.Join(projDir, "s.jsonl"), []byte(transcript), 0o600); err != nil {
		t.Fatal(err)
	}
	p := &NativePricer{ClaudeHome: home, Prices: stdPrices, Now: func() time.Time { return now }}
	entry, err := p.CurrentWeekly(context.Background())
	if err != nil {
		t.Fatalf("CurrentWeekly: %v", err)
	}
	if entry == nil {
		t.Fatal("CurrentWeekly = nil, want a current-week entry")
	}
	if diff := entry.TotalCost - 25.0; diff > epsilon || diff < -epsilon {
		t.Errorf("TotalCost = %.4f, want 25.00", entry.TotalCost)
	}
	if probed, perr := p.Probed(); !probed || perr != nil {
		t.Errorf("Probed() = (%v,%v), want (true,nil) after a successful weekly scan", probed, perr)
	}
}

// TestNativePricerWeeklyClearsPriorScanError is a regression guard: a successful
// CurrentWeekly scan MUST clear a prior recorded scan error (lastErr is shared
// with ActiveBlock via Probed). Injecting a stale error then scanning a clean
// home must leave Probed()'s error nil.
func TestNativePricerWeeklyClearsPriorScanError(t *testing.T) {
	home := t.TempDir() // empty projects/ -> clean scan
	p := &NativePricer{ClaudeHome: home, Prices: stdPrices, Now: time.Now}
	// Simulate a prior failed probe.
	p.mu.Lock()
	p.probed = true
	p.lastErr = errStale
	p.mu.Unlock()

	if _, err := p.CurrentWeekly(context.Background()); err != nil {
		t.Fatalf("CurrentWeekly: %v", err)
	}
	if _, perr := p.Probed(); perr != nil {
		t.Errorf("Probed() error = %v, want nil (success must clear the stale error)", perr)
	}
}

var errStale = &staleErr{}

type staleErr struct{}

func (*staleErr) Error() string { return "stale probe error" }
