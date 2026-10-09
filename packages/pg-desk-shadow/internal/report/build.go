package report

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Build loads every scratch directory, computes one PhaseReport per directory
// (phase-tagged) and, with more than one, the combined section.
func Build(dirs []string, p Params) (Report, error) {
	rep := Report{Schema: SchemaID}
	var asOf time.Time
	for _, d := range dirs {
		in, err := Load(d)
		if err != nil {
			return Report{}, err
		}
		pr := Compute(in, p)
		rep.Phases = append(rep.Phases, pr)
		if t := parseTS(pr.DataQuality.LastTick); t.After(asOf) {
			asOf = t
		}
	}
	sort.SliceStable(rep.Phases, func(i, j int) bool { return rep.Phases[i].Phase < rep.Phases[j].Phase })
	if !asOf.IsZero() {
		rep.AsOf = asOf.Format(time.RFC3339)
	}
	if len(rep.Phases) > 1 {
		rep.Combined = Combine(rep.Phases)
	}
	return rep, nil
}

// Combine merges phase reports: counts add, distributions are recomputed from
// the merged samples.
func Combine(phases []PhaseReport) *Combined {
	c := &Combined{ByClass: map[string]int{}}
	var td, hh, ch, dl []float64
	for _, p := range phases {
		c.Phases = append(c.Phases, p.Phase)
		c.LiveDetected += p.Live.InWindow
		c.Matched += p.Misses.Matched
		c.Missed += p.Misses.Missed
		c.LateDetected += p.Misses.LateDetected
		c.Unexplained += p.Misses.Unexplained
		for k, v := range p.Misses.ByClass {
			c.ByClass[k] += v
		}
		c.SweepHeadline += p.Sweep.Headline
		c.ShadowOnly += p.ShadowOnly.Total
		td = append(td, p.samp.tickDur...)
		hh = append(hh, p.samp.hydrationsHour...)
		ch = append(ch, p.samp.costHour...)
		dl = append(dl, p.samp.dVsLive...)
	}
	c.TickDurationS, c.HydrationsPerHour, c.CostPerHour, c.VsLiveEnqueue = MakeDist(td), MakeDist(hh), MakeDist(ch), MakeDist(dl)
	return c
}

// Write renders report.json and report.md into dir.
func Write(rep Report, dir string) (jsonPath, mdPath string, err error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", "", err
	}
	b, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return "", "", err
	}
	jsonPath, mdPath = filepath.Join(dir, "report.json"), filepath.Join(dir, "report.md")
	if err := os.WriteFile(jsonPath, append(b, '\n'), 0o600); err != nil {
		return "", "", err
	}
	if err := os.WriteFile(mdPath, []byte(Markdown(rep)), 0o600); err != nil {
		return "", "", err
	}
	return jsonPath, mdPath, nil
}

func pct(f float64) string { return fmt.Sprintf("%.1f percent", f*100) }

// pctOf is pct, or "n/a (<why>)" when the share has no denominator.
func pctOf(f float64, ok bool, why string) string {
	if !ok {
		return "n/a (" + why + ")"
	}
	return pct(f)
}

func dist(d Dist, unit string) string {
	if d.N == 0 {
		return "no samples"
	}
	return fmt.Sprintf("n=%d p50=%.1f%s p90=%.1f%s max=%.1f%s", d.N, d.P50, unit, d.P90, unit, d.Max, unit)
}

// Markdown renders the report. Every PR appears only as its HMAC label.
func Markdown(rep Report) string {
	var b strings.Builder
	w := func(f string, a ...any) { fmt.Fprintf(&b, f, a...) }
	w("# pg-desk shadow compare report\n\nSchema `%s`, data as of %s.\n\n", rep.Schema, rep.AsOf)
	for _, p := range rep.Phases {
		dq := p.DataQuality
		w("## Phase %s\n\n### Data quality\n\n", p.Phase)
		w("- Run window: %s to %s (%.2f days); T0 %s; seeded: %v; bd mode: %s.\n", dq.FirstTick, dq.LastTick, dq.ElapsedDays, dq.T0, dq.Seeded, dq.BDMode)
		w("- Ticks: %d executed, %d completed, %d failed, %d skipped for budget; slots %d total, %d eligible.\n", dq.ExecutedTicks, dq.CompletedTicks, dq.FailedTicks, dq.SkippedTicks, dq.SlotsTotal, dq.SlotsEligible)
		w("- Collector uptime: %s (spec definition: completed ticks over slots with the live router up and the machine awake; skipped_budget ticks count against it); raw: %s.\n", pct(dq.UptimeSpec), pct(dq.UptimeRaw))
		var reasons []string
		for r := range dq.Gaps {
			reasons = append(reasons, r)
		}
		sort.Strings(reasons)
		for _, r := range reasons {
			w("- Gap `%s`: %d, %.0f seconds.\n", r, dq.Gaps[r].Count, dq.Gaps[r].Seconds)
		}
		w("- Live router down (holes in the dispatch log): %.0f seconds.\n", dq.RouterDownSecs)
		w("- Log windows: router events %s; queue %s; run record %s; reset markers %v.\n", dq.RouterEventsWindow, dq.QueueWindow, dq.RunRecordWindow, dq.ResetMarkers)
		w("- Warm-up: %d ticks excluded from metrics (ends %s).\n", dq.WarmupTicks, dq.WarmupEnd)
		var days []string
		for d := range dq.BuildsByDay {
			days = append(days, d)
		}
		sort.Strings(days)
		for _, d := range days {
			w("- Tool builds on %s: %s.\n", d, strings.Join(dq.BuildsByDay[d], ", "))
		}
		bl := dq.Baseline
		w("- Baseline of the live sweep signals: %d sweep rows, content_hash_changed on %d (%s), %d anchors written %v. %s\n", bl.SweepRows, bl.HashChangedRows, pct(bl.HashChangedShare), bl.AnchorRows, bl.AnchorByCause, bl.HashStatement)
		for _, s := range dq.Statements {
			w("- %s\n", s)
		}
		w("\n### Stop criteria\n\n| Criterion | Need | Have | Met |\n| --- | --- | --- | --- |\n")
		for _, c := range p.Stop.Criteria {
			w("| %s | %s | %s | %v |\n", c.Name, c.Need, c.Have, c.Met)
		}
		w("\nRun complete: **%v**.\n\n", p.Stop.Complete)

		w("### (a) Feed misses\n\nLIVE-DETECTED in window: %d (excluded: %d warm-up, %d outside the run, %d without enqueued_at). Tolerance %s. Matched %d, missed %d; **unexplained: %d** (only unexplained counts as failure).\n\n", p.Live.InWindow, p.Live.ExcludedWarmup, p.Live.ExcludedOutside, p.Live.WithoutEnqueuedAt, p.Misses.Tolerance, p.Misses.Matched, p.Misses.Missed, p.Misses.Unexplained)
		w("| Class | Count |\n| --- | --- |\n")
		for _, c := range MissClasses {
			w("| %s | %d |\n", c, p.Misses.ByClass[c])
		}
		upEvents := p.Live.InWindow - p.Misses.ByClass[ClassCollectorDown]
		w("\nCoverage CEILING (informational, an upper bound; matched plus missed-but-detected-later, over LIVE-DETECTED in window): %s; while the collector was up (events not classed collector-down): %s. %d of the %d missed events were flagged by the shadow later (the first later shadow item for the same PR within the late window of %s, one item per missed event and never an item that matched a live event: an upper bound on the delay, not proof it is the same change); their delay is under (d).\n\n", pctOf(p.Misses.CoverageCeiling, p.Live.InWindow > 0, "no events"), pctOf(p.Misses.CoverageCeilingWhenUp, upEvents > 0, "no events outside collector-down"), p.Misses.LateDetected, p.Misses.Missed, p.Misses.LateWindow)
		for _, e := range p.Misses.Entries {
			late := ""
			if e.LateSeconds != nil {
				late = fmt.Sprintf(" (shadow detected it later, after %.0fs)", *e.LateSeconds)
			}
			w("- `%s` at %s: %s %s%s\n", e.PR, e.At, e.Class, e.Evidence, late)
		}
		w("\n### (b) SWEEP-CAUGHT\n\nHeadline (pr-content-change, conflict-flip): %d; other causes %v. Shadow-detected %d; caught by a local/sweep-origin item %d (counted separately); shadow-missed %d (of which declared blind spot %d). The live sweep caught what the live feed missed on %d rows (rate %s). This feeds pg2-xg2k8 (sweep capacity).\n\n", p.Sweep.Headline, p.Sweep.OtherCauses, p.Sweep.ShadowDetected, p.Sweep.CaughtLocalSweep, p.Sweep.ShadowMissed, p.Sweep.MissedBlindSpot, p.Sweep.FeedMissed, pct(p.Sweep.FeedMissedRate))
		for _, e := range p.Sweep.Entries {
			w("- `%s` %s (%s): %s; live feed missed: %v\n", e.PR, e.End, e.Cause, e.Result, e.FeedMissed)
		}
		so := p.ShadowOnly
		w("\n### (c) Shadow-only detections\n\n%d shadow-only (origin pg-connector, no live pr.changed within tolerance), %.2f per hour; %d first-observation reconciles counted apart; %d with no projection field change. By kind %v; by field %v. Other-origin items %v (of which a real field change %v).\n\n", so.Total, so.NoiseRatePerHour, so.FirstObservation, so.NoFieldChange, so.ByKind, so.ByField, so.OtherOrigins, so.OtherReal)
		w("### (d) Detection delay\n\nNegative means the shadow was first.\n\n- shadow minus live enqueue: %s\n- shadow minus the PR's own updated time: %s\n- live enqueue minus the PR's own updated time: %s\n- missed events the shadow flagged later (shadow minus live enqueue, an upper bound): %s\n\n", dist(p.Delay.VsLiveEnqueue, "s"), dist(p.Delay.ShadowFromUpdate, "s"), dist(p.Delay.LiveFromUpdate, "s"), dist(p.Delay.MissedThenDetected, "s"))
		w("### (e) Cost\n\n- Tick duration: %s; %d ticks over 60 s.\n- Hydrations per hour: %s.\n- GraphQL points per hour (shadow, lower bound): %s.\n- Hours with the shared token below the 1,000 reserve: %d. %s\n\n", dist(p.Cost.TickDurationS, "s"), p.Cost.TicksOver60s, dist(p.Cost.HydrationsPerHour, ""), dist(p.Cost.CostPerHour, ""), p.Cost.HoursBelowReserve, p.Cost.Statement)
		w("| Hour (UTC) | Ticks | Hydrations | Points (lower bound) | Token remaining at hour end | Min remaining |\n| --- | --- | --- | --- | --- | --- |\n")
		for _, h := range p.Cost.Hours {
			rem := "unknown"
			if h.RemainingKnown {
				rem = fmt.Sprint(h.RemainingAtEnd)
			}
			w("| %s | %d | %d | %d | %s | %d |\n", h.Hour, h.Ticks, h.Hydrations, h.GraphQLCost, rem, h.MinRemaining)
		}
		w("\n### (f) Ticks skipped for budget\n\n%d.\n\n", p.SkippedBudgetTicks)
		if p.HeadSHA != nil {
			w("### (g) Head SHA disagreement (optional)\n\nCompared %d, disagreeing %d, only in the shadow store %d, only in the live store %d.\n\n", p.HeadSHA.Compared, p.HeadSHA.Disagreeing, p.HeadSHA.OnlyShadow, p.HeadSHA.OnlyLive)
		}
	}
	if c := rep.Combined; c != nil {
		w("## Combined (%s)\n\n- LIVE-DETECTED in window %d, matched %d, missed %d (of which flagged later %d), unexplained %d; misses by class %v.\n- SWEEP-CAUGHT headline %d; shadow-only %d.\n- Tick duration: %s; hydrations per hour: %s; points per hour: %s; shadow minus live enqueue: %s.\n", strings.Join(c.Phases, " + "), c.LiveDetected, c.Matched, c.Missed, c.LateDetected, c.Unexplained, c.ByClass, c.SweepHeadline, c.ShadowOnly, dist(c.TickDurationS, "s"), dist(c.HydrationsPerHour, ""), dist(c.CostPerHour, ""), dist(c.VsLiveEnqueue, "s"))
	}
	return b.String()
}
