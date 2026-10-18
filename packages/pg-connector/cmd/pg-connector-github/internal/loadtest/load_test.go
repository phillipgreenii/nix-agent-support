// Package loadtest is the load test of the pg-connector-github daemon's
// scheduler, governor and change engine, run together.
//
// # Scale and model
//
// 150 kept-alive pull requests (120 listed by 11 configured queries, so their
// work is class 3; 30 kept alive by access alone, so their work is class 4), on
// a fake clock, over the REAL store (SQLite, in memory), scheduler, governor,
// summary service, group engine, revalidation handler, change engine, settle
// lifecycle, query runner and change feed. Only the origin is fake: a summary
// origin, five group fetchers and a query listing, each of which admits its
// calls through the governor, counts them, and answers after a round trip of
// fake time. The scenario, seeded by loadSeed:
//
//   - runs cold for the first summary cycle (a cold start appends nothing) and
//     gives a handful of PRs a new head in the second;
//   - gives 100 to 120 PRs a new head within the third summary cycle (a mass
//     rebase), a handful in the fourth and fifth, then one PR a minute;
//   - injects interactive reads throughout: four callers at once
//     (summary.Service.InteractiveFresh, each for another PR) every thirty
//     seconds and two (groupsvc.Service.Ensure with fresh: true) every ninety;
//   - lets another process drain the token between minutes 8 and 11.5: the
//     origin's GraphQL remaining falls to 1,300, then 1,005, and recovers when
//     its window resets;
//   - runs nineteen minutes, which is three of the six-minute maximum ages (see
//     below), the last ninety seconds quiet so that every pending row ends.
//
// A consumer polls the feed every ten seconds and acknowledges the poll's
// NextSeq on the next tick.
//
// What differs from the daemon's defaults, and why (everything else is a
// default: summary_ttl 60 s, settle_timeout 30 s, the GraphQL cap of 1,500 an
// hour with a reserve of 1,000, background_share 0.9):
//
//   - detail_max_age, conversation_max_age and ci_max_age are 6 minutes (30 by
//     default) and detail_ttl and conversation_ttl 2 minutes (10), so that the
//     run covers more than two maximum ages in a time the race detector can
//     afford (a run at the defaults takes two hours of fake time);
//   - rest_requests_per_hour is 1,500 (3,000), so that the REST cap binds inside
//     the run: the runs groups ask for 150 requests a minute and the burst's
//     dependents for 300 more;
//   - the scheduler has ONE worker whose calls take a quarter of a second, which
//     is the throughput of the daemon's four workers at a second each, and every
//     origin call parks until the driver has advanced the clock, so the run is
//     deterministic.
//
// # Properties asserted
//
//  1. Spend: per bucket, over every rolling window of the bucket's length, the
//     spend never exceeds the cap, the background classes never exceed their
//     share of it, and nothing spends below the reserve. The REST cap is
//     reached, so this is exercised; the burst's class 2 dependents take more
//     than the 10% the background leaves, so the cap itself binds before the
//     share does.
//  2. Interactive wait: the p99 of the fake time between an interactive read's
//     submission and its return stays under interactiveBound.
//  3. Progress: classes 3 and 4 each complete work in every five-minute window
//     (scheduler.DefaultAgeAfter) from the burst on while interactive reads keep
//     arriving, outside the drain, when every GraphQL spend is refused.
//  4. Falling reading: while the origin's remaining is low the governor admits at
//     most the reading less rate_reserve_points, and it admits at the normal
//     rate again once the origin's window resets.
//  5. Change engine: every pending row ends by its deadline plus one lifecycle
//     interval and none is left; per PR the log holds one head_changed row for
//     each head change of the origin, plus the late rows of dependents that
//     landed after a deadline (none when no row timed out); sequence numbers
//     are dense and increasing and a PR's versions never repeat.
//  6. Pending rows never exceed the number of PRs.
//  7. A consumer that polls and acknowledges never sees a row twice after its
//     acknowledgement and never misses one.
//
// # Running it
//
//	nice -n 10 go test -p 4 -count=1 -race ./cmd/pg-connector-github/internal/loadtest/
//
// TestLoad runs the scenario twice side by side and requires the appended rows
// and the totals to be identical. TestLoadMutationsAreCaught weakens the
// harness three ways (a governor with ten times the caps, a lifecycle that is
// never swept, a consumer that acknowledges the log head instead of the poll's
// NextSeq) and requires the matching property to fail.
package loadtest

import (
	"cmp"
	"fmt"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/classify"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/scheduler"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

const (
	// The origin drain: the reading falls at drainStart and recovers at drainEnd.
	// drainMid is when it falls to its lowest.
	drainStart = 8 * time.Minute
	drainMid   = drainStart + 30*time.Second
	drainEnd   = 11*time.Minute + 30*time.Second

	// interactiveBound is the p99 wait the test allows an interactive read. A
	// class 1 task jumps the queue, so it waits for the one batch in flight to
	// finish (at most one round trip of a quarter second), then for the class 1
	// tasks ahead of it (a cluster is three summary reads or two Ensure calls of
	// two flights each, so at most four round trips) and for its own round trip:
	// about six round trips, or a second and a half. Five seconds is three times
	// that, a twelfth of summary_ttl (60 s) and a sixth of settle_timeout (30 s):
	// loose enough for a tick's rounding, tight enough that a read queued behind
	// background work (a burst of 300 batches takes 75 s) fails it. The first
	// guess was twice summary_ttl (120 s); the measured p99 is under a second.
	interactiveBound = 5 * time.Second

	// The fake time of the mutated runs: the cap is reached after about nine
	// minutes (the REST spend is 1,947 in twelve with ten times the cap); the
	// burst's rows are all appended within five.
	capMutationDuration   = 12 * time.Minute
	burstMutationDuration = 5 * time.Minute

	// The progress windows are five minutes (scheduler.DefaultAgeAfter), stepping
	// by a minute.
	progressWindow = scheduler.DefaultAgeAfter
)

// result is what a scenario run produced, for the checks to read.
type result struct {
	opt       scenarioOpts
	t0        time.Time
	end       time.Time
	caps      map[governor.Bucket]governor.Limits
	share     float64
	spends    []governor.SpendEvent
	govs      []govSample
	waits     []waitRec
	stores    []storeRec
	queryRuns []time.Time
	hits      map[string]int
	rows      []store.ChangeRow
	changes   map[string]int // head changes the origin made, by ref
	settled   map[string]int
	pend      []*pendRec
	maxPend   int
	late      []string
	steps     []readingStep
	errs      []string

	delivered []int64
	twice     []string
	driver    string

	// the scale the run had
	prs, queries, burstHeads int
	burstAt, summaryTTL      time.Duration
	maxAges                  []time.Duration
}

func (h *harness) collect() *result {
	rows, err := h.rig.Store.ChangesAfter(h.ctx, 0, 0)
	if err != nil {
		h.t.Fatal(err)
	}
	r := &result{
		opt: h.opt, t0: h.t0, end: h.clk.Now(), caps: h.govCaps, share: h.cfg.BackgroundShare,
		govs: h.govs, waits: h.waits, rows: rows, changes: map[string]int{},
		prs: nPRs, queries: len(h.queryNames), burstHeads: h.burstHeads, burstAt: h.burstAt,
		summaryTTL: h.cfg.SummaryTTL.Std(),
		maxAges:    []time.Duration{h.cfg.DetailMaxAge.Std(), h.cfg.ConversationMaxAge.Std(), h.cfg.CIMaxAge.Std()},
		driver:     fmt.Sprintf("%d scheduler steps, %d batches, %d origin calls", h.stepCalls, h.batches, h.released),
		maxPend:    h.maxPend, late: h.late, steps: h.steps, delivered: h.cons.delivered, twice: h.cons.twice,
	}
	h.rec.mu.Lock()
	r.spends = slices.Clone(h.rec.spends)
	r.stores = slices.Clone(h.rec.stores)
	r.queryRuns = slices.Clone(h.rec.queryRuns)
	r.hits = map[string]int{}
	for k, v := range h.rec.queryHits {
		r.hits[k] = v
	}
	r.settled = map[string]int{}
	for k, v := range h.rec.settled {
		r.settled[k] = v
	}
	r.errs = slices.Clone(h.rec.errs)
	h.rec.mu.Unlock()
	for i := 0; i < nPRs; i++ {
		r.changes[refOf(i)] = h.rig.Origin.Changes(nodeOf(i))
	}
	for _, p := range h.pend {
		r.pend = append(r.pend, p)
	}
	sort.Slice(r.pend, func(i, j int) bool {
		if !r.pend[i].deadline.Equal(r.pend[j].deadline) {
			return r.pend[i].deadline.Before(r.pend[j].deadline)
		}
		return r.pend[i].id < r.pend[j].id
	})
	return r
}

// ---- the checks --------------------------------------------------------------

func windowOf(b governor.Bucket) time.Duration {
	if b == governor.BucketRESTSearch {
		return governor.DefaultSearchWindow
	}
	return governor.DefaultWindow
}

func isBackground(c governor.Class) bool { return c == 0 || c.Background() }

// rollingMax is the largest spend inside any window of the given length that
// ends at a spend: the sum of the events with at in (t - window, t]. keep says
// which events count.
func rollingMax(evs []governor.SpendEvent, window time.Duration, keep func(governor.SpendEvent) bool) int64 {
	var sum, best int64
	var in []governor.SpendEvent
	for _, e := range evs {
		if !keep(e) {
			continue
		}
		in = append(in, e)
		sum += e.Cost
		for len(in) > 0 && !in[0].At.After(e.At.Add(-window)) {
			sum -= in[0].Cost
			in = in[1:]
		}
		best = max(best, sum)
	}
	return best
}

func (r *result) bucketEvents(b governor.Bucket) []governor.SpendEvent {
	var out []governor.SpendEvent
	for _, e := range r.spends {
		if e.Bucket == b {
			out = append(out, e)
		}
	}
	slices.SortStableFunc(out, func(a, b governor.SpendEvent) int { return a.At.Compare(b.At) })
	return out
}

func bgCap(cap int64, share float64) int64 { return int64(float64(cap)*share + 1e-9) }

// checkSpend is property 1.
func checkSpend(r *result) []string {
	var bad []string
	for _, b := range governor.Buckets() {
		lim := r.caps[b]
		evs := r.bucketEvents(b)
		window := windowOf(b)
		if got := rollingMax(evs, window, func(governor.SpendEvent) bool { return true }); got > lim.Cap {
			bad = append(bad, fmt.Sprintf("%s: %d spent inside one %s, over the cap of %d", b, got, window, lim.Cap))
		}
		bcap := bgCap(lim.Cap, r.share)
		if got := rollingMax(evs, window, func(e governor.SpendEvent) bool { return isBackground(e.Class) }); got > bcap {
			bad = append(bad, fmt.Sprintf("%s: the background classes spent %d inside one %s, over their share of %d", b, got, window, bcap))
		}
		if b == governor.BucketGraphQL {
			for _, e := range evs {
				if e.Remaining >= 0 && e.Remaining < lim.Reserve {
					bad = append(bad, fmt.Sprintf("%s: a spend at %s left %d remaining, below the reserve of %d", b, e.At.Format("15:04:05"), e.Remaining, lim.Reserve))
					break
				}
			}
		}
	}
	for _, s := range r.govs {
		for _, bs := range s.state.Buckets {
			lim := r.caps[bs.Bucket]
			if bs.Spent > lim.Cap || bs.BackgroundSpent > bgCap(lim.Cap, r.share) {
				bad = append(bad, fmt.Sprintf("%s: the governor's own state at %s shows %d spent (%d background), over the cap of %d (background %d)",
					bs.Bucket, s.at.Format("15:04:05"), bs.Spent, bs.BackgroundSpent, lim.Cap, bgCap(lim.Cap, r.share)))
				return bad
			}
		}
	}
	return bad
}

// checkBinds says the REST cap was reached, so property 1 was exercised: the
// spend of all classes came within 5% of it. (The background classes stop at
// their share, which is 90% of the cap; here the burst's class 2 dependents
// take more than the other 10%, so the cap itself is what binds first. The
// share is asserted all the same, on every sample, by checkSpend.)
func checkBinds(r *result) []string {
	lim := r.caps[governor.BucketRESTCore]
	got := rollingMax(r.bucketEvents(governor.BucketRESTCore), windowOf(governor.BucketRESTCore), func(governor.SpendEvent) bool { return true })
	if want := lim.Cap * 95 / 100; got < want {
		return []string{fmt.Sprintf("the REST spend peaked at %d, under 95%% of its cap (%d): the cap never bound, so the test shows nothing about it", got, want)}
	}
	return nil
}

// quantile is the q-quantile of the sorted waits (nearest rank).
func quantile(ws []time.Duration, q float64) time.Duration {
	if len(ws) == 0 {
		return 0
	}
	i := int(float64(len(ws))*q+0.999999) - 1
	return ws[max(0, min(i, len(ws)-1))]
}

func (r *result) waitDurations() []time.Duration {
	var out []time.Duration
	for _, w := range r.waits {
		out = append(out, w.wait)
	}
	slices.Sort(out)
	return out
}

// checkInteractive is property 2.
func checkInteractive(r *result) []string {
	ws := r.waitDurations()
	if len(ws) < 60 {
		return []string{fmt.Sprintf("only %d interactive reads were measured", len(ws))}
	}
	if p := quantile(ws, 0.99); p > interactiveBound {
		return []string{fmt.Sprintf("the interactive p99 wait is %s, over the bound of %s (p50 %s, max %s, %d reads)", p, interactiveBound, quantile(ws, 0.5), ws[len(ws)-1], len(ws))}
	}
	return nil
}

// checkProgress is property 3: in every window of five minutes from the burst on
// (outside the origin drain, when every GraphQL spend is refused), class 3 work
// (a configured query run, or any group of a member PR stored) and class 4 work
// (any group of a PR kept alive by access alone stored) each happened.
func checkProgress(r *result) []string {
	var bad []string
	type span struct{ from, to time.Time }
	skip := span{r.t0.Add(drainStart), r.t0.Add(drainEnd + time.Minute)}
	start := r.t0.Add(2 * time.Minute)
	last := r.end.Add(-90 * time.Second)
	for w := start; !w.Add(progressWindow).After(last); w = w.Add(time.Minute) {
		to := w.Add(progressWindow)
		if w.Before(skip.to) && to.After(skip.from) {
			continue
		}
		in := func(t time.Time) bool { return !t.Before(w) && t.Before(to) }
		c3, c4 := false, false
		for _, t := range r.queryRuns {
			c3 = c3 || in(t)
		}
		for _, s := range r.stores {
			if !in(s.at) {
				continue
			}
			if s.node < nMembers {
				c3 = true
			} else {
				c4 = true
			}
		}
		if !c3 {
			bad = append(bad, fmt.Sprintf("no class 3 work completed in %s to %s", w.Format("15:04:05"), to.Format("15:04:05")))
		}
		if !c4 {
			bad = append(bad, fmt.Sprintf("no class 4 work completed in %s to %s", w.Format("15:04:05"), to.Format("15:04:05")))
		}
		if len(bad) >= 6 {
			break
		}
	}
	return bad
}

// checkReading is property 4.
func checkReading(r *result) []string {
	var bad []string
	if len(r.steps) != 3 {
		return []string{fmt.Sprintf("the origin's reading moved %d times, want 3", len(r.steps))}
	}
	reserve := r.caps[governor.BucketGraphQL].Reserve
	gq := r.bucketEvents(governor.BucketGraphQL)
	spendIn := func(from, to time.Time) int64 {
		var n int64
		for _, e := range gq {
			if !e.At.Before(from) && e.At.Before(to) {
				n += e.Cost
			}
		}
		return n
	}
	for i, s := range r.steps[:2] {
		to := r.steps[i+1].at
		// One call may have been admitted before the reading fell and settle after it.
		if got, limit := spendIn(s.at, to), s.remaining-reserve+1; got > limit {
			bad = append(bad, fmt.Sprintf("with the origin reading %d, %d GraphQL points were spent in %s to %s, over %d (the reading less the reserve of %d, plus one call in flight)",
				s.remaining, got, s.at.Format("15:04:05"), to.Format("15:04:05"), limit, reserve))
		}
	}
	// The reading is low long enough to bind, and the daemon spends at the normal
	// rate before it and after it. "Would have spent" is the rate of the four
	// minutes before the fall, over the time the reading was lowest.
	fall, recover := r.steps[0].at, r.steps[2].at
	before := spendIn(fall.Add(-4*time.Minute), fall)
	span := recover.Sub(r.steps[1].at)
	would := before * int64(span) / int64(4*time.Minute)
	allowed := r.steps[1].remaining - reserve + 1
	if before < 20 {
		bad = append(bad, fmt.Sprintf("only %d GraphQL points were spent in the four minutes before the reading fell: too little to show a difference", before))
	}
	if would < 2*allowed {
		bad = append(bad, fmt.Sprintf("the reading did not bind: at the earlier rate the daemon would have spent %d in the %s the reading was lowest, against %d allowed", would, span, allowed))
	}
	after := spendIn(recover, recover.Add(4*time.Minute))
	if after*2 < before {
		bad = append(bad, fmt.Sprintf("spend did not resume at the normal rate: %d points in the four minutes after the reading recovered, against %d before it fell", after, before))
	}
	return bad
}

// checkScale says the run had the scale the packet asks for: about 150 PRs, 11
// query strings, a burst of at least 100 heads inside one summary cycle, and at
// least two maximum ages of fake time.
func checkScale(r *result) []string {
	var bad []string
	if r.prs < 140 || r.prs > 160 {
		bad = append(bad, fmt.Sprintf("%d PRs, want about 150", r.prs))
	}
	if r.queries != 11 {
		bad = append(bad, fmt.Sprintf("%d configured queries, want 11", r.queries))
	}
	if r.burstHeads < 100 {
		bad = append(bad, fmt.Sprintf("the burst changed %d heads, want at least 100", r.burstHeads))
	}
	if cycle := r.burstAt / r.summaryTTL; cycle != 2 { // the third cycle is [2 ttl, 3 ttl)
		bad = append(bad, fmt.Sprintf("the burst came at %s, which is summary cycle %d, want the third", r.burstAt, cycle+1))
	}
	for _, age := range r.maxAges {
		if r.end.Sub(r.t0) < 2*age {
			bad = append(bad, fmt.Sprintf("the run covers %s, under two maximum ages of %s", r.end.Sub(r.t0), age))
		}
	}
	return bad
}

// checkPending is properties 5 (the deadline half) and 6.
func checkPending(r *result) []string {
	bad := slices.Clone(r.late)
	if len(bad) > 5 {
		bad = bad[:5]
	}
	for _, p := range r.pend {
		switch {
		case !p.ended:
			bad = append(bad, fmt.Sprintf("the pending row of %s (deadline %s) was never ended", p.id, p.deadline.Format("15:04:05.000")))
		case p.endedAt.Sub(p.deadline) > time.Second:
			bad = append(bad, fmt.Sprintf("the pending row of %s ended at %s, more than one lifecycle interval after its deadline %s", p.id, p.endedAt.Format("15:04:05.000"), p.deadline.Format("15:04:05.000")))
		}
		if len(bad) >= 8 {
			break
		}
	}
	if len(r.pend) < nPRs/2 {
		bad = append(bad, fmt.Sprintf("only %d pending rows were seen: the burst did not exercise the barrier", len(r.pend)))
	}
	if r.maxPend > nPRs {
		bad = append(bad, fmt.Sprintf("%d pending rows at one time, more than the %d PRs", r.maxPend, nPRs))
	}
	return bad
}

func hasKind(c store.ChangeRow, kind string) bool { return slices.Contains(c.Kinds, kind) }

// checkRows is property 5's other half: the log against the origin's changes.
func checkRows(r *result) []string {
	var bad []string
	add := func(format string, args ...any) {
		if len(bad) < 8 {
			bad = append(bad, fmt.Sprintf(format, args...))
		}
	}
	for i, c := range r.rows {
		if c.Seq != int64(i)+1 {
			add("the log's sequence numbers are not dense and increasing: row %d has seq %d", i+1, c.Seq)
			break
		}
	}
	lastVersion := map[string]int64{}
	head, late := map[string]int{}, map[string]int{}
	for _, c := range r.rows {
		if v, ok := lastVersion[c.ID]; ok && c.Version <= v {
			add("%s: seq %d has version %d after version %d", c.ID, c.Seq, c.Version, v)
		}
		lastVersion[c.ID] = c.Version
		if hasKind(c, classify.KindHeadChanged) {
			head[c.ID]++
		} else {
			late[c.ID]++
		}
	}
	totalLate := 0
	for ref, n := range r.changes {
		if head[ref] != n {
			add("%s: the origin changed the head %d times and the log holds %d head_changed rows", ref, n, head[ref])
		}
		if late[ref] > 3*n {
			add("%s: %d late rows for %d head changes (at most three groups follow a head change)", ref, late[ref], n)
		}
		totalLate += late[ref]
	}
	for ref := range head {
		if _, ok := r.changes[ref]; !ok {
			add("the log holds rows for %s, which is no PR of the scenario", ref)
		}
	}
	if held := r.settled[classifyTimeout] + r.settled[classifyFailed]; held == 0 && totalLate > 0 {
		add("%d late rows, though every pending row settled (no group was held back)", totalLate)
	}
	for _, c := range r.rows {
		if c.At.Before(r.t0.Add(time.Minute+10*time.Second)) && len(r.rows) > 0 {
			add("seq %d was appended at %s, before the first head change: a cold start must append nothing", c.Seq, c.At.Format("15:04:05"))
			break
		}
	}
	return bad
}

const (
	classifyTimeout = "timeout"
	classifyFailed  = "failed"
)

// checkConsumer is property 7.
func checkConsumer(r *result) []string {
	bad := slices.Clone(r.twice)
	if len(bad) > 5 {
		bad = bad[:5]
	}
	got := map[int64]bool{}
	for i, s := range r.delivered {
		got[s] = true
		if i > 0 && s <= r.delivered[i-1] {
			bad = append(bad, fmt.Sprintf("the consumer was handed seq %d after seq %d", s, r.delivered[i-1]))
			break
		}
	}
	var missed []int64
	for _, c := range r.rows {
		if !got[c.Seq] {
			missed = append(missed, c.Seq)
		}
	}
	if len(missed) > 0 {
		bad = append(bad, fmt.Sprintf("the consumer never saw %d of the %d rows (first missed: seq %d)", len(missed), len(r.rows), missed[0]))
	}
	if len(r.delivered) == 0 {
		bad = append(bad, "the consumer was handed nothing")
	}
	return bad
}

// allChecks runs every property; the keys name the property for the mutation
// test and for the failure messages.
func allChecks(r *result) map[string][]string {
	return map[string][]string{
		"0 scale":              checkScale(r),
		"1 spend":              append(checkSpend(r), checkBinds(r)...),
		"2 interactive wait":   checkInteractive(r),
		"3 progress":           checkProgress(r),
		"4 falling reading":    checkReading(r),
		"5 pending deadlines":  checkPending(r),
		"5 rows against truth": checkRows(r),
		"7 consumer":           checkConsumer(r),
	}
}

// signature is what two runs of one seed must agree on: the appended rows, in
// order, and the totals the run produced.
func (r *result) signature() string {
	var b strings.Builder
	for _, c := range r.rows {
		fmt.Fprintf(&b, "%d %s v%d %v\n", c.Seq, c.ID, c.Version, c.Kinds)
	}
	for _, bk := range governor.Buckets() {
		var n, bg int64
		for _, e := range r.bucketEvents(bk) {
			n += e.Cost
			if isBackground(e.Class) {
				bg += e.Cost
			}
		}
		fmt.Fprintf(&b, "%s spent %d (%d background)\n", bk, n, bg)
	}
	keys := make([]string, 0, len(r.settled))
	for k := range r.settled {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		fmt.Fprintf(&b, "settled %s %d\n", k, r.settled[k])
	}
	fmt.Fprintf(&b, "waits %d, pending rows seen %d, max pending %d\n", len(r.waits), len(r.pend), r.maxPend)
	return b.String()
}

func (r *result) summaryLines() string {
	ws := r.waitDurations()
	lates := 0
	for _, c := range r.rows {
		if !hasKind(c, classify.KindHeadChanged) {
			lates++
		}
	}
	heads := 0
	for _, n := range r.changes {
		heads += n
	}
	var b strings.Builder
	fmt.Fprintf(&b, "fake time %s; %d origin head changes; %d log rows (%d late); pending rows seen %d, at most %d at once; settled %v\n",
		r.end.Sub(r.t0), heads, len(r.rows), lates, len(r.pend), r.maxPend, r.settled)
	for _, bk := range []governor.Bucket{governor.BucketGraphQL, governor.BucketRESTCore} {
		lim := r.caps[bk]
		evs := r.bucketEvents(bk)
		all := rollingMax(evs, windowOf(bk), func(governor.SpendEvent) bool { return true })
		bg := rollingMax(evs, windowOf(bk), func(e governor.SpendEvent) bool { return isBackground(e.Class) })
		fmt.Fprintf(&b, "%s: peak %d of cap %d per %s, peak background %d of %d\n", bk, all, lim.Cap, windowOf(bk), bg, bgCap(lim.Cap, r.share))
	}
	for _, bk := range []governor.Bucket{governor.BucketGraphQL, governor.BucketRESTCore} {
		var per []string
		evs := r.bucketEvents(bk)
		for from := r.t0; from.Before(r.end); from = from.Add(5 * time.Minute) {
			var n int64
			for _, e := range evs {
				if !e.At.Before(from) && e.At.Before(from.Add(5*time.Minute)) {
					n += e.Cost
				}
			}
			per = append(per, fmt.Sprint(n))
		}
		fmt.Fprintf(&b, "%s spent per five minutes: %s\n", bk, strings.Join(per, " "))
	}
	fmt.Fprintf(&b, "interactive reads %d: p50 %s p99 %s max %s (bound %s)\n", len(ws), quantile(ws, 0.5), quantile(ws, 0.99), quantile(ws, 1), interactiveBound)
	fmt.Fprintf(&b, "driver: %s\n", r.driver)
	fmt.Fprintf(&b, "query runs %d; consumer was handed %d rows", len(r.queryRuns), len(r.delivered))
	return b.String()
}

// ---- the tests ---------------------------------------------------------------

// runAll runs the scenarios in parallel (each has its own store, clock and
// scheduler) and returns their results in order; a scenario that failed leaves
// nil.
func runAll(t *testing.T, name string, opts ...scenarioOpts) []*result {
	t.Helper()
	out := make([]*result, len(opts))
	t.Run(name, func(t *testing.T) {
		for i, o := range opts {
			t.Run(fmt.Sprint(i), func(t *testing.T) {
				t.Parallel()
				out[i] = newHarness(t, o).run()
			})
		}
	})
	return out
}

func TestLoad(t *testing.T) {
	t.Parallel() // with TestLoadMutationsAreCaught: under -race the two run for minutes each
	start := time.Now()
	// Two runs of the same seed, side by side: the first is checked, and the two
	// must agree.
	res := runAll(t, "runs", scenarioOpts{seed: loadSeed}, scenarioOpts{seed: loadSeed})
	first, second := res[0], res[1]
	if first == nil || second == nil {
		t.Fatal("a scenario run failed")
	}
	t.Logf("%s\n(wall clock, both runs together: %s)", first.summaryLines(), time.Since(start).Round(time.Millisecond))
	checks := allChecks(first)
	for _, name := range sortedKeys(checks) {
		for _, msg := range checks[name] {
			t.Errorf("property %s: %s", name, msg)
		}
	}
	if len(first.errs) > 0 {
		t.Errorf("the daemon logged errors the run survived: %v", first.errs[:min(5, len(first.errs))])
	}
	if a, b := first.signature(), second.signature(); a != b {
		t.Errorf("two runs of seed %d differ:\nfirst:\n%s\nsecond:\n%s", loadSeed, a, b)
	}
}

// TestLoadSeedDecidesTheBurst shows the seed is used: the same seed picks the
// same PRs for the burst and another seed picks others.
func TestLoadSeedDecidesTheBurst(t *testing.T) {
	burst := func(seed int64) []int {
		h := newHarness(t, scenarioOpts{seed: seed})
		return h.pick(100+h.rng.Intn(21), 0)
	}
	a, b, c := burst(loadSeed), burst(loadSeed), burst(loadSeed+1)
	if !slices.Equal(a, b) {
		t.Errorf("the same seed picked %v and then %v", a, b)
	}
	if slices.Equal(a, c) {
		t.Errorf("seeds %d and %d picked the same %d PRs: the seed does not decide the burst", loadSeed, loadSeed+1, len(a))
	}
	if len(a) < 100 {
		t.Errorf("the burst changes %d heads, want at least 100", len(a))
	}
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, cmp.Compare[string])
	return keys
}

// TestLoadMutationsAreCaught weakens the harness three ways, as the packet's
// validation does by hand, and requires the matching assertion to fail.
func TestLoadMutationsAreCaught(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		opt  scenarioOpts
		want string // the property that must report it
	}{
		{"governor caps times ten", scenarioOpts{seed: loadSeed, duration: capMutationDuration, capScale: 10}, "1 spend"},
		{"lifecycle never swept", scenarioOpts{seed: loadSeed, duration: burstMutationDuration, skipSweep: true}, "5 pending deadlines"},
		{"consumer acknowledges the head", scenarioOpts{seed: loadSeed, duration: burstMutationDuration, ackEarly: true}, "7 consumer"},
	}
	var opts []scenarioOpts
	for _, tc := range cases {
		opts = append(opts, tc.opt)
	}
	res := runAll(t, "mutated", opts...)
	for i, tc := range cases {
		if res[i] == nil {
			t.Errorf("the run with %q failed", tc.name)
			continue
		}
		got := allChecks(res[i])
		if len(got[tc.want]) == 0 {
			t.Errorf("property %s did not fail with the mutation %q; findings by property: %v", tc.want, tc.name, got)
		} else {
			t.Logf("%q is caught by property %s: %s", tc.name, tc.want, got[tc.want][0])
		}
	}
}
