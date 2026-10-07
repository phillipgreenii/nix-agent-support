package report

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/phillipgreenii/pg-desk-shadow/internal/schema"
	"github.com/phillipgreenii/pg-desk-shadow/internal/sqlite"
)

// BlindFields are the projection fields the list fingerprint intentionally
// cannot see (spec: merge-state status, CI detail).
var BlindFields = map[string]bool{"merge_state_status": true, "mergeable": true, "checks_rollup": true}

// HeadlineCauses are the SWEEP-CAUGHT headline anchor causes.
var HeadlineCauses = map[string]bool{"pr-content-change": true, "conflict-flip": true}

type interval struct {
	from, to time.Time
	reason   string
}

func (i interval) overlaps(a, b time.Time) bool { return i.from.Before(b) && i.to.After(a) }

type shadowItem struct {
	id     string
	at     time.Time
	kinds  []string
	origin string
	fields []string
	updAt  time.Time
	tick   time.Time
}

func sortedUnique(v []string) []string {
	m := map[string]bool{}
	for _, s := range v {
		m[s] = true
	}
	var out []string
	for s := range m {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// dedupeTicks keeps the LAST tick row per (phase, slot) (a recovered row, or a
// duplicate append after a crash, collapses to one).
func dedupeTicks(rows []schema.Row) []schema.Row {
	idx := map[string]int{}
	var out []schema.Row
	for _, r := range rows {
		if r.Kind != schema.KindTick {
			continue
		}
		k := r.Phase + "|" + r.Slot
		if i, ok := idx[k]; ok {
			out[i] = r
			continue
		}
		idx[k] = len(out)
		out = append(out, r)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Slot < out[j].Slot })
	return out
}

// Compute builds one phase's report from one scratch directory.
func Compute(in Input, p Params) PhaseReport {
	lab := Labeler{Key: in.Key}
	m := in.Manifest
	pr := PhaseReport{Phase: m.Phase}
	dq := &pr.DataQuality
	dq.Phase, dq.Seeded, dq.T0, dq.BDMode = m.Phase, m.Seeded, m.CreatedAt, m.BDMode
	dq.Gaps = map[string]GapStat{}
	dq.ResetMarkers = in.Markers
	dq.BuildsByDay = map[string][]string{}
	t0 := parseTS(m.CreatedAt)

	ticks := dedupeTicks(in.Rows)
	var gaps []interval
	for _, r := range in.Rows {
		if r.Kind != schema.KindGap {
			continue
		}
		from, to := parseTS(r.GapFrom), parseTS(r.GapTo)
		g := dq.Gaps[r.Reason]
		g.Count++
		if !from.IsZero() && to.After(from) {
			g.Seconds += to.Sub(from).Seconds()
			gaps = append(gaps, interval{from, to, r.Reason})
		}
		dq.Gaps[r.Reason] = g
	}

	// Tick bookkeeping.
	var firstStart, lastEnd, warmupEnd time.Time
	var done, executed []schema.Row
	var items []shadowItem
	for _, t := range ticks {
		ts, te := parseTS(t.TickStart), parseTS(t.TickEnd)
		if !ts.IsZero() && (firstStart.IsZero() || ts.Before(firstStart)) {
			firstStart = ts
		}
		if te.After(lastEnd) {
			lastEnd = te
		}
		if day := ts.UTC().Format("2006-01-02"); day != "0001-01-01" {
			for _, b := range t.Builds {
				dq.BuildsByDay[day] = append(dq.BuildsByDay[day], b)
			}
		}
		if t.Warmup {
			dq.WarmupTicks++
			if te.After(warmupEnd) {
				warmupEnd = te
			}
		}
		if t.Status == schema.StatusSkippedBudget {
			dq.SkippedTicks++
			continue
		}
		executed = append(executed, t)
		switch t.Status {
		case schema.StatusOK, schema.StatusPartial, schema.StatusRecovered:
			done = append(done, t)
			dq.CompletedTicks++
		default:
			dq.FailedTicks++
		}
		for _, it := range t.Items {
			at := parseTS(it.At)
			if at.IsZero() {
				at = te
			}
			items = append(items, shadowItem{id: it.EntityID, at: at, kinds: it.Kinds, origin: it.Origin, fields: it.Fields, updAt: parseTS(it.UpdatedAt), tick: te})
		}
	}
	for d, v := range dq.BuildsByDay {
		dq.BuildsByDay[d] = sortedUnique(v)
	}
	dq.ExecutedTicks = len(executed)
	if warmupEnd.IsZero() {
		warmupEnd = t0
	} else {
		dq.WarmupEnd = schema.Format(warmupEnd)
	}
	if firstStart.IsZero() {
		firstStart = t0
	}
	dq.FirstTick, dq.LastTick = schema.Format(firstStart), schema.Format(lastEnd)
	dq.ElapsedDays = lastEnd.Sub(firstStart).Hours() / 24
	if dq.ElapsedDays < 0 {
		dq.ElapsedDays = 0
	}

	// Router-down intervals from holes in the live dispatch log.
	var routerDown []interval
	if len(in.Events) > 1 {
		for i := 1; i < len(in.Events); i++ {
			a, b := in.Events[i-1].Time, in.Events[i].Time
			if b.Sub(a) > p.RouterDownGap && b.After(firstStart) && a.Before(lastEnd) {
				routerDown = append(routerDown, interval{a, b, "router-down"})
				dq.RouterDownSecs += b.Sub(a).Seconds()
			}
		}
	}
	dq.RouterEventsWindow = window(evTimes(in.Events))
	dq.QueueWindow = window(qTimes(in.Queue))
	dq.RunRecordWindow = window(runTimes(in.Runs))
	if !in.HaveRunRecord {
		dq.Statements = append(dq.Statements, "The run-record copy is ABSENT: the sweep metric (b) and the sweep baseline are unavailable.")
	}
	if len(in.Events) == 0 {
		dq.Statements = append(dq.Statements, "The router events copy is EMPTY: no LIVE-DETECTED events and router uptime cannot be judged.")
	}

	// Uptime: slots between the first and the last tick.
	if len(ticks) > 0 {
		var slots []time.Time
		for s := parseTS(ticks[0].Slot); !s.IsZero() && !s.After(parseTS(ticks[len(ticks)-1].Slot)); s = s.Add(p.SlotPeriod) {
			slots = append(slots, s)
		}
		doneAt := map[time.Time]bool{}
		for _, t := range done {
			doneAt[parseTS(t.Slot)] = true
		}
		compl := 0
		for _, s := range slots {
			dq.SlotsTotal++
			excluded := false
			for _, g := range gaps {
				if (g.reason == schema.GapSleep || g.reason == schema.GapRestart || g.reason == schema.GapUnfinish) && g.overlaps(s, s.Add(time.Second)) {
					excluded = true
				}
			}
			for _, g := range routerDown {
				if g.overlaps(s, s.Add(time.Second)) {
					excluded = true
				}
			}
			if doneAt[s] {
				compl++
			}
			if !excluded {
				dq.SlotsEligible++
			}
		}
		_ = compl
		okEligible := 0
		for _, s := range slots {
			if !doneAt[s] {
				continue
			}
			okEligible++
		}
		if dq.SlotsTotal > 0 {
			dq.UptimeRaw = float64(okEligible) / float64(dq.SlotsTotal)
		}
		if dq.SlotsEligible > 0 {
			u := float64(okEligible) / float64(dq.SlotsEligible)
			if u > 1 {
				u = 1
			}
			dq.UptimeSpec = u
		}
	}

	// Baselines of the live sweep signals.
	base := &dq.Baseline
	base.RunRecordPresent = in.HaveRunRecord
	base.AnchorByCause = map[string]int{}
	var sweepRows []RunRow
	for _, r := range in.Runs {
		if r.Change == "sweep" && !r.End.Before(t0) {
			sweepRows = append(sweepRows, r)
		}
	}
	base.SweepRows = len(sweepRows)
	for _, r := range sweepRows {
		if r.HashChanged {
			base.HashChangedRows++
		}
		if r.Anchor {
			base.AnchorRows++
			base.AnchorByCause[r.Cause]++
		}
	}
	if base.SweepRows > 0 {
		base.HashChangedShare = float64(base.HashChangedRows) / float64(base.SweepRows)
		base.WindowFrom, base.WindowTo = schema.Format(sweepRows[0].End), schema.Format(sweepRows[len(sweepRows)-1].End)
	}
	base.HashSignalUsable = base.SweepRows == 0 || base.HashChangedShare <= p.HashUnusable
	if base.SweepRows > 0 && !base.HashSignalUsable {
		base.HashStatement = fmt.Sprintf("content_hash_changed is true on %.1f percent of live sweep rows (over %.0f percent): the hash signal is UNUSABLE and is not used anywhere in this report.", base.HashChangedShare*100, p.HashUnusable*100)
	} else if base.SweepRows > 0 {
		base.HashStatement = fmt.Sprintf("content_hash_changed is true on %.1f percent of live sweep rows.", base.HashChangedShare*100)
	}
	if m.Seeded {
		dq.Statements = append(dq.Statements,
			"The REMOTE re-hydration tier is effectively OFF in this phase because hydrated_at is seeded on the scratch store and sweep.max_age is "+m.SweepMaxAge+"; the LOCAL reconcile tier (sweep.reconcile_age "+m.ReconcileAge+") stays ON.",
			"The warm-up seeding is a warm-up simplification and is NOT the treatment bead pg2-5rb3t weighs; phase B measures that.")
	} else {
		dq.Statements = append(dq.Statements, "No seeding: the default sweep and hydration tiers run on the migrated copy (phase B style).")
	}
	dq.Params = map[string]any{
		"live_period_s": p.LivePeriod.Seconds(), "slot_period_s": p.SlotPeriod.Seconds(), "sweep_period_s": p.SweepPeriod.Seconds(),
		"first_hour_s": p.FirstHour.Seconds(), "router_down_gap_s": p.RouterDownGap.Seconds(),
	}

	// Tolerance T: live period + slot + measured tick duration p90.
	var durs []float64
	for _, t := range executed {
		if !t.Warmup {
			durs = append(durs, float64(t.DurationMS)/1000)
		}
	}
	pr.samp.tickDur = durs
	T := p.LivePeriod + p.SlotPeriod + time.Duration(MakeDist(durs).P90*float64(time.Second))
	pr.Misses.Tolerance = T.String()

	// Shadow items by id (pg-connector origin).
	byID := map[string][]shadowItem{}
	var others []shadowItem
	for _, it := range items {
		if it.origin == "pg-connector" {
			byID[it.id] = append(byID[it.id], it)
		} else {
			others = append(others, it)
		}
	}
	for id := range byID {
		sort.SliceStable(byID[id], func(i, j int) bool { return byID[id][i].at.Before(byID[id][j].at) })
	}
	nearest := func(id string, e time.Time, win time.Duration) (shadowItem, bool) {
		var best shadowItem
		found := false
		for _, it := range byID[id] {
			d := it.at.Sub(e)
			if d < 0 {
				d = -d
			}
			if d <= win && (!found || d < absDur(best.at.Sub(e))) {
				best, found = it, true
			}
		}
		return best, found
	}

	// LIVE-DETECTED.
	type liveEv struct {
		id string
		at time.Time
		d  Dispatch
	}
	seenEv := map[string]bool{}
	var live []liveEv
	var waits []float64
	for _, d := range in.Events {
		if d.EventType != "pr.changed" {
			continue
		}
		if d.EnqueuedAt.IsZero() {
			pr.Live.WithoutEnqueuedAt++
			continue
		}
		k := d.ID + "|" + d.EnqueuedAt.Format(time.RFC3339Nano)
		if seenEv[k] {
			continue
		}
		seenEv[k] = true
		live = append(live, liveEv{d.ID, d.EnqueuedAt, d})
		if !d.StartedAt.IsZero() {
			waits = append(waits, d.StartedAt.Sub(d.EnqueuedAt).Seconds())
		}
	}
	pr.Live.Total = len(live)
	pr.Live.MedianWaitSeconds = MakeDist(waits).P50
	startOK := firstStart
	if warmupEnd.After(startOK) {
		startOK = warmupEnd
	}
	endOK := lastEnd.Add(-T)
	matchedItems := map[string]bool{}
	pr.Misses.ByClass = map[string]int{}
	for _, c := range MissClasses {
		pr.Misses.ByClass[c] = 0
	}
	for _, e := range live {
		switch {
		case e.at.Before(firstStart) || e.at.After(lastEnd):
			pr.Live.ExcludedOutside++
			continue
		case e.at.Before(warmupEnd):
			pr.Live.ExcludedWarmup++
			continue
		case e.at.After(endOK):
			pr.Live.ExcludedOutside++
			continue
		}
		pr.Live.InWindow++
		if it, ok := nearest(e.id, e.at, T); ok {
			pr.Misses.Matched++
			matchedItems[it.id+"|"+it.at.Format(time.RFC3339Nano)] = true
			pr.samp.dVsLive = append(pr.samp.dVsLive, it.at.Sub(e.at).Seconds())
			if !it.updAt.IsZero() {
				pr.samp.dShadowUpd = append(pr.samp.dShadowUpd, it.at.Sub(it.updAt).Seconds())
				pr.samp.dLiveUpd = append(pr.samp.dLiveUpd, e.at.Sub(it.updAt).Seconds())
			}
			continue
		}
		class, ev := classifyMiss(e.id, e.at, T, firstStart, t0, p, in, ticks, gaps, routerDown, others, byID)
		pr.Misses.Missed++
		pr.Misses.ByClass[class]++
		pr.Misses.Entries = append(pr.Misses.Entries, MissEntry{PR: lab.Label(e.id), At: schema.Format(e.at), Class: class, Evidence: ev})
	}
	pr.Misses.Unexplained = pr.Misses.ByClass[ClassUnexplained]

	// (b) SWEEP-CAUGHT.
	pr.Sweep.OtherCauses = map[string]int{}
	liveByID := map[string][]time.Time{}
	for _, e := range live {
		liveByID[e.id] = append(liveByID[e.id], e.at)
	}
	for _, r := range sweepRows {
		if !r.Anchor || r.End.Before(warmupEnd) || r.End.Before(firstStart) || r.End.After(lastEnd) {
			continue
		}
		if !HeadlineCauses[r.Cause] {
			pr.Sweep.OtherCauses[r.Cause]++
			continue
		}
		pr.Sweep.Headline++
		win := p.SweepPeriod + T
		result := "shadow-missed"
		if _, ok := nearestIn(byID[r.EntityID], r.End, win, true); ok {
			result = "shadow-detected"
			pr.Sweep.ShadowDetected++
		} else if hasOther(others, r.EntityID, r.End, win) {
			result = "caught-by-local-or-sweep-origin"
			pr.Sweep.CaughtLocalSweep++
		} else {
			pr.Sweep.ShadowMissed++
			if r.Cause == "conflict-flip" {
				pr.Sweep.MissedBlindSpot++
				result = "shadow-missed (declared blind spot)"
			}
		}
		feedMissed := true
		for _, e := range liveByID[r.EntityID] {
			if e.After(r.End.Add(-win)) && !e.After(r.End) {
				feedMissed = false
			}
		}
		if feedMissed {
			pr.Sweep.FeedMissed++
		}
		pr.Sweep.Entries = append(pr.Sweep.Entries, SweepEntry{PR: lab.Label(r.EntityID), End: schema.Format(r.End), Cause: r.Cause, Result: result, FeedMissed: feedMissed})
	}
	if pr.Sweep.Headline > 0 {
		pr.Sweep.FeedMissedRate = float64(pr.Sweep.FeedMissed) / float64(pr.Sweep.Headline)
	}

	// (c) shadow-only detections.
	so := &pr.ShadowOnly
	so.ByKind, so.ByField = map[string]int{}, map[string]int{}
	so.OtherOrigins, so.OtherReal = map[string]int{}, map[string]int{}
	for _, it := range items {
		if it.at.Before(warmupEnd) {
			continue
		}
		if it.origin != "pg-connector" {
			so.OtherOrigins[it.origin]++
			if len(it.fields) > 0 {
				so.OtherReal[it.origin]++
			}
			continue
		}
		if matchedItems[it.id+"|"+it.at.Format(time.RFC3339Nano)] {
			continue
		}
		if len(it.kinds) == 1 && it.kinds[0] == "reconcile" {
			so.FirstObservation++
			continue
		}
		// Within tolerance of a live event for the id: not shadow-only.
		near := false
		for _, e := range liveByID[it.id] {
			if absDur(it.at.Sub(e)) <= T {
				near = true
			}
		}
		if near {
			continue
		}
		so.Total++
		for _, k := range it.kinds {
			so.ByKind[k]++
		}
		if len(it.fields) == 0 {
			so.NoFieldChange++
		}
		for _, f := range it.fields {
			so.ByField[f]++
		}
	}
	if h := lastEnd.Sub(startOK).Hours(); h > 0 {
		so.NoiseRatePerHour = float64(so.Total) / h
	}

	// (d) delay.
	pr.Delay = Delay{VsLiveEnqueue: MakeDist(pr.samp.dVsLive), ShadowFromUpdate: MakeDist(pr.samp.dShadowUpd), LiveFromUpdate: MakeDist(pr.samp.dLiveUpd)}

	// (e) cost.
	computeCost(&pr, executed, warmupEnd)
	pr.SkippedBudgetTicks = dq.SkippedTicks

	// (g) optional head SHA.
	if p.LiveStore != "" {
		pr.HeadSHA = headSHA(in, p.LiveStore)
	}

	// Stop criteria.
	weekdays := countWeekdays(done, p)
	live100 := pr.Live.InWindow
	days := dq.ElapsedDays
	sweepOrDays := pr.Sweep.Headline >= p.MinSweepCaught || days >= float64(p.MaxDays)
	pr.Stop.Criteria = []Criterion{
		{Name: "weekdays of ticks", Need: "at least 2", Have: fmt.Sprint(weekdays), Met: weekdays >= 2},
		{Name: "LIVE-DETECTED pr.changed events", Need: fmt.Sprintf("at least %d", p.MinLive), Have: fmt.Sprint(live100), Met: live100 >= p.MinLive},
		{Name: "collector uptime", Need: fmt.Sprintf("at least %.0f percent", p.UptimeMin*100), Have: fmt.Sprintf("%.1f percent", dq.UptimeSpec*100), Met: dq.UptimeSpec >= p.UptimeMin},
		{Name: "SWEEP-CAUGHT headline count or elapsed days", Need: fmt.Sprintf("at least %d, or %d days elapsed", p.MinSweepCaught, p.MaxDays), Have: fmt.Sprintf("%d caught, %.1f days", pr.Sweep.Headline, days), Met: sweepOrDays},
	}
	pr.Stop.Complete = true
	for _, c := range pr.Stop.Criteria {
		pr.Stop.Complete = pr.Stop.Complete && c.Met
	}
	return pr
}

func absDur(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

func nearestIn(items []shadowItem, at time.Time, win time.Duration, any bool) (shadowItem, bool) {
	for _, it := range items {
		if absDur(it.at.Sub(at)) <= win {
			return it, true
		}
	}
	return shadowItem{}, false
}

func hasOther(others []shadowItem, id string, at time.Time, win time.Duration) bool {
	for _, it := range others {
		if it.id == id && absDur(it.at.Sub(at)) <= win {
			return true
		}
	}
	return false
}

func evTimes(v []Dispatch) []time.Time {
	var o []time.Time
	for _, d := range v {
		o = append(o, d.Time)
	}
	return o
}

func qTimes(v []QueueRow) []time.Time {
	var o []time.Time
	for _, d := range v {
		o = append(o, d.At)
	}
	return o
}

func runTimes(v []RunRow) []time.Time {
	var o []time.Time
	for _, d := range v {
		o = append(o, d.End)
	}
	return o
}

func window(ts []time.Time) string {
	var lo, hi time.Time
	for _, t := range ts {
		if t.IsZero() {
			continue
		}
		if lo.IsZero() || t.Before(lo) {
			lo = t
		}
		if t.After(hi) {
			hi = t
		}
	}
	if lo.IsZero() {
		return "empty"
	}
	return schema.Format(lo) + " to " + schema.Format(hi)
}

func classifyMiss(id string, e time.Time, T time.Duration, firstStart, t0 time.Time, p Params, in Input, ticks []schema.Row, gaps, routerDown []interval, others []shadowItem, byID map[string][]shadowItem) (string, string) {
	lo, hi := e.Add(-T), e.Add(T)
	// collector-down: a gap overlaps, or no completed tick sits in [e, e+T].
	for _, g := range gaps {
		if g.overlaps(lo, hi) {
			return ClassCollectorDown, "gap: " + g.reason
		}
	}
	haveTick := false
	for _, t := range ticks {
		s := parseTS(t.Slot)
		if !s.Before(e) && !s.After(hi) && t.Status != schema.StatusSkippedBudget {
			haveTick = true
		}
	}
	if !haveTick {
		allSkipped := false
		for _, t := range ticks {
			s := parseTS(t.Slot)
			if !s.Before(lo) && !s.After(hi) && t.Status == schema.StatusSkippedBudget {
				allSkipped = true
			}
		}
		if !allSkipped {
			return ClassCollectorDown, "no tick in the window"
		}
	}
	// copy-staleness.
	if e.Sub(t0) <= p.FirstHour {
		return ClassCopyStaleness, "event in the first hour after T0"
	}
	if len(in.SeedIDs) > 0 && !in.SeedIDs[id] && len(byID[id]) == 0 {
		return ClassCopyStaleness, "PR was not in the seeded set"
	}
	// skipped-budget / exit-2 / failed ticks in the window.
	skipped, budgetEx, hydFail := false, false, false
	for _, t := range ticks {
		s := parseTS(t.Slot)
		if s.Before(lo) || s.After(hi) {
			continue
		}
		switch {
		case t.Status == schema.StatusSkippedBudget:
			skipped = true
		case t.Status == schema.StatusFailed:
			hydFail = true
		}
		for _, src := range t.Sources {
			if strings.Contains(src.Reason, "hydration_budget") || strings.Contains(src.Reason, "hydration_backend_degraded") {
				budgetEx = true
			} else if src.Status != "ok" && strings.Contains(src.Reason, "hydrate") {
				hydFail = true
			}
		}
	}
	switch {
	case skipped:
		return ClassSkippedBudget, "a skipped_budget tick in the window"
	case budgetEx:
		return ClassBudgetExhaust, "a tick in the window exited 2 with a budget reason"
	case hydFail:
		return ClassHydrationFail, "a failed or hydration-degraded tick in the window"
	}
	// blind-spot: the first LATER shadow hydration of the entity changed only
	// blind fields, or a local/sweep item with real change did.
	for _, it := range others {
		if it.id == id && it.at.After(e) && len(it.fields) > 0 && allBlind(it.fields) {
			return ClassBlindSpot, "a later local/sweep-origin hydration changed only blind-spot fields"
		}
	}
	for _, it := range byID[id] {
		if it.at.After(e) {
			if len(it.fields) > 0 && allBlind(it.fields) {
				return ClassBlindSpot, "the next shadow detection changed only blind-spot fields"
			}
			break
		}
	}
	// live-only artefacts.
	evid := "pr.changed:" + id
	enq := 0
	for _, q := range in.Queue {
		if q.EventID != evid || q.At.Before(e.Add(-2*T)) || q.At.After(e.Add(T)) {
			continue
		}
		if q.Op == "evict" && q.Reason == "reemit" {
			return ClassLiveOnly, "live queue re-emitted (coalesced) the event"
		}
		if q.Op == "enqueue" {
			enq++
		}
	}
	if enq >= 2 {
		return ClassLiveOnly, "live queue enqueued the same event more than once (coalescing)"
	}
	for _, g := range routerDown {
		if g.overlaps(lo, hi) {
			return ClassLiveOnly, "live router outage"
		}
	}
	return ClassUnexplained, ""
}

func allBlind(f []string) bool {
	for _, x := range f {
		if !BlindFields[x] {
			return false
		}
	}
	return len(f) > 0
}

func computeCost(pr *PhaseReport, executed []schema.Row, warmupEnd time.Time) {
	type acc struct {
		ticks int
		hyd   int64
		cost  int
		last  *schema.Budget
		min   int
		minOK bool
	}
	hours := map[string]*acc{}
	var order []string
	for _, t := range executed {
		te := parseTS(t.TickEnd)
		if te.Before(warmupEnd) || t.Warmup {
			continue
		}
		if t.DurationMS > 60000 {
			pr.Cost.TicksOver60s++
		}
		h := te.Format("2006-01-02T15:00Z")
		a := hours[h]
		if a == nil {
			a = &acc{}
			hours[h] = a
			order = append(order, h)
		}
		a.ticks++
		a.hyd += t.Hydrations
		a.cost += t.GraphQLCostSum
		if t.Budget != nil && t.Budget.Known {
			a.last = t.Budget
			if !a.minOK || t.Budget.Remaining < a.min {
				a.min, a.minOK = t.Budget.Remaining, true
			}
		}
	}
	sort.Strings(order)
	pr.Cost.Ceiling = 4000
	for _, h := range order {
		a := hours[h]
		hr := Hour{Hour: h, Ticks: a.ticks, Hydrations: a.hyd, GraphQLCost: a.cost}
		if a.last != nil {
			hr.RemainingAtEnd, hr.RemainingKnown, hr.MinRemaining = a.last.Remaining, true, a.min
			if a.min < 1000 {
				pr.Cost.HoursBelowReserve++
			}
		}
		pr.Cost.Hours = append(pr.Cost.Hours, hr)
		pr.samp.hydrationsHour = append(pr.samp.hydrationsHour, float64(a.hyd))
		pr.samp.costHour = append(pr.samp.costHour, float64(a.cost))
	}
	pr.Cost.TickDurationS = MakeDist(pr.samp.tickDur)
	pr.Cost.HydrationsPerHour = MakeDist(pr.samp.hydrationsHour)
	pr.Cost.CostPerHour = MakeDist(pr.samp.costHour)
	pr.Cost.Statement = "Shadow spend is a LOWER BOUND (a show row excludes its metadata read; older rows lack graphql_cost). The shared token ceiling is 4,000 points per hour (5,000 minus the connector's 1,000 reserve) across the live flow AND the shadow."
}

func countWeekdays(done []schema.Row, p Params) int {
	loc := p.Loc
	if loc == nil {
		loc = time.UTC
	}
	per := map[string]int{}
	for _, t := range done {
		if t.Warmup {
			continue
		}
		te := parseTS(t.TickEnd).In(loc)
		if wd := te.Weekday(); wd == time.Saturday || wd == time.Sunday {
			continue
		}
		per[te.Format("2006-01-02")]++
	}
	n := 0
	for _, c := range per {
		if c >= p.WeekdayMinTicks {
			n++
		}
	}
	return n
}

func headSHA(in Input, liveStore string) *HeadSHA {
	ctx := context.Background()
	q := "SELECT entity_id, head_sha FROM entity WHERE entity_type='pr'"
	live, err1 := sqlite.DB{Path: liveStore, ReadOnly: true}.Query(ctx, q)
	shadow, err2 := sqlite.DB{Path: filepath.Join(in.Dir, "state", "pg-desk", "store.db")}.Query(ctx, q)
	if err1 != nil || err2 != nil {
		return nil
	}
	lm := map[string]string{}
	for _, r := range live {
		lm[sqlite.Str(r["entity_id"])] = sqlite.Str(r["head_sha"])
	}
	out := &HeadSHA{}
	seen := map[string]bool{}
	for _, r := range shadow {
		id := sqlite.Str(r["entity_id"])
		seen[id] = true
		l, ok := lm[id]
		if !ok {
			out.OnlyShadow++
			continue
		}
		out.Compared++
		if l != sqlite.Str(r["head_sha"]) {
			out.Disagreeing++
		}
	}
	for id := range lm {
		if !seen[id] {
			out.OnlyLive++
		}
	}
	return out
}
