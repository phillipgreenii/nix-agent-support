package alertrules

import (
	"os"
	"strings"
	"testing"
	"time"
)

// pg-router-queue-slow-growth: arrivals outpace completions over a long window
// (operator ruling 2026-10-12: "the rate of consumption is much lower than the
// rate of generation, that is a problem"). Arrivals are pg_router_enqueued_total
// NET of pg_router_unconsumed_expired_total (pg2-9nfla: a born-expired event that a
// decline retires and the source re-emits is one work item, not one per decline),
// completions pg_router_throughput_total; the rule fires when a type has taken in
// more than 1.5 times as many events as it completed over 6h, at least 6 arrived,
// and a backlog of 5 or more has not drained once in the last hour. The expression
// is modelled here and replayed over testdata/drain_pg2_2026-10-12.txt and
// testdata/drain_pg2_2026-10-13.txt; the real PromQL is exercised by
// grafana/alerting/rule-tests/pg-router-queue-slow-growth.test.yaml.

const (
	growthWindow      = 6 * time.Hour
	growthRatio       = 1.5
	growthMinArrivals = 6
	growthDepthFloor  = 5
	growthDepthWindow = time.Hour
	growthMinPresent  = 108 // 90% of the 120 samples a 30s scrape yields over 1h
	growthFor         = 30 * time.Minute
	growthKeep        = time.Hour

	// growthArrivals is the arrival count of the rule: events enqueued, less events
	// retired without an accept. The `or on(type) (0 * ...)` supplies the 0 for a type
	// whose unconsumed-expired counter has no series (OTel counters are created on the
	// first increment), so a type that never lost an event reads its enqueues as before.
	growthArrivals = `(sum by (type) (increase(pg_router_enqueued_total[6h])) - on(type) (sum by (type) (increase(pg_router_unconsumed_expired_total[6h])) or on(type) (0 * sum by (type) (increase(pg_router_enqueued_total[6h])))))`
	growthDone     = `sum by (type) (increase(pg_router_throughput_total[6h]))`

	// wantGrowthExpr MUST equal the expr of rule pg-router-queue-slow-growth.
	wantGrowthExpr = `(` + growthArrivals + ` > 1.5 * ` + growthDone + ` or ` + growthArrivals + ` unless on(type) ` + growthDone + `) and on(type) ` + growthArrivals + ` >= 6 and on(type) min_over_time(pg_router_queue_depth[1h]) >= 5 and on(type) count_over_time(pg_router_queue_depth[1h]) >= 108`
)

// growthCond models the expression at instant t. increase() over a counter is the
// number of events that arrived (or settled) in the window; a type with arrivals
// and no completions at all (no throughput series) takes the `unless on(type)`
// branch, which is the same as completions == 0 here.
func growthCond(l lane, series []obs, t time.Time) bool {
	return growthCondWith(l, series, t, true)
}

// growthCondWith is growthCond with the arrival count as a parameter: net false is
// the rule before pg2-9nfla, which counted every enqueue, so a retire-and-re-emit
// pair was an arrival each time round.
func growthCondWith(l lane, series []obs, t time.Time, net bool) bool {
	arrivals, completions := 0, 0
	for _, e := range l.enq {
		if e.After(t.Add(-growthWindow)) && !e.After(t) {
			arrivals++
		}
	}
	if net {
		for _, u := range l.retired {
			if u.After(t.Add(-growthWindow)) && !u.After(t) {
				arrivals--
			}
		}
	}
	for _, d := range l.disp {
		if d.to.After(t.Add(-growthWindow)) && !d.to.After(t) {
			completions++
		}
	}
	if arrivals < growthMinArrivals || float64(arrivals) <= growthRatio*float64(completions) {
		return false
	}
	present, minDepth := 0, 0.0
	for _, o := range obsIn(series, t.Add(-growthDepthWindow), t) {
		if o.depth > 0 {
			present++
			if minDepth == 0 || o.depth < minDepth {
				minDepth = o.depth
			}
		}
	}
	return present >= growthMinPresent && minDepth >= growthDepthFloor
}

func replayGrowth(l lane, series []obs, awake func(time.Time) bool, from, to time.Time) map[time.Time]alertState {
	return replayAlert(func(t time.Time) bool { return growthCond(l, series, t) }, awake, from, to, growthFor, growthKeep)
}

// replayGrowthEveryEnqueue is replayGrowth under the rule as it was before pg2-9nfla.
func replayGrowthEveryEnqueue(l lane, series []obs, awake func(time.Time) bool, from, to time.Time) map[time.Time]alertState {
	return replayAlert(func(t time.Time) bool { return growthCondWith(l, series, t, false) }, awake, from, to, growthFor, growthKeep)
}

func TestSlowGrowthRuleMatchesModel(t *testing.T) {
	r := ruleBlock(t, "pg-router-queue-slow-growth")
	if got := ruleExpr(t, r); got != wantGrowthExpr {
		t.Errorf("queue-slow-growth expr drifted from the modelled one:\n got %q\nwant %q", got, wantGrowthExpr)
	}
	for _, need := range []string{
		"for: 30m", "keepFiringFor: 1h", "noDataState: OK", "execErrState: Error", "severity: warning",
		"condition: C", "type: gt", "params: [0]", "reducer: last", "from: 21600", "{{ $labels.type }}",
		"title: pg-router queue is slowly growing (arrivals outpace completions, by type)",
	} {
		if !strings.Contains(r, need) {
			t.Errorf("queue-slow-growth rule lost %q", need)
		}
	}
	if strings.Contains(r, "keep_firing_for") {
		t.Error("snake_case keep_firing_for is silently ignored by file provisioning; use keepFiringFor")
	}
	// The escalation label is pinned to origin-unavailable; this rule routes to the triager.
	if strings.Contains(r, "escalation:") {
		t.Error("queue-slow-growth must not carry an escalation label without an operator ruling")
	}
}

// The reference case of the ruling: 2026-10-12 12:00Z to 19:40Z took in 11 events and
// completed 5 (ratio 2.2, +0.78 an hour, depth 5 to 11), which no rule caught. The
// recorded lane pages for it. The host slept 08:29Z-12:20Z and 13:36Z-16:31Z, so the
// condition cannot hold until the hour of fresh scrapes after the second wake
// (about 17:25Z) and the alert fires 30 minutes later; it is firing at 19:40Z.
func TestSlowGrowthRecordedDayPages(t *testing.T) {
	l := loadLane(t, laneFixture)
	from := parseUTC("2026-10-12T08:32:00Z")
	series := l.scrapes(from, laneEnd)

	var arrivals, completions int
	lo, hi := parseUTC("2026-10-12T12:00:00Z"), parseUTC("2026-10-12T19:40:00Z")
	for _, e := range l.enq {
		if e.After(lo) && !e.After(hi) {
			arrivals++
		}
	}
	for _, d := range l.disp {
		if d.to.After(lo) && !d.to.After(hi) {
			completions++
		}
	}
	if arrivals != 11 || completions != 5 {
		t.Fatalf("the reference window holds %d arrivals and %d completions, the alert record says 11 and 5", arrivals, completions)
	}

	m := replayGrowth(l, series, awakeIn(l), from, laneEnd)
	wake := parseUTC("2026-10-12T16:30:53Z")
	if tm, bad := firstWhere(m, from, wake.Add(50*time.Minute), isFiring); bad {
		t.Errorf("paged at %s, before an hour of fresh scrapes after the wake", tm.Format("15:04:05"))
	}
	first, ok := firstWhere(m, from, laneEnd, isFiring)
	if !ok || first.Before(parseUTC("2026-10-12T17:50:00Z")) || first.After(parseUTC("2026-10-12T18:05:00Z")) {
		t.Fatalf("first page at %v (found=%v), want about 17:55Z", first, ok)
	}
	if !m[hi].firing {
		t.Errorf("not firing at %s, the end of the reference window", hi.Format("15:04"))
	}
	// The backlog keeps outgrowing the consumer through the evening (depth 10 to about
	// 27; the ratio dips under 1.5 for a while around 20:30Z-22:25Z as five dispatches
	// settle in quick succession, and keepFiringFor rides through the first hour of
	// it), so the alert is firing again at the end of the recording.
	if !m[parseUTC("2026-10-13T00:20:00Z")].firing {
		t.Error("not firing at 00:20Z on 10-13, with the backlog still growing")
	}
}

// The wake grace: had the host stayed awake through 13:15Z the same rule would have
// held on 6 arrivals against 0 completions with a backlog of 6 and fired 30 minutes
// later, but the sleep at 13:36Z interrupts the Pending state and the first
// evaluations after the wake find fewer than 108 scrapes in the hour.
func TestSlowGrowthPendingStateDoesNotSurviveASleep(t *testing.T) {
	l := loadLane(t, laneFixture)
	series := l.scrapes(parseUTC("2026-10-12T12:00:00Z"), laneEnd)
	m := replayGrowth(l, series, awakeIn(l), parseUTC("2026-10-12T12:20:00Z"), parseUTC("2026-10-12T17:20:00Z"))
	if tm, bad := firstWhere(m, parseUTC("2026-10-12T12:20:00Z"), parseUTC("2026-10-12T13:13:00Z"), condTrue); bad {
		t.Fatalf("condition true at %s, before the hour had 108 scrapes", tm.Format("15:04:05"))
	}
	pend, ok := firstWhere(m, parseUTC("2026-10-12T13:14:00Z"), parseUTC("2026-10-12T13:36:00Z"), condTrue)
	if !ok {
		t.Fatal("the condition never held before the sleep; the test would be vacuous")
	}
	if tm, bad := firstWhere(m, pend, parseUTC("2026-10-12T17:20:00Z"), isFiring); bad {
		t.Fatalf("fired at %s on a Pending state from before the sleep (pending since %s)", tm.Format("15:04:05"), pend.Format("15:04:05"))
	}
}

// synthLane is a lane with an initial backlog, one arrival every arrEvery and one
// completion every compEvery (a 30s dispatch; its event leaves at the settle), over
// `hours` hours from 2026-10-12T00:00Z. compEvery 0 completes nothing. churnEvery
// models a retire-and-re-emit pair (the queue drops an event unaccepted and the
// source re-emits it): an arrival, an eviction and an unconsumed-expired together,
// no completion.
func synthLane(hours int, initial int, arrEvery, compEvery, churnEvery time.Duration) lane {
	start := parseUTC("2026-10-12T00:00:00Z")
	var l lane
	for i := 0; i < initial; i++ {
		l.enq = append(l.enq, start)
	}
	end := start.Add(time.Duration(hours) * time.Hour)
	for at := start.Add(arrEvery); arrEvery > 0 && at.Before(end); at = at.Add(arrEvery) {
		l.enq = append(l.enq, at)
	}
	for at := start.Add(compEvery); compEvery > 0 && at.Before(end); at = at.Add(compEvery) {
		l.disp = append(l.disp, span{at.Add(-30 * time.Second), at})
		l.evict = append(l.evict, at)
	}
	for at := start.Add(churnEvery); churnEvery > 0 && at.Before(end); at = at.Add(churnEvery) {
		l.enq = append(l.enq, at)
		l.evict = append(l.evict, at)
		l.retired = append(l.retired, at)
	}
	sortTimes(l.enq)
	sortTimes(l.evict)
	sortTimes(l.retired)
	return l
}

func sortTimes(ts []time.Time) {
	for i := 1; i < len(ts); i++ {
		for j := i; j > 0 && ts[j].Before(ts[j-1]); j-- {
			ts[j], ts[j-1] = ts[j-1], ts[j]
		}
	}
}

func TestSlowGrowthSyntheticLanes(t *testing.T) {
	m20, m28, m36, m90 := 20*time.Minute, 28*time.Minute, 36*time.Minute, 90*time.Minute
	cases := []struct {
		name string
		lane lane
		want bool // does the rule ever fire in hours 7..16 (a full 6h lookback)?
	}{
		// 18 arrivals and 18 completions per 6h: ratio 1.0, a lane that keeps up.
		{"keeping up, ratio 1.0", synthLane(16, 8, m20, m20, 0), false},
		// 18 arrivals and 12.9 completions: ratio 1.4, under the 1.5 bar.
		{"ratio 1.4", synthLane(16, 8, m20, m28, 0), false},
		// 18 arrivals and 10 completions: ratio 1.8.
		{"ratio 1.8", synthLane(16, 8, m20, m36, 0), true},
		// Arrivals and no completion at all (the `unless on(type)` branch).
		{"no completions", synthLane(16, 8, m20, 0, 0), true},
		// 4 arrivals in 6h: below the arrivals floor, whatever the ratio.
		{"below the arrivals floor", synthLane(16, 8, m90, 0, 0), false},
		// 36 enqueues and no completions per 6h, but every one is a retire-and-re-emit
		// pair: no distinct arrival, and depth never reaches the floor.
		{"churn alone", synthLane(16, 0, 0, 0, 10*time.Minute), false},
		// The same churn on top of a real backlog of 8 that nothing consumes: 36
		// enqueues and 36 retirements per 6h are no work arriving, so a lane whose
		// distinct arrivals do not outpace its completions does not page (pg2-9nfla;
		// every enqueue was an arrival before, and this paged).
		{"churn on a held backlog", synthLane(16, 8, 0, 0, 10*time.Minute), false},
		// Churn does not hide real growth either: 18 distinct arrivals and 10
		// completions per 6h (ratio 1.8) page with or without the 36 pairs on top.
		{"churn on top of real growth", synthLane(16, 8, m20, m36, 10*time.Minute), true},
		// ... and churn does not page a lane that keeps up: 18 distinct arrivals and 18
		// completions per 6h, with 36 pairs on top (54 enqueues, 18 completions).
		{"churn on top of a lane that keeps up", synthLane(16, 8, m20, m20, 10*time.Minute), false},
	}
	for _, c := range cases {
		from, to := parseUTC("2026-10-12T07:00:00Z"), parseUTC("2026-10-12T16:00:00Z")
		series := c.lane.scrapes(from.Add(-time.Hour), to)
		m := replayGrowth(c.lane, series, func(time.Time) bool { return true }, from, to)
		_, got := firstWhere(m, from, to, isFiring)
		if got != c.want {
			t.Errorf("%s: fires = %v, want %v", c.name, got, c.want)
		}
	}
}

// windowCounts is what the three counters advanced by in the 6 hours to t: enqueues,
// events retired without an accept, and settled dispatches.
func windowCounts(l lane, t time.Time) (enq, retired, done int) {
	in := func(at time.Time) bool { return at.After(t.Add(-growthWindow)) && !at.After(t) }
	for _, e := range l.enq {
		if in(e) {
			enq++
		}
	}
	for _, u := range l.retired {
		if in(u) {
			retired++
		}
	}
	for _, d := range l.disp {
		if in(d.to) {
			done++
		}
	}
	return enq, retired, done
}

// The usage-limit decline storm of 2026-10-13 (pg2-9nfla; found by pg2-hyeno, the
// post-apply verification of this rule). drain.pg2 held a standing backlog of 19 to
// 35 events all day. From 14:52Z to 20:52Z 22 distinct beads arrived against about 6
// completions and depth rose from 22 to 35: a correct page. From 20:54Z the account's
// usage limit made the handler decline every offer; each decline retires a
// born-expired drain event unaccepted and the source re-emits it, which the queue
// counts as one more arrival: 312 enqueues in the 6 hours to 22:42Z, of 27 distinct
// beads, at most 8 of them new, against 13 completions. The rule as shipped read a
// ratio of 24 and, with that burst in its window until about 04:00Z the next day, stayed
// Alerting on a lane whose distinct arrivals no longer outpaced its completions.
func TestSlowGrowthDeclineStormChurnIsNotArrivals(t *testing.T) {
	l := loadLane(t, stormFixture)
	from, to := parseUTC("2026-10-13T14:55:00Z"), parseUTC("2026-10-13T23:28:00Z")
	series := l.scrapes(from.Add(-time.Hour), to)
	net := replayGrowth(l, series, awakeIn(l), from, to)
	every := replayGrowthEveryEnqueue(l, series, awakeIn(l), from, to)
	stormStart := parseUTC("2026-10-13T20:54:00Z")

	// The onset: 22 enqueues of 22 distinct beads between 14:52Z and 20:52Z, and a page
	// whichever way the arrivals are counted, evaluation for evaluation until the storm.
	enq, retired, _ := windowCounts(l, parseUTC("2026-10-13T20:52:00Z"))
	if enq != 22 || retired > 3 {
		t.Fatalf("the 6 hours to 20:52Z hold %d enqueues and %d retirements, the verification counted 22 distinct beads", enq, retired)
	}
	for tm := from; tm.Before(stormStart); tm = tm.Add(evalEvery) {
		if net[tm].cond != every[tm].cond {
			t.Errorf("at %s the net arrivals read the condition %v, every enqueue %v: the onset must not depend on the lever", tm.Format("15:04"), net[tm].cond, every[tm].cond)
		}
	}
	if !net[parseUTC("2026-10-13T20:52:00Z")].firing {
		t.Error("the onset (22 distinct beads against about 6 completions, depth 22 to 35) does not page at 20:52Z")
	}

	// The storm in the window of 22:42Z, the verification's numbers: 312 enqueues of 27
	// distinct beads, at most 8 of them new, 13 completions.
	enq, retired, done := windowCounts(l, parseUTC("2026-10-13T22:42:00Z"))
	t.Logf("the 6 hours to 22:42Z: %d enqueues, %d retired unconsumed (%d net arrivals), %d completions", enq, retired, enq-retired, done)
	if enq < 300 || enq > 320 {
		t.Errorf("%d enqueues in the 6 hours to 22:42Z, the verification counted 312", enq)
	}
	if n := enq - retired; n < 4 || n > 10 {
		t.Errorf("%d net arrivals in the 6 hours to 22:42Z, want about 7 (the verification: at most 8 of the 27 distinct beads were new)", n)
	}

	// As shipped, every enqueue counts: the condition holds through the whole storm and
	// the alert is still firing when the recording ends, which is what was observed
	// (Alerting from 20:14Z, still at 23:00Z).
	for tm := parseUTC("2026-10-13T20:56:00Z"); !tm.After(to); tm = tm.Add(evalEvery) {
		if !every[tm].cond {
			t.Fatalf("with every enqueue counted the condition is false at %s, but the rule as shipped held it through the storm", tm.Format("15:04"))
		}
	}
	if !every[to].firing {
		t.Error("with every enqueue counted the alert is not firing at the end of the recording")
	}

	// Net of retirements the churn is not arrivals, and the lane stops paging once the
	// work items it holds stop outpacing what it completes. Enqueues less retirements is
	// the change in the backlog plus the completions, so the condition is "the backlog
	// grew by more than half of what was completed in the window": it holds while the
	// onset's 22 arrivals are still in the window and the storm has only started to
	// retire the stale part of the standing backlog (depth 35 to 25 by 21:10Z), and ends
	// when the retirements have caught up with the re-emissions (net 5 at 21:20Z, under
	// the floor of 6), more than an hour before the 11 dispatches of 22:02Z-22:29Z lift
	// the completions. Counting each distinct bead once would hold it to about 23:22Z
	// (the verification's estimate), but that takes an event id the counters do not have.
	for _, hm := range []string{"20:56", "21:00", "21:10"} {
		if !net[parseUTC("2026-10-13T"+hm+":00Z")].cond {
			t.Errorf("net of retirements the condition is false at %s, while the onset's arrivals outpace the completions", hm)
		}
	}
	lastCond, ok := lastWhere(net, stormStart, to, condTrue)
	if !ok || lastCond.Before(parseUTC("2026-10-13T21:10:00Z")) || lastCond.After(parseUTC("2026-10-13T21:20:00Z")) {
		t.Fatalf("the condition last held at %v (found=%v), want it to end about 21:10Z, as the retirements catch up with the re-emissions", lastCond, ok)
	}
	t.Logf("net of retirements the condition last held at %s", lastCond.Format("15:04:05"))
	for tm := lastCond.Add(evalEvery); !tm.After(to); tm = tm.Add(evalEvery) {
		if net[tm].cond {
			t.Fatalf("the condition holds again at %s", tm.Format("15:04"))
		}
	}
	if net[to].firing {
		t.Errorf("still firing at %s, keepFiringFor after the condition ended at %s", to.Format("15:04"), lastCond.Format("15:04"))
	}
	lastFire, _ := lastWhere(net, stormStart, to, isFiring)
	if lastFire.Before(lastCond) || lastFire.After(lastCond.Add(growthKeep+evalEvery)) {
		t.Errorf("last firing evaluation at %s, want within keepFiringFor of %s", lastFire.Format("15:04"), lastCond.Format("15:04"))
	}
}

// A burst that is gone within the hour is not slow growth even when the ratio says
// so: 40 arrivals, 5 completions, the other 35 retired unaccepted at 10:30. The ratio
// reads 8 for the next 6 hours, but the backlog drained, so the depth floor (an hour
// that holds a backlog of 5 or more on 108 scrapes) never holds.
func TestSlowGrowthIgnoresABurstThatDrainedWithinTheHour(t *testing.T) {
	var l lane
	start := parseUTC("2026-10-12T10:00:00Z")
	for i := 0; i < 40; i++ {
		l.enq = append(l.enq, start)
	}
	for i := 0; i < 5; i++ {
		at := start.Add(time.Duration(i+1) * time.Minute)
		l.disp = append(l.disp, span{at.Add(-30 * time.Second), at})
		l.evict = append(l.evict, at)
	}
	for i := 0; i < 35; i++ {
		l.evict = append(l.evict, start.Add(30*time.Minute))
	}
	sortTimes(l.evict)
	from, to := start, start.Add(8*time.Hour)
	m := replayGrowth(l, l.scrapes(from, to), func(time.Time) bool { return true }, from, to)
	if tm, bad := firstWhere(m, from, to, condTrue); bad {
		t.Fatalf("an absorbed burst evaluated true at %s", tm.Format("15:04"))
	}
}

// loadSurge reads a recorded surge (testdata/issue_changed_2026-10-01.csv: one row
// per 30s scrape of depth and the accepts the queue log records, header in the file)
// as a lane: an arrival is a rise in depth plus the accepts of the bucket, and
// depth that falls without an accept is an unaccepted retirement.
func loadSurge(t *testing.T, path, day string) (lane, []sample) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var l lane
	var depths []sample
	prev := 0
	for _, line := range strings.Split(string(b), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Split(line, ",")
		if len(f) != 4 {
			t.Fatalf("%s: bad row %q", path, line)
		}
		at := atDay(day, f[0])
		var depth, acc int
		for i, dst := range []*int{&depth, &acc} {
			n := 0
			for _, c := range f[i+1] {
				n = n*10 + int(c-'0')
			}
			*dst = n
		}
		arr := depth - prev + acc
		gone := acc
		if arr < 0 {
			gone, arr = acc-arr, 0
		}
		for i := 0; i < arr; i++ {
			l.enq = append(l.enq, at)
		}
		for i := 0; i < acc; i++ {
			l.disp = append(l.disp, span{at.Add(-time.Second), at})
		}
		for i := 0; i < gone; i++ {
			l.evict = append(l.evict, at)
		}
		if depth > 0 {
			depths = append(depths, sample{at, float64(depth)})
		}
		prev = depth
	}
	return l, depths
}

func atDay(day, hms string) time.Time { return parseUTC(day + "T" + hms + "Z") }

// surgeScrapes is the scrape series of a surge lane: depth from the lane, `settled`
// from its accepts, and the role NEVER caught in flight. That last part is an
// assumption: the surge fixtures record depth and accepts only, and the roles they
// cover (desk-pr, desk-issue) run dispatches of seconds to under a minute, so a
// scrape lands in one rarely; a consumer that is silent for many minutes is idle
// (or hung), which is what the rule is meant to report.
func surgeScrapes(l lane, from, to time.Time) []obs { return l.scrapes(from, to) }

// The recorded pr.changed buildup of 2026-09-30 (testdata/pr_changed_2026-09-30.csv,
// the stall pg2-n1pm4 was filed for), in the queue log's own view of the accepts. Two
// stretches have a backlog and no consumer progress:
//   - 13:13:30Z to 13:40:30Z: 8 or 9 events stand untouched for 27 minutes (below the
//     old rule's depth floor of 10, so it never saw it);
//   - 14:07:30Z to 15:04Z: the queue drains at two events a minute, then the log records
//     NO accept for 56 minutes with 28 to 55 queued, until the daemon restarts at
//     14:49:52Z and the accepts resume (the old rule's 30 minute window plus 30 minutes
//     of `for` could not page 45 minutes of silence).
//
// Both are "queue > 0 and nothing being processed" (the roles of these types run
// dispatches of seconds, so a silent log is an idle consumer, or a hung one). The
// new rule pages each once the silence has lasted a window plus `for`, about 15
// minutes in, and resolves them after keepFiringFor.
func TestPrChangedStall20260930PagesAsIdleWithBacklog(t *testing.T) {
	const day = "2026-09-30"
	l, _ := loadSurge(t, "testdata/pr_changed_2026-09-30.csv", day)
	from, to := atDay(day, "12:31:00"), atDay(day, "15:10:00")
	series := surgeScrapes(l, atDay(day, "12:00:30"), to)
	m := replayStalled(series, func(time.Time) bool { return true }, from, to)

	// Silence 1: last accept 13:13:30Z.
	if tm, bad := firstWhere(m, from, atDay(day, "13:22:00"), condTrue); bad {
		t.Errorf("condition true at %s, while the consumer was still draining the 12:46Z burst", tm.Format("15:04:05"))
	}
	one, ok := firstWhere(m, from, to, isFiring)
	if !ok || one.Before(atDay(day, "13:27:00")) || one.After(atDay(day, "13:32:00")) {
		t.Fatalf("first page at %v (found=%v), want 14-18 minutes after the 13:13:30Z accept", one, ok)
	}
	// The accepts at 13:40:30Z end it; the alert holds for keepFiringFor and resolves.
	if tm, bad := firstWhere(m, atDay(day, "13:52:00"), atDay(day, "14:20:00"), isFiring); bad {
		t.Errorf("still firing at %s, after the consumer resumed at 13:40:30Z", tm.Format("15:04:05"))
	}
	// Silence 2: last accept 14:07:30Z.
	two, ok := firstWhere(m, atDay(day, "14:00:00"), to, isFiring)
	if !ok || two.Before(atDay(day, "14:21:00")) || two.After(atDay(day, "14:28:00")) {
		t.Fatalf("second page at %v (found=%v), want 14-20 minutes after the 14:07:30Z accept", two, ok)
	}
	if last, _ := lastWhere(m, from, to, condTrue); last.After(atDay(day, "15:05:00")) {
		t.Errorf("condition still true at %s, after the consumer resumed at 15:04", last.Format("15:04:05"))
	}
}

// The 2026-10-01 issue.changed buildup (depth 0 to 38 in about an hour, recorded in
// testdata/issue_changed_2026-10-01.csv) is a surge a WORKING consumer absorbs: it
// accepts an event every minute or two through it. None of the rules pages for it, and
// that is the intent the repo already recorded for it (pg2-nw41e, pg2-ralaa): not
// the idle rule (a dispatch settles inside every 10 minute window), not the fast
// growth rule (its trend guards reject a ramp that drains as it climbs) and not the
// slow-growth rule (arrivals are about 1.1 times completions over the window). The
// idle rule WOULD page it if the dispatch gauge were all it read, which is why the
// throughput guard exists (TestStalledPaceLimitedFastRoleIsNotIdle).
func TestSurge20261001IsAbsorbedByEveryRule(t *testing.T) {
	const day = "2026-10-01"
	l, depths := loadSurge(t, "testdata/issue_changed_2026-10-01.csv", day)
	from, to := atDay(day, "20:31:00"), atDay(day, "23:30:00")
	always := func(time.Time) bool { return true }

	for tt := from; !tt.After(to); tt = tt.Add(evalEvery) {
		if alerting(depths, tt) {
			t.Fatalf("pg-router-queue-depth-growing paged for the absorbed surge at %s", tt.Format("15:04"))
		}
	}
	series := surgeScrapes(l, atDay(day, "20:00:30"), to)
	if tm, bad := firstWhere(replayStalled(series, always, from, to), from, to, isFiring); bad {
		t.Errorf("pg-router-queue-stalled paged for the absorbed surge at %s", tm.Format("15:04"))
	}
	if tm, bad := firstWhere(replayGrowth(l, series, always, from, to), from, to, isFiring); bad {
		t.Errorf("pg-router-queue-slow-growth paged for the absorbed surge at %s", tm.Format("15:04"))
	}
}
