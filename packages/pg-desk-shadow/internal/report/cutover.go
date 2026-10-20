package report

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/phillipgreenii/pg-desk-shadow/internal/schema"
)

// The four default cutover criteria, in the fixed order they are reported.
const (
	CutoverMisses = "zero unexplained misses"
	CutoverDelay  = "detection delay p90 no worse than live"
	CutoverCost   = "GraphQL points per hour inside the cap"
	CutoverUptime = "daemon uptime at least 95 percent"
)

// Recommendation verdicts: keep-cap, raise-cap-to-<N> and relax-max-age-2h.
const (
	VerdictKeepCap  = "keep-cap"
	VerdictRaisePfx = "raise-cap-to-"
	VerdictRelax    = "relax-max-age-2h"
)

const (
	// defaultDaemonCap is the shadow run's default cap, in points per hour.
	defaultDaemonCap = 1500
	// cutoverCap is the ceiling no recommended cap may exceed.
	cutoverCap = 4000
	// capStep is the step of a raised cap.
	capStep = 100
	// backgroundShareTenths is the background share of the cap (0.9) in tenths,
	// so every threshold is compared in integer arithmetic.
	backgroundShareTenths = 9
)

// CutoverCriterion is one default cutover criterion with its verdict.
type CutoverCriterion struct {
	Name string `json:"name"`
	// Need is the definition used, printed beside the figure.
	Need     string `json:"need"`
	Have     string `json:"have"`
	Met      bool   `json:"met"`
	Evidence string `json:"evidence"`
}

// Recommendation is the measured budget recommendation: one verdict and the
// full arithmetic that produced it.
type Recommendation struct {
	Verdict string `json:"verdict"`
	Method  string `json:"method"`
}

// FollowUp is a proposed bead for one unmet criterion; the report only lists it.
type FollowUp struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

// Cutover is the cutover section of a phase report.
type Cutover struct {
	Criteria       []CutoverCriterion `json:"criteria"`
	Recommendation Recommendation     `json:"recommendation"`
	FollowUps      []FollowUp         `json:"follow_ups"`
}

// cutoverInput is what the four criteria and the recommendation read.
type cutoverInput struct {
	liveInWindow int
	unexplained  int
	shadowDelay  Dist
	liveDelay    Dist
	// hours is every clock hour of the cost section, zero-cost hours included.
	hours []Hour
	// cap is the manifest's daemon_graphql_points_per_hour; 0 means unknown.
	cap        int
	upSlots    int
	totalSlots int
}

// daemonServing reports whether a daemon sample shows the daemon serving:
// healthy, or degraded (still serving). No sample, or down, is not serving.
func daemonServing(d *schema.DaemonSample) bool {
	return d != nil && (d.State == schema.DaemonHealthy || d.State == schema.DaemonDegraded)
}

// evalCutover evaluates the four criteria in their fixed order, the budget
// recommendation, and one follow-up per unmet criterion.
func evalCutover(in cutoverInput) Cutover {
	type result struct {
		c   CutoverCriterion
		gap string
	}
	var res []result
	{
		c := CutoverCriterion{Name: CutoverMisses, Need: "Misses.Unexplained == 0 over the LIVE-DETECTED events in the run window (only the unexplained class counts as a miss)"}
		var gap string
		if in.liveInWindow == 0 {
			c.Have = "not evaluable"
			c.Evidence = "cannot be evaluated: no LIVE-DETECTED events in the run window"
			gap = "no LIVE-DETECTED events in the window, so no miss could be judged"
		} else {
			c.Have = fmt.Sprintf("%d unexplained of %d LIVE-DETECTED in window", in.unexplained, in.liveInWindow)
			c.Met = in.unexplained == 0
			c.Evidence = fmt.Sprintf("%d unexplained misses, need 0; %d LIVE-DETECTED events in the window", in.unexplained, in.liveInWindow)
			gap = fmt.Sprintf("%d unexplained misses over the 0 allowed", in.unexplained)
		}
		res = append(res, result{c, gap})
	}
	{
		c := CutoverCriterion{Name: CutoverDelay, Need: "p90 of the shadow's detection delay from the PR's own update time <= p90 of the live flow's (nearest-rank p90)"}
		var gap string
		sd, ld := in.shadowDelay, in.liveDelay
		switch {
		case sd.N == 0 || ld.N == 0:
			c.Have = "not evaluable"
			c.Evidence = fmt.Sprintf("cannot be evaluated: shadow delay samples n=%d, live delay samples n=%d (both need at least 1)", sd.N, ld.N)
			gap = "a delay distribution has no samples, so the p90s could not be compared"
		default:
			c.Have = fmt.Sprintf("shadow p90 %.1f s (n=%d) vs live p90 %.1f s (n=%d)", sd.P90, sd.N, ld.P90, ld.N)
			c.Met = sd.P90 <= ld.P90
			cmp := "<="
			if !c.Met {
				cmp = ">"
			}
			c.Evidence = fmt.Sprintf("shadow p90 %.1f s %s live p90 %.1f s", sd.P90, cmp, ld.P90)
			gap = fmt.Sprintf("shadow p90 %.1f s exceeds live p90 %.1f s by %.1f s (%.1f - %.1f)", sd.P90, ld.P90, sd.P90-ld.P90, sd.P90, ld.P90)
		}
		res = append(res, result{c, gap})
	}
	{
		c := CutoverCriterion{Name: CutoverCost, Need: "no clock hour of the run records more GraphQL cost than the run's cap (hourly buckets of the cost section, points per hour, strictly greater fails)"}
		var gap string
		switch {
		case in.cap <= 0:
			c.Have = "cap unknown"
			c.Evidence = "cap unknown: the manifest has no daemon_graphql_points_per_hour (an older scratch directory); the cap is not defaulted here"
			gap = "cap unknown: the manifest has no daemon_graphql_points_per_hour"
		case len(in.hours) == 0:
			c.Have = "not evaluable"
			c.Evidence = "cannot be evaluated: no hourly cost samples in the run"
			gap = "no hourly cost samples, so no hour could be compared with the cap"
		default:
			peak, over := in.hours[0], 0
			for _, h := range in.hours {
				if h.GraphQLCost > peak.GraphQLCost {
					peak = h
				}
				if h.GraphQLCost > in.cap {
					over++
				}
			}
			c.Met = over == 0
			c.Have = fmt.Sprintf("highest hour %d points (%s) vs cap %d; %d of %d hours over the cap", peak.GraphQLCost, peak.Hour, in.cap, over, len(in.hours))
			cmp := "<="
			if !c.Met {
				cmp = ">"
			}
			c.Evidence = fmt.Sprintf("peak %d points %s cap %d; hours over the cap %d of %d", peak.GraphQLCost, cmp, in.cap, over, len(in.hours))
			gap = fmt.Sprintf("%d of %d hours over the cap; the highest, %s, recorded %d points, %d over the cap of %d (%d - %d)", over, len(in.hours), peak.Hour, peak.GraphQLCost, peak.GraphQLCost-in.cap, in.cap, peak.GraphQLCost, in.cap)
		}
		res = append(res, result{c, gap})
	}
	{
		c := CutoverCriterion{Name: CutoverUptime, Need: "up_slots * 100 >= 95 * SlotsTotal (integer arithmetic); a slot is up when a tick row of that slot carries a daemon sample that is healthy or degraded, and a slot with no sample is not up"}
		var gap string
		if in.totalSlots == 0 {
			c.Have = "no slots"
			c.Evidence = "cannot be evaluated: no slots"
			gap = "no slots in the run, so uptime could not be measured"
		} else {
			lhs, rhs := in.upSlots*100, 95*in.totalSlots
			c.Met = lhs >= rhs
			c.Have = fmt.Sprintf("%d of %d slots up (%d / %d = %.1f percent)", in.upSlots, in.totalSlots, in.upSlots, in.totalSlots, float64(in.upSlots)*100/float64(in.totalSlots))
			cmp := ">="
			if !c.Met {
				cmp = "<"
			}
			c.Evidence = fmt.Sprintf("%d x 100 = %d %s 95 x %d = %d", in.upSlots, lhs, cmp, in.totalSlots, rhs)
			minUp := (rhs + 99) / 100
			gap = fmt.Sprintf("%d slots up of %d, %d short of the %d needed (ceil(95 x %d / 100) = %d)", in.upSlots, in.totalSlots, minUp-in.upSlots, minUp, in.totalSlots, minUp)
		}
		res = append(res, result{c, gap})
	}

	out := Cutover{FollowUps: []FollowUp{}}
	for _, r := range res {
		out.Criteria = append(out.Criteria, r.c)
		if r.c.Met {
			continue
		}
		out.FollowUps = append(out.FollowUps, FollowUp{
			Title: "Shadow cutover criterion not met: " + r.c.Name,
			Body:  fmt.Sprintf("Criterion: %s.\nDefinition: %s.\nMeasured: %s.\nGap: %s.\nEvidence: %s.", r.c.Name, r.c.Need, r.c.Have, r.gap, r.c.Evidence),
		})
	}
	out.Recommendation = recommend(in.hours, in.cap)
	return out
}

// tenths renders n tenths as a decimal without a trailing zero (13500 -> 1350).
func tenths(n int) string {
	return strconv.FormatFloat(float64(n)/10, 'f', -1, 64)
}

// recommend derives the measured budget recommendation from the hourly GraphQL
// cost samples (every clock hour, zero-cost hours included) and the run's cap
// (0 = unknown, the 1,500 default is assumed and said). Thresholds are compared
// as integers: p90 x 10 <= cap x 9.
func recommend(hours []Hour, cap int) Recommendation {
	var b strings.Builder
	w := func(f string, a ...any) { fmt.Fprintf(&b, f, a...) }
	samples := make([]float64, 0, len(hours))
	vals := make([]string, 0, len(hours))
	for _, h := range hours {
		samples = append(samples, float64(h.GraphQLCost))
		vals = append(vals, strconv.Itoa(h.GraphQLCost))
	}
	d := MakeDist(samples)
	p90, max := int(d.P90), int(d.Max)
	capNote := "the run's cap"
	if cap <= 0 {
		cap, capNote = defaultDaemonCap, "the manifest has no daemon_graphql_points_per_hour, so the 1,500 default is assumed"
	}
	w("Hourly GraphQL cost samples (n=%d, every clock hour of the run including zero-cost hours, points): [%s]. ", d.N, strings.Join(vals, " "))
	w("p90 = %d (nearest rank), maximum = %d. Cap C = %d points per hour (%s); background share = 0.9. ", p90, max, cap, capNote)
	keep := cap * backgroundShareTenths
	w("Keep threshold = C x 0.9 = %d x 0.9 = %s points. ", cap, tenths(keep))
	if d.N == 0 {
		w("No hourly cost samples: nothing was measured, so there is no evidence for a change: keep the cap.")
		return Recommendation{Verdict: VerdictKeepCap, Method: b.String()}
	}
	if p90*10 <= keep {
		w("p90 %d <= %s: within the background budget: keep the cap.", p90, tenths(keep))
		return Recommendation{Verdict: VerdictKeepCap, Method: b.String()}
	}
	w("p90 %d > %s: over the background budget at cap %d. ", p90, tenths(keep), cap)
	// At the default cap only the documented single step to 1,600 is considered;
	// any other cap tries every step of 100 up to the 4,000 ceiling.
	last := cutoverCap
	if cap == defaultDaemonCap {
		last = defaultDaemonCap + capStep
		w("At the default cap of %d the only cap step considered is %d (%d x 0.9 = %s points). ", cap, last, last, tenths(last*backgroundShareTenths))
	}
	for c := cap + capStep; c <= last; c += capStep {
		if p90*10 <= c*backgroundShareTenths {
			w("Smallest cap in steps of %d above %d whose 0.9 share covers the p90: %d x 0.9 = %s >= %d: raise the cap to %d.", capStep, cap, c, tenths(c*backgroundShareTenths), p90, c)
			return Recommendation{Verdict: VerdictRaisePfx + strconv.Itoa(c), Method: b.String()}
		}
	}
	w("No cap step up to %d covers it (%d x 0.9 = %s < %d): relax both detail_max_age and conversation_max_age to 2 h, which cuts the hard-age re-pulls to a quarter.", last, last, tenths(last*backgroundShareTenths), p90)
	return Recommendation{Verdict: VerdictRelax, Method: b.String()}
}
