package alertrules

import (
	"fmt"
	"testing"
	"time"
)

// pg2-vn4jb (operator ruling A, 2026-10-05): pg-router-budget-stops pages only
// on REPEATED budget stops, `sum by (role) (increase(...[1h])) >= 2`, for: 0m.
// No PromQL engine is a dependency, so both the OLD rule
// (`sum by (role) (rate(...[10m]))`, gt 0, for: 10m) and the NEW rule are
// replayed here over synthetic counter samples with the increase()/rate()
// first-sample baseline and range extrapolation of promIncrease
// (upstream_killed_test.go). Grafana evaluates every minute (group interval
// 1m); `for` is the span from the first true evaluation to a later true one.

const (
	bsOldWindow = 10 * time.Minute
	bsOldFor    = 10 * time.Minute
	bsNewWindow = time.Hour
	bsNewFor    = 0 * time.Minute
	bsNewMin    = 2.0
)

// bsOutcome is what a replayed rule did.
type bsOutcome struct {
	paged   bool          // a true evaluation lasted >= for
	trueN   int           // evaluations at which the condition held
	longest time.Duration // longest first-true -> last-consecutive-true span
	maxInc  float64       // max increase() seen
}

// bsReplay evaluates `cond(increase over window)` every ukEval over [from, to]
// and applies the Grafana `for` hold: pending starts at the first true
// evaluation and the alert fires once a true evaluation is >= forDur later.
func bsReplay(series []ukSample, from, to time.Time, window, forDur time.Duration, cond func(float64) bool) bsOutcome {
	var o bsOutcome
	var pendingFrom time.Time
	pending := false
	for t := from; !t.After(to); t = t.Add(ukEval) {
		v, ok := promIncrease(series, t, window)
		if ok && v > o.maxInc {
			o.maxInc = v
		}
		if !ok || !cond(v) {
			pending = false
			continue
		}
		o.trueN++
		if !pending {
			pending, pendingFrom = true, t
		}
		span := t.Sub(pendingFrom)
		if span > o.longest {
			o.longest = span
		}
		if span >= forDur {
			o.paged = true
		}
	}
	return o
}

// bsOld replays the pre-pg2-vn4jb rule: rate(...[10m]) > 0, for: 10m. rate() is
// increase()/window, so `> 0` is exactly `increase > 0`.
func bsOld(series []ukSample, from, to time.Time) bsOutcome {
	return bsReplay(series, from, to, bsOldWindow, bsOldFor, func(v float64) bool { return v > 0 })
}

// bsNew replays the new rule: increase(...[1h]) >= 2, for: 0m.
func bsNew(series []ukSample, from, to time.Time) bsOutcome {
	return bsReplay(series, from, to, bsNewWindow, bsNewFor, func(v float64) bool { return v >= bsNewMin })
}

// bsAlignments are the sub-minute offsets of the evaluation grid relative to
// the events, so a result does not depend on one lucky phase.
var bsAlignments = []time.Duration{0, 7 * time.Second, 15 * time.Second, 23 * time.Second, 31 * time.Second, 44 * time.Second, 53 * time.Second}

// bsEvents returns event instants at first, first+gap, first+2*gap, ... (n of them).
func bsEvents(first time.Time, gap time.Duration, n int) []time.Time {
	out := make([]time.Time, n)
	for i := range out {
		out[i] = first.Add(time.Duration(i) * gap)
	}
	return out
}

// bsRun builds the counter (existing series at `base`, or fresh from 0) and
// replays one rule over a grid shifted by `align`; it covers the lead-up to the
// first event and two hours after the last, so every window is exercised.
func bsRun(events []time.Time, base float64, fresh bool, align time.Duration, rule func([]ukSample, time.Time, time.Time) bsOutcome) bsOutcome {
	first, last := events[0], events[len(events)-1]
	series := ukSeries(first.Add(-90*time.Minute), last.Add(2*time.Hour+time.Minute), base, events, fresh)
	return rule(series, first.Add(-10*time.Minute+align), last.Add(2*time.Hour))
}

var bsT0 = ukAt("05", "12:00:10")

// The question the bead asked: does ONE budget stop page today? Replayed, it
// does NOT page under the old rule either: rate[10m] stays above 0 for just
// under 10 minutes after one event (from the first post-event sample until the
// last pre-event sample leaves the window), which is strictly less than
// `for: 10m`, so the hold never completes. The old rule paged only when a
// second event arrived inside the hold. The new rule also does not page one
// event, and its increase() stays below 2 at every evaluation.
func TestBudgetStopsOneEventDoesNotPageOldOrNew(t *testing.T) {
	for _, base := range []float64{0, 5, 72} {
		for _, align := range bsAlignments {
			t.Run(fmt.Sprintf("base%v/align%v", base, align), func(t *testing.T) {
				ev := []time.Time{bsT0}
				old := bsRun(ev, base, false, align, bsOld)
				if old.paged {
					t.Errorf("old rule paged one event (longest true span %v)", old.longest)
				}
				// Borderline, not trivially quiet: the condition holds for most of
				// the 10m hold, just short of it.
				if old.longest < 8*time.Minute || old.longest >= bsOldFor {
					t.Errorf("old rule true span for one event = %v, want in [8m, 10m)", old.longest)
				}
				now := bsRun(ev, base, false, align, bsNew)
				if now.paged || now.trueN != 0 {
					t.Errorf("new rule paged one event: %+v", now)
				}
				if now.maxInc >= bsNewMin {
					t.Errorf("one event read increase %.3f, must stay below %.0f", now.maxInc, bsNewMin)
				}
				if now.maxInc < 0.99 {
					t.Errorf("one event read increase %.3f, want ~1 (the model must see it)", now.maxInc)
				}
			})
		}
	}
}

// The pg2-68005 shape: the same two beads re-failing every ~25 minutes, so
// 2-3 budget stops per hour per role. The NEW rule pages on every grid
// alignment. The OLD rule does NOT page it: each event holds the rate above 0
// for < 10m and 25m gaps separate the holds, so nothing chains to a full `for`.
// Two events inside the hold (here 4m apart) page under both.
func TestBudgetStopsRepeatsPage(t *testing.T) {
	for _, tc := range []struct {
		name    string
		gap     time.Duration
		n       int
		wantOld bool
	}{
		{"2 events 25m apart", 25 * time.Minute, 2, false},
		{"3 events 25m apart (pg2-68005)", 25 * time.Minute, 3, false},
		{"3 events 20m apart", 20 * time.Minute, 3, false},
		{"2 events 4m apart", 4 * time.Minute, 2, true},
		{"2 events 45m apart", 45 * time.Minute, 2, false},
	} {
		for _, base := range []float64{0, 5} {
			for _, align := range bsAlignments {
				t.Run(fmt.Sprintf("%s/base%v/align%v", tc.name, base, align), func(t *testing.T) {
					ev := bsEvents(bsT0, tc.gap, tc.n)
					if got := bsRun(ev, base, false, align, bsOld); got.paged != tc.wantOld {
						t.Errorf("old rule paged = %v, want %v (longest %v)", got.paged, tc.wantOld, got.longest)
					}
					now := bsRun(ev, base, false, align, bsNew)
					if !now.paged {
						t.Errorf("new rule did not page (%+v)", now)
					}
					// Two real stops read ~2 (extrapolation only scales a dense
					// series up slightly); never wildly above the real count.
					if now.maxInc < bsNewMin || now.maxInc > float64(tc.n)+0.6 {
						t.Errorf("max increase %.3f for %d events", now.maxInc, tc.n)
					}
				})
			}
		}
	}
}

// for: 0m and the 1h window. Two events 25 minutes apart page for the ~35
// minutes until the first leaves the window; a hypothetical for: 10m would
// also page there, but would never page two events 59 minutes apart (the
// condition holds for under a minute), which is why the rule uses 0m.
func TestBudgetStopsWindowEdgeAndForInteraction(t *testing.T) {
	bsNewFor10m := func(series []ukSample, from, to time.Time) bsOutcome {
		return bsReplay(series, from, to, bsNewWindow, 10*time.Minute, func(v float64) bool { return v >= bsNewMin })
	}
	for _, tc := range []struct {
		name string
		gap  time.Duration
		// alwaysPages: every grid alignment pages under for: 0m.
		// sometimes:   some, not all, alignments page (a sub-minute overlap).
		// never:       no alignment pages.
		want    string
		for10m  bool // would for: 10m page on at least one alignment
		maxTrue time.Duration
	}{
		{"25m apart", 25 * time.Minute, "always", true, 0},
		{"51m apart", 51 * time.Minute, "always", false, 0},
		{"55m apart", 55 * time.Minute, "always", false, 0},
		{"59m apart", 59 * time.Minute, "sometimes", false, time.Minute},
		{"61m apart", 61 * time.Minute, "never", false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pages, total := 0, 0
			any10 := false
			for _, base := range []float64{0, 5} {
				for _, align := range bsAlignments {
					ev := bsEvents(bsT0, tc.gap, 2)
					total++
					now := bsRun(ev, base, false, align, bsNew)
					if now.paged {
						pages++
					}
					if tc.maxTrue > 0 && time.Duration(now.trueN)*ukEval > tc.maxTrue {
						t.Errorf("true for %d evals, want at most %v", now.trueN, tc.maxTrue)
					}
					if bsRun(ev, base, false, align, bsNewFor10m).paged {
						any10 = true
					}
				}
			}
			switch tc.want {
			case "always":
				if pages != total {
					t.Errorf("paged %d of %d alignments, want all", pages, total)
				}
			case "sometimes":
				if pages == 0 || pages == total {
					t.Errorf("paged %d of %d alignments, want some but not all", pages, total)
				}
			case "never":
				if pages != 0 {
					t.Errorf("paged %d of %d alignments, want none", pages, total)
				}
			}
			if any10 != tc.for10m {
				t.Errorf("for: 10m would page = %v, want %v", any10, tc.for10m)
			}
		})
	}
}

// Boundary of the `>= 2` bar on an existing series: one event reads ~1 and
// never reaches 2 at any phase of the window (including with the first sample
// in the window right next to the event), two events read ~2 and page, and
// the increase is not lifted by range extrapolation to the next integer.
func TestBudgetStopsBoundaryOneVersusTwo(t *testing.T) {
	for off := time.Duration(0); off < 2*time.Minute; off += 5 * time.Second {
		for _, base := range []float64{0, 1, 9} {
			one := bsRun([]time.Time{bsT0.Add(off)}, base, false, 0, bsNew)
			if one.paged || one.maxInc >= bsNewMin {
				t.Errorf("one event (off %v, base %v): %+v", off, base, one)
			}
			two := bsRun(bsEvents(bsT0.Add(off), 2*time.Minute, 2), base, false, 0, bsNew)
			if !two.paged || two.maxInc < bsNewMin || two.maxInc > 2.6 {
				t.Errorf("two events (off %v, base %v): %+v", off, base, two)
			}
		}
	}
}

// A NEW series (the first budget stop of a role after a pg-router restart)
// does not count its first increment: increase() has no earlier sample to
// subtract, so two stops read as 1 and the THIRD pages. This is ACCEPTED
// (documented in alerts.yaml; not pre-initialized because the Emitter does not
// know the role set), and pinned here so a change is deliberate. The same
// stops on an already-existing series page on the second.
func TestBudgetStopsFreshSeriesUndercountsByOne(t *testing.T) {
	for _, align := range bsAlignments {
		for _, tc := range []struct {
			n         int
			freshWant bool
		}{{1, false}, {2, false}, {3, true}, {4, true}} {
			ev := bsEvents(bsT0, 20*time.Minute, tc.n)
			fresh := bsRun(ev, 0, true, align, bsNew)
			if fresh.paged != tc.freshWant {
				t.Errorf("fresh series, %d stops, align %v: paged = %v, want %v (%+v)", tc.n, align, fresh.paged, tc.freshWant, fresh)
			}
			existing := bsRun(ev, 0, false, align, bsNew)
			if existing.paged != (tc.n >= 2) {
				t.Errorf("existing series, %d stops, align %v: paged = %v, want %v (%+v)", tc.n, align, existing.paged, tc.n >= 2, existing)
			}
		}
	}
}
