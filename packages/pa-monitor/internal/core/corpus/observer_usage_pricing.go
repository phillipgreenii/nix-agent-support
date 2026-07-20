package corpus

import (
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/usage"
)

// UsagePricingObserver maintains the timestamped pricing-record set per transcript
// file (fed by the Monitor's tail from the single decode) and prices the current
// 5h block + current week by delegating to the pure usage.ActiveBlock /
// usage.CurrentWeekly funcs — the observer owns only the records, never the block
// math. It replaces NativePricer's per-tick whole-corpus WalkDir. Its criteria
// gates it to the Transcript class (NOT ActiveOnly — pricing needs in-window files
// beyond the active sessions); the mtime WINDOW that bounds which files the Monitor
// opens for pricing is applied by the Monitor's walk, not here.
//
// Clock: Block/Weekly price against the now PASSED IN by the Monitor (sourced from
// the poller's injectable clock), never a captured wall-clock — so an injected
// clock in tests and production is honored.
type UsagePricingObserver struct {
	prices  usage.PriceTable
	recs    map[string][]usage.Record // keyed by transcript path
	probed  bool
	lastErr error
}

// NewUsagePricingObserver builds the observer with the account price table. No
// clock and no window param: the Monitor owns both (it passes now to Block/Weekly
// and applies the walk window).
func NewUsagePricingObserver(prices usage.PriceTable) *UsagePricingObserver {
	return &UsagePricingObserver{prices: prices, recs: map[string][]usage.Record{}}
}

// Criteria: the Transcript class, no ActiveOnly (pricing folds in-window files that
// have no active session) and no Window (the Monitor's walk applies the pricing
// window W; the observer does not gate by age).
func (o *UsagePricingObserver) Criteria() Criteria {
	return Criteria{Classes: []FileClass{Transcript}}
}

// setRecords replaces path's pricing records (the tail returns the whole file's
// records each fold — including on a cache-hit — so replace, never append).
func (o *UsagePricingObserver) setRecords(path string, recs []usage.Record) {
	o.recs[path] = recs
}

// resetErr clears the accumulated scan error; the Monitor calls it at the top of
// each Scan before folding.
func (o *UsagePricingObserver) resetErr() { o.lastErr = nil }

// noteScanErr records the FIRST non-nil pricing-file scan error of this Scan,
// matching NativePricer.scanRecordsCached's firstErr (a partial scan still prices
// what it read; the error surfaces via Probed -> tree.CostProbeErr).
func (o *UsagePricingObserver) noteScanErr(err error) {
	if err != nil && o.lastErr == nil {
		o.lastErr = err
	}
}

// Prune satisfies Observer; pricing prunes by path (see prunePaths), so the
// session-id-keyed Prune is a no-op.
func (o *UsagePricingObserver) Prune(_ map[string]bool) {}

// prunePaths drops records for paths absent from activePaths (the Monitor's
// active transcript set: resolved session paths ∪ in-window walked paths).
func (o *UsagePricingObserver) prunePaths(activePaths map[string]bool) {
	for p := range o.recs {
		if !activePaths[p] {
			delete(o.recs, p)
		}
	}
}

// flatten concatenates every path's records. Order is irrelevant: ActiveBlock
// sorts by timestamp and CurrentWeekly sums, so map-iteration order cannot change
// either result (unlike the Limits fold's newestPct tiebreak).
func (o *UsagePricingObserver) flatten() []usage.Record {
	var all []usage.Record
	for _, r := range o.recs {
		all = append(all, r...)
	}
	return all
}

// Block returns the current 5h block priced from all retained records at now, or
// nil when there is no active block. Sets probed (mirrors NativePricer).
func (o *UsagePricingObserver) Block(now time.Time) *usage.Block {
	o.probed = true
	return usage.ActiveBlock(o.flatten(), o.prices, now)
}

// Weekly returns the current (Monday-anchored) week's cost from all retained
// records at now, or nil when the week has no records. Sets probed.
func (o *UsagePricingObserver) Weekly(now time.Time) *usage.WeeklyEntry {
	o.probed = true
	return usage.CurrentWeekly(o.flatten(), o.prices, now)
}

// Probed reports whether pricing has run and the first scan error, if any
// (parity with NativePricer.Probed -> tree.CostProbed / CostProbeErr).
func (o *UsagePricingObserver) Probed() (bool, error) {
	return o.probed, o.lastErr
}
