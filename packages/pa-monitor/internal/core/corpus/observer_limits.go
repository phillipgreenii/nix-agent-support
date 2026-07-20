package corpus

import (
	"sort"

	"github.com/phillipgreenii/pa-monitor/internal/core/limits"
)

// LimitsObserver maintains the parsed status-sibling records per file (fed by the
// Monitor's status tail) and folds them into the account-global current-window
// reading via limits.Current (the ADR-0029 window-peak fold, reused verbatim). It
// replaces the daemon's per-tick SiblingLimitsSource whole-tree walk. Its criteria
// gates it to the StatusSibling class (no Window: the greatest-resets_at window may
// live in an older file, matching the old source's unbounded read).
type LimitsObserver struct {
	recs map[string][]limits.Record // keyed by status-sibling path
}

func NewLimitsObserver() *LimitsObserver {
	return &LimitsObserver{recs: map[string][]limits.Record{}}
}

// Criteria: the StatusSibling class, no ActiveOnly and no Window.
func (o *LimitsObserver) Criteria() Criteria {
	return Criteria{Classes: []FileClass{StatusSibling}}
}

// setRecords replaces path's status records (the tail returns the whole file each fold).
func (o *LimitsObserver) setRecords(path string, recs []limits.Record) {
	o.recs[path] = recs
}

// Prune satisfies Observer; limits prunes by path (see prunePaths).
func (o *LimitsObserver) Prune(_ map[string]bool) {}

// prunePaths drops records for status paths absent from activePaths.
func (o *LimitsObserver) prunePaths(activePaths map[string]bool) {
	for p := range o.recs {
		if !activePaths[p] {
			delete(o.recs, p)
		}
	}
}

// Current flattens every file's records IN SORTED-PATH ORDER, then folds via
// limits.Current. The sort makes the fold deterministic: limits.Current's
// no-window fallback (newestPct) returns the first record at the newest ts —
// order-sensitive — and the old SiblingLimitsSource iterated sorted ReadDir
// order, so sorting by path keeps parity and avoids Go map-iteration flakiness.
func (o *LimitsObserver) Current() *limits.Limits {
	paths := make([]string, 0, len(o.recs))
	for p := range o.recs {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	var all []limits.Record
	for _, p := range paths {
		all = append(all, o.recs[p]...)
	}
	return limits.Current(all)
}
