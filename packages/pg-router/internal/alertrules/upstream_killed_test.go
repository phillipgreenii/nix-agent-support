package alertrules

import (
	"math"
	"regexp"
	"strings"
	"testing"
	"time"
)

// pg2-fy2pm: pg-router-upstream-killed pages only on a SUSTAINED burst of
// reason=upstream-killed handler errors (>= 10 in 30m, per role). No PromQL
// engine is a dependency, so the rule is modelled here: the YAML expr is
// pinned to the exact text, and a simplified Prometheus increase() (including
// the first-sample baseline and range extrapolation) is replayed over synthetic
// counter samples built from the observed 2026-10-03/04 bursts.

const wantUpstreamKilledExpr = `sum by (role) (increase(pg_router_failures_total{class="handler-error",reason="upstream-killed"}[30m])) >= 10`

const (
	ukWindow    = 30 * time.Minute
	ukThreshold = 10.0
	ukScrape    = 30 * time.Second
	ukEval      = time.Minute
)

func TestUpstreamKilledRule(t *testing.T) {
	r := ruleBlock(t, "pg-router-upstream-killed")
	if got := ruleExpr(t, r); got != wantUpstreamKilledExpr {
		t.Errorf("upstream-killed expr:\n got %q\nwant %q", got, wantUpstreamKilledExpr)
	}
	// from: 1800 matches the [30m] range; for: 0m because the window already
	// encodes "sustained"; gt 0 over the PromQL-filtered value (no gte evaluator).
	for _, need := range []string{
		"for: 0m", "noDataState: OK", "execErrState: Error", "severity: warning",
		"{{ $labels.role }}", "from: 1800", "type: gt", "params: [0]", "instant: true",
	} {
		if !strings.Contains(r, need) {
			t.Errorf("upstream-killed rule lost %q", need)
		}
	}
	// Role-only cardinality: the only grouping label is role.
	if !strings.Contains(wantUpstreamKilledExpr, "sum by (role) (") {
		t.Error("expr must aggregate by role only")
	}
	if regexp.MustCompile(`by \([^)]*(class|reason|source)`).MatchString(wantUpstreamKilledExpr) {
		t.Error("expr must not group by any label other than role")
	}
}

type ukSample struct {
	at time.Time
	v  float64
}

// ukSeries builds a cumulative counter scraped every ukScrape over [from, to].
// base is the counter value before the first kill. If fresh, the series does
// not exist until the first scrape after the first kill (a new label set after
// a pg-router restart); otherwise it exists, flat at base, from `from`.
func ukSeries(from, to time.Time, base float64, kills []time.Time, fresh bool) []ukSample {
	var out []ukSample
	for t := from; !t.After(to); t = t.Add(ukScrape) {
		v := base
		for _, k := range kills {
			if !k.After(t) {
				v++
			}
		}
		if fresh && v == base {
			continue
		}
		out = append(out, ukSample{t, v})
	}
	return out
}

// ukIncrease models Prometheus increase() over the range (t-window, t]:
// last-first scaled by range extrapolation (no counter resets in the tests,
// and ONE sample yields no result).
func ukIncrease(series []ukSample, t time.Time) (float64, bool) {
	start := t.Add(-ukWindow)
	var in []ukSample
	for _, s := range series {
		if s.at.After(start) && !s.at.After(t) {
			in = append(in, s)
		}
	}
	if len(in) < 2 {
		return 0, false
	}
	first, last := in[0], in[len(in)-1]
	delta := last.v - first.v
	sampled := last.at.Sub(first.at).Seconds()
	avg := sampled / float64(len(in)-1)
	toStart := first.at.Sub(start).Seconds()
	toEnd := t.Sub(last.at).Seconds()
	threshold := avg * 1.1
	if toStart >= threshold {
		toStart = avg / 2
	}
	if toEnd >= threshold {
		toEnd = avg / 2
	}
	if delta > 0 && first.v >= 0 {
		if toZero := sampled * (first.v / delta); toZero < toStart {
			toStart = toZero
		}
	}
	return delta * (sampled + toStart + toEnd) / sampled, true
}

// ukReplay evaluates the rule every ukEval over [from, to] and reports the
// maximum increase seen and whether `>= 10` (for: 0m) ever held.
func ukReplay(series []ukSample, from, to time.Time) (max float64, paged bool) {
	for t := from; !t.After(to); t = t.Add(ukEval) {
		v, ok := ukIncrease(series, t)
		if !ok {
			continue
		}
		if v > max {
			max = v
		}
		if v >= ukThreshold {
			paged = true
		}
	}
	return max, paged
}

func ukAt(day, hms string) time.Time {
	t, err := time.Parse(time.RFC3339, "2026-10-"+day+"T"+hms+"Z")
	if err != nil {
		panic(err)
	}
	return t
}

// ukSpread returns n kill instants evenly spread from start over span (first at
// start, last at start+span).
func ukSpread(start time.Time, span time.Duration, n int) []time.Time {
	out := make([]time.Time, n)
	for i := range out {
		if n == 1 {
			out[i] = start
			continue
		}
		out[i] = start.Add(span * time.Duration(i) / time.Duration(n-1))
	}
	return out
}

// The three bursts observed 2026-10-03/04 (role desk-pr), replayed on one
// continuous counter: only the 14-in-20-min burst pages.
func TestUpstreamKilledObservedBursts(t *testing.T) {
	// 14 in 20 min (21:46-22:05Z, 8 of them in 21:46-21:51Z).
	a := append(ukSpread(ukAt("03", "21:46:10"), 5*time.Minute, 8), ukSpread(ukAt("03", "21:52:00"), 13*time.Minute, 6)...)
	// ~3 in ~33 min (02:41-03:14Z).
	c := ukSpread(ukAt("04", "02:41:20"), 33*time.Minute, 3)
	// ~7 in ~22 min (05:38-06:00Z).
	b := ukSpread(ukAt("04", "05:38:20"), 22*time.Minute, 7)
	if len(a) != 14 || len(c) != 3 || len(b) != 7 {
		t.Fatalf("burst sizes = %d,%d,%d", len(a), len(c), len(b))
	}

	for _, fresh := range []bool{false, true} {
		name := map[bool]string{false: "existing-series", true: "fresh-series"}[fresh]
		t.Run(name, func(t *testing.T) {
			whole := append(append(append([]time.Time{}, a...), c...), b...)
			series := ukSeries(ukAt("03", "21:00:00"), ukAt("04", "07:00:00"), 5, whole, fresh)

			// Every burst evaluated in isolation (window covers burst + 30m).
			_, pagedA := ukReplay(series, ukAt("03", "21:46:00"), ukAt("03", "22:36:00"))
			maxC, pagedC := ukReplay(series, ukAt("04", "02:41:00"), ukAt("04", "03:50:00"))
			maxB, pagedB := ukReplay(series, ukAt("04", "05:38:00"), ukAt("04", "06:35:00"))
			if !pagedA {
				t.Error("14 kills in 20 min MUST page")
			}
			if pagedB || maxB >= ukThreshold {
				t.Errorf("7 kills in 22 min MUST NOT page (max increase %.2f)", maxB)
			}
			if pagedC || maxC >= ukThreshold {
				t.Errorf("3 kills in ~33 min MUST NOT page (max increase %.2f)", maxC)
			}
		})
	}
}

// Boundary: 9 kills never page, 10 kills page (>= 10 is `> 9` for the integer
// counts, and increase() extrapolation only scales UP slightly on a continuous
// series, so 9 real kills read ~9.x and stay under the 10 bar).
func TestUpstreamKilledBoundaryNineAndTen(t *testing.T) {
	from, to := ukAt("04", "10:00:00"), ukAt("04", "11:30:00")
	for _, tc := range []struct {
		name  string
		kills int
		span  time.Duration
		want  bool
	}{
		{"9 tight (5m)", 9, 5 * time.Minute, false},
		{"10 tight (5m)", 10, 5 * time.Minute, true},
		{"9 spread across the whole window", 9, 29 * time.Minute, false},
		{"10 spread across 25m", 10, 25 * time.Minute, true},
		{"10 spread over 40m (never 10 in one window)", 10, 40 * time.Minute, false},
		{"11 tight", 11, 5 * time.Minute, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			kills := ukSpread(ukAt("04", "10:10:10"), tc.span, tc.kills)
			series := ukSeries(from, to, 7, kills, false)
			max, paged := ukReplay(series, from.Add(ukWindow), to)
			if paged != tc.want {
				t.Errorf("paged = %v, want %v (max increase %.3f)", paged, tc.want, max)
			}
			if !tc.want && max >= ukThreshold {
				t.Errorf("max increase %.3f must stay below %.0f", max, ukThreshold)
			}
			if tc.want && math.Abs(max-float64(tc.kills)) > 1.0 {
				t.Errorf("max increase %.3f is not within 1 of the %d real kills", max, tc.kills)
			}
		})
	}
}

// A NEW series (first kill after a pg-router restart) does not count its first
// increment: increase() has no earlier sample to subtract, so 10 kills read as
// 9 and the 11th pages. This is ACCEPTED (documented in alerts.yaml), not
// pre-initialized; the test pins the behavior so a change is deliberate.
func TestUpstreamKilledFreshSeriesUndercountsByOne(t *testing.T) {
	from, to := ukAt("04", "10:00:00"), ukAt("04", "11:30:00")
	for _, tc := range []struct {
		kills int
		want  bool
	}{{9, false}, {10, false}, {11, true}, {14, true}} {
		kills := ukSpread(ukAt("04", "10:10:10"), 5*time.Minute, tc.kills)
		fresh := ukSeries(from, to, 0, kills, true)
		max, paged := ukReplay(fresh, from.Add(ukWindow), to)
		if paged != tc.want {
			t.Errorf("fresh series, %d kills: paged = %v, want %v (max increase %.3f)", tc.kills, paged, tc.want, max)
		}
		// The same kills on an already-existing series page one kill earlier.
		existing := ukSeries(from, to, 0, kills, false)
		if _, p := ukReplay(existing, from.Add(ukWindow), to); p != (tc.kills >= 10) {
			t.Errorf("existing series, %d kills: paged = %v, want %v", tc.kills, p, tc.kills >= 10)
		}
	}
}

// The model must show the single-event reality the rule exists for: a lone
// kill (or two) never reaches the bar.
func TestUpstreamKilledIsolatedKillsDoNotPage(t *testing.T) {
	from, to := ukAt("04", "10:00:00"), ukAt("04", "11:30:00")
	for n := 1; n <= 3; n++ {
		series := ukSeries(from, to, 100, ukSpread(ukAt("04", "10:10:10"), 4*time.Minute, n), false)
		if max, paged := ukReplay(series, from.Add(ukWindow), to); paged {
			t.Errorf("%d kill(s) paged (max increase %.3f)", n, max)
		}
	}
}
