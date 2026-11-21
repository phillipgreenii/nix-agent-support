package corpus

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/usage"
)

func pricingFixture() usage.PriceTable {
	return usage.PriceTable{Default: usage.ModelPrices{InputPerMTok: 5, OutputPerMTok: 25}}
}

func rec(ts time.Time, model string, in, out int) usage.Record {
	return usage.Record{Timestamp: ts, Model: model, Tokens: usage.ModelTokens{Input: in, Output: out}}
}

// TestUsagePricing_BlockMatchesActiveBlock proves the observer's Block is exactly
// usage.ActiveBlock over the flattened record set (the observer owns only the
// records; the block math is the pure func, reused verbatim).
func TestUsagePricing_BlockMatchesActiveBlock(t *testing.T) {
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	prices := pricingFixture()
	recsA := []usage.Record{rec(now.Add(-2*time.Hour), "m", 100, 50)}
	recsB := []usage.Record{rec(now.Add(-1*time.Hour), "m", 200, 30)}

	o := NewUsagePricingObserver(prices)
	o.setRecords("a", recsA)
	o.setRecords("b", recsB)

	got := o.Block(now)
	want := usage.ActiveBlock(append(append([]usage.Record{}, recsA...), recsB...), prices, now)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Block = %+v, want %+v", got, want)
	}
	if got == nil {
		t.Fatalf("Block returned nil for in-window records")
	}
}

// TestUsagePricing_WeeklyMatchesCurrentWeekly proves Weekly is exactly
// usage.CurrentWeekly over the flattened set.
func TestUsagePricing_WeeklyMatchesCurrentWeekly(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC) // a Wednesday
	prices := pricingFixture()
	recsA := []usage.Record{rec(now.Add(-24*time.Hour), "m", 100, 50)}
	recsB := []usage.Record{rec(now.Add(-1*time.Hour), "m", 200, 30)}

	o := NewUsagePricingObserver(prices)
	o.setRecords("a", recsA)
	o.setRecords("b", recsB)

	got := o.Weekly(now)
	want := usage.CurrentWeekly(append(append([]usage.Record{}, recsA...), recsB...), prices, now)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Weekly = %+v, want %+v", got, want)
	}
}

// TestUsagePricing_ProbedTrueAfterBlock: probed flips true once Block is called,
// mirroring NativePricer.ActiveBlock setting probed=true.
func TestUsagePricing_ProbedTrueAfterBlock(t *testing.T) {
	o := NewUsagePricingObserver(pricingFixture())
	if p, _ := o.Probed(); p {
		t.Fatalf("Probed()=true before any Block/Weekly call")
	}
	o.Block(time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC))
	if p, err := o.Probed(); !p || err != nil {
		t.Fatalf("Probed() = (%v,%v), want (true,nil) after Block", p, err)
	}
}

// TestUsagePricing_NoteScanErrSurfacesInProbed proves a pricing-file scan error
// threads into Probed() (CostProbeErr parity with NativePricer.firstErr) and that
// resetErr clears it.
func TestUsagePricing_NoteScanErrSurfacesInProbed(t *testing.T) {
	o := NewUsagePricingObserver(pricingFixture())
	o.Block(time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)) // probed=true, err nil
	boom := errors.New("scan boom")
	o.noteScanErr(boom)
	if _, err := o.Probed(); err == nil {
		t.Fatalf("Probed() err = nil after noteScanErr, want non-nil")
	}
	// first-error semantics: a later different error does not overwrite.
	o.noteScanErr(errors.New("second"))
	if _, err := o.Probed(); err != boom {
		t.Fatalf("Probed() err = %v, want the first error %v", err, boom)
	}
	o.resetErr()
	if _, err := o.Probed(); err != nil {
		t.Fatalf("Probed() err = %v after resetErr, want nil", err)
	}
}

// TestUsagePricing_UsesPassedNowNotWallClock: with records anchored far in the
// past and now passed to match, the block is active — proving Block prices
// against the PASSED now, not the real wall-clock (guards the clock ship-blocker).
func TestUsagePricing_UsesPassedNowNotWallClock(t *testing.T) {
	now := time.Date(2020, 1, 15, 12, 0, 0, 0, time.UTC)
	o := NewUsagePricingObserver(pricingFixture())
	o.setRecords("a", []usage.Record{rec(now.Add(-30*time.Minute), "m", 100, 50)})
	if b := o.Block(now); b == nil {
		t.Fatalf("Block(pastNow) = nil; observer must price against the passed now, not wall-clock")
	}
}

// TestUsagePricing_PrunePathsDropsRecords: prunePaths drops a vanished path's records.
func TestUsagePricing_PrunePathsDropsRecords(t *testing.T) {
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	o := NewUsagePricingObserver(pricingFixture())
	o.setRecords("a", []usage.Record{rec(now.Add(-1*time.Hour), "m", 100, 50)})
	o.setRecords("b", []usage.Record{rec(now.Add(-1*time.Hour), "m", 999, 999)})
	o.prunePaths(map[string]bool{"a": true}) // only a survives
	blockA := o.Block(now)
	wantA := usage.ActiveBlock([]usage.Record{rec(now.Add(-1*time.Hour), "m", 100, 50)}, pricingFixture(), now)
	if !reflect.DeepEqual(blockA, wantA) {
		t.Fatalf("after prune Block = %+v, want only path a's records priced %+v", blockA, wantA)
	}
}

// TestUsagePricing_EmptyReturnsNilBlock: no records -> nil block (matches
// usage.ActiveBlock's empty guard).
func TestUsagePricing_EmptyReturnsNilBlock(t *testing.T) {
	o := NewUsagePricingObserver(pricingFixture())
	if b := o.Block(time.Now()); b != nil {
		t.Fatalf("Block with no records = %+v, want nil", b)
	}
}

// TestUsagePricing_FlattenCacheTracksChanges proves the cached flatten is reused
// while no file's records change (the same view set again) and rebuilt when a
// file grows, is replaced, or is pruned.
func TestUsagePricing_FlattenCacheTracksChanges(t *testing.T) {
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	o := NewUsagePricingObserver(pricingFixture())
	a := []usage.Record{rec(now.Add(-2*time.Hour), "m", 1, 1)}
	b := []usage.Record{rec(now.Add(-1*time.Hour), "m", 2, 2)}
	o.setRecords("a", a)
	o.setRecords("b", b)

	first := o.flatten()
	if len(first) != 2 {
		t.Fatalf("flatten len = %d, want 2", len(first))
	}
	o.setRecords("a", a) // unchanged: same view again, as every scan does
	o.setRecords("b", b)
	if !o.flatValid {
		t.Fatalf("unchanged setRecords invalidated the flatten cache")
	}
	if again := o.flatten(); &again[0] != &first[0] {
		t.Fatalf("flatten rebuilt although nothing changed")
	}

	grown := append(b[:1:1], rec(now.Add(-30*time.Minute), "m", 3, 3))
	o.setRecords("b", grown)
	if o.flatValid {
		t.Fatalf("a grown file did not invalidate the cache")
	}
	if got := o.flatten(); len(got) != 3 {
		t.Fatalf("flatten after growth len = %d, want 3", len(got))
	}

	o.prunePaths(map[string]bool{"a": true})
	if got := o.flatten(); len(got) != 1 {
		t.Fatalf("flatten after prune len = %d, want 1", len(got))
	}
}

// BenchmarkUsagePricing_BlockAndWeeklyIdleScan measures the per-scan pricing
// cost when no file changed: the flatten sort now runs once, not per scan.
func BenchmarkUsagePricing_BlockAndWeeklyIdleScan(b *testing.B) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	o := NewUsagePricingObserver(pricingFixture())
	files := make([][]usage.Record, 50)
	for f := range files {
		for i := 0; i < 2000; i++ {
			files[f] = append(files[f], rec(now.Add(-time.Duration(f*2000+i)*time.Second), "m", i, 1))
		}
	}
	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		for f, r := range files {
			o.setRecords(string(rune('a'+f)), r)
		}
		o.Block(now)
		o.Weekly(now)
	}
}
