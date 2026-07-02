package usage

import (
	"context"
	"time"
)

// mondayAnchor returns the 00:00 UTC of the Monday on or before t. ccusage
// anchors weekly buckets on Monday; the store/proto paths parse the Period as a
// UTC "YYYY-MM-DD", so anchoring in UTC keeps the round-trip consistent.
func mondayAnchor(t time.Time) time.Time {
	u := t.UTC()
	// time.Weekday: Sunday=0..Saturday=6; days since Monday:
	offset := (int(u.Weekday()) + 6) % 7
	d := u.AddDate(0, 0, -offset)
	return time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, time.UTC)
}

// CurrentWeekly sums every record in now's Monday-anchored week and prices it
// per model, returning the current week's entry (ADR 0021 §3). Returns nil when
// no records fall in the current week (ccusage weekly produces no current row).
func CurrentWeekly(records []Record, prices PriceTable, now time.Time) *WeeklyEntry {
	start := mondayAnchor(now)
	end := start.AddDate(0, 0, 7)
	byModel := map[string]ModelTokens{}
	any := false
	for _, r := range records {
		ts := r.Timestamp.UTC()
		if ts.Before(start) || !ts.Before(end) {
			continue
		}
		any = true
		mt := byModel[r.Model]
		mt.Input += r.Tokens.Input
		mt.Output += r.Tokens.Output
		mt.CacheCreation += r.Tokens.CacheCreation
		mt.CacheRead += r.Tokens.CacheRead
		byModel[r.Model] = mt
	}
	if !any {
		return nil
	}
	var cost float64
	for model, tok := range byModel {
		c, _ := prices.Cost(model, tok)
		cost += c
	}
	return &WeeklyEntry{
		Period:    start.Format("2006-01-02"),
		TotalCost: cost,
	}
}

// CurrentWeekly scans transcripts and returns the native current-week entry,
// mirroring the retired ccusage Provider.CurrentWeekly. Best-effort like
// ActiveBlock: a scan error is recorded via Probed but does not blank the entry.
func (p *NativePricer) CurrentWeekly(_ context.Context) (*WeeklyEntry, error) {
	now := time.Now
	if p.Now != nil {
		now = p.Now
	}
	records, err := scanRecords(p.ClaudeHome)
	p.mu.Lock()
	p.probed = true
	// Set unconditionally so a successful scan clears a prior error, matching
	// ActiveBlock — Probed() shares lastErr, and a stale error would otherwise
	// stick until the next ActiveBlock call.
	p.lastErr = err
	p.mu.Unlock()
	return CurrentWeekly(records, p.Prices, now()), nil
}
