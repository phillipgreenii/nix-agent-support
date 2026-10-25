package loadtest

import (
	"fmt"
	"slices"
	"sort"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/telemetry"
)

// The tracked-set load scenario: about 500 tracked PRs, the 100 that the
// configured queries list watched (plus twenty watched by a direct read) and the
// rest tracked by a read alone, on the real store, scheduler, governor, summary
// service, group engine and change engine, with the registration the daemon runs
// now (a row only for a watched PR), the daemon's real freshness windows and its
// post-cutover GraphQL cap of 4,000 an hour. The REST cap is the harness's 1,500
// an hour, which the PRs' runs groups alone exceed, so a budget does bind.
//
// What it shows (the properties of the PR lane's tracking design):
//
//  1. The spend never exceeds a cap, and the REST cap is reached (so the
//     governor really held work back), as in the default scenario.
//  2. The freshness of the watched PRs is not starved by the unwatched
//     population: each watched member PR's first summary comes within two
//     minutes of the start, and from the eighth minute on (once the cold start,
//     which queues every PR at once, has drained) no two of its summary
//     refreshes are more than five minutes apart. Five minutes is the
//     scheduler's aging bound (scheduler.DefaultAgeAfter): a tracked-only task
//     overdue that long competes as a watched one, so it is the longest a
//     watched PR can be made to wait for the 400 tracked-only PRs.
//  3. The governor backlog is emitted: the daemon's own telemetry, read every
//     ten seconds of fake time, carries governor.backlog for every class, its
//     keep-alive class (the tracked-only PRs' work) peaks well above zero and is
//     still above zero at the end (the work the budget could not serve), alongside
//     the tracked entity counts by class (100 members, 400 other open, 0 closed in
//     grace), the watched count (120) and the configured tracked-set limit.
//  4. Interactive reads still wait under the bound.
//
// What it does NOT show: that watched PRs stay fresh when the budget cannot
// cover them. The scheduler orders by priority class but a class 4 task overdue
// for the aging time competes as class 3, ordered by due time, so under a budget
// too small for the tracked set every PR waits about as long as every other. The
// tracked-set alert and the governor backlog exist for that case.
//
// Running it:
//
//	nice -n 10 go test -p 4 -count=1 -race -run TestTrackedSetLoad ./cmd/pg-connector-github/internal/loadtest/

const (
	trackedDuration = 15 * time.Minute
	trackedDirect   = 20 // non-member PRs watched by a direct read
	// The watched PRs' first summary comes within trackedFirstBound; after
	// trackedWarmup no two refreshes are more than trackedGapBound apart.
	trackedFirstBound = 2 * time.Minute
	trackedWarmup     = 8 * time.Minute
	trackedGapBound   = progressWindow
)

func trackedOpts() scenarioOpts {
	return scenarioOpts{
		seed: loadSeed, duration: trackedDuration,
		prs: trackedPRs, members: trackedMembers, graphqlPerHour: trackedGraphQLPerHour,
		watchMembers: true, watchDirect: trackedDirect, metrics: true, defaultAges: true, realRegistration: true,
	}
}

// summaryTimes lists, per PR, the times its summary was stored, in order.
func summaryTimes(r *result) map[int][]time.Time {
	out := map[int][]time.Time{}
	for _, s := range r.stores {
		if s.group == groups.Summary {
			out[s.node] = append(out[s.node], s.at)
		}
	}
	for _, ts := range out {
		slices.SortFunc(ts, func(a, b time.Time) int { return a.Compare(b) })
	}
	return out
}

// checkWatchedFresh is property 2: each watched member PR's first summary comes
// within trackedFirstBound of the start and, from trackedWarmup on, no two of its
// summaries (nor the last and the end of the run) are more than trackedGapBound
// apart.
func checkWatchedFresh(r *result) []string {
	var bad []string
	times := summaryTimes(r)
	warm := r.t0.Add(trackedWarmup)
	for i := 0; i < r.members; i++ {
		ts := times[i]
		if len(ts) == 0 {
			bad = append(bad, fmt.Sprintf("watched PR %s never had its summary stored", nodeOf(i)))
			continue
		}
		if first := ts[0].Sub(r.t0); first > trackedFirstBound {
			bad = append(bad, fmt.Sprintf("watched PR %s waited %s for its first summary, over %s", nodeOf(i), first, trackedFirstBound))
		}
		prev := warm
		for _, at := range append(slices.Clone(ts), r.end) {
			if at.Before(warm) {
				continue
			}
			if gap := at.Sub(prev); gap > trackedGapBound {
				bad = append(bad, fmt.Sprintf("watched PR %s went %s without a refresh (from %s), over %s", nodeOf(i), gap, prev.Sub(r.t0), trackedGapBound))
				break
			}
			prev = at
		}
		if len(bad) >= 6 {
			break
		}
	}
	return bad
}

var allClasses = []string{"interactive", "invalidated", "query", "keepalive", "fill"}

// checkBacklogEmitted is property 4.
func checkBacklogEmitted(r *result) []string {
	var bad []string
	if want := int(trackedDuration/(sampleEvery*tick)) - 1; len(r.samples) < want {
		return []string{fmt.Sprintf("%d telemetry samples, want at least %d", len(r.samples), want)}
	}
	var peak, last int64
	for _, s := range r.samples {
		for _, c := range allClasses {
			if _, ok := s.backlog[c]; !ok {
				bad = append(bad, fmt.Sprintf("governor.backlog has no series for class %s at %s", c, s.at.Sub(r.t0)))
			}
		}
		peak, last = max(peak, s.backlog["keepalive"]), s.backlog["keepalive"]
		if !s.hasWatched || !s.hasLimit {
			bad = append(bad, fmt.Sprintf("watched.entities or tracked.limit unreported at %s", s.at.Sub(r.t0)))
		}
		if s.limit != trackedLimit {
			bad = append(bad, fmt.Sprintf("tracked.limit = %d at %s, want %d", s.limit, s.at.Sub(r.t0), trackedLimit))
		}
		if len(bad) >= 6 {
			return bad
		}
	}
	if peak < 100 {
		bad = append(bad, fmt.Sprintf("the keep-alive backlog peaked at %d, want at least 100 of the %d tracked-only PRs", peak, r.prs-r.members))
	}
	if last == 0 {
		bad = append(bad, "the keep-alive backlog was empty at the end: the budget did not bind")
	}
	// The tracked and watched counts settle once the first fetches have stored
	// every PR; from the second minute on they are the scenario's.
	for _, s := range r.samples {
		if s.at.Sub(r.t0) < 2*time.Minute {
			continue
		}
		wantTracked := map[string]int64{telemetry.ClassMembers: int64(r.members), telemetry.ClassOtherOpen: int64(r.prs - r.members), telemetry.ClassClosedGrace: 0}
		for class, want := range wantTracked {
			if s.tracked[class] != want {
				bad = append(bad, fmt.Sprintf("tracked.entities[%s] = %d at %s, want %d", class, s.tracked[class], s.at.Sub(r.t0), want))
			}
		}
		if want := int64(r.members + trackedDirect); s.watched != want {
			bad = append(bad, fmt.Sprintf("watched.entities = %d at %s, want %d", s.watched, s.at.Sub(r.t0), want))
		}
		if len(bad) >= 6 {
			break
		}
	}
	return bad
}

func trackedChecks(r *result) map[string][]string {
	return map[string][]string{
		"0 scale": func() []string {
			if r.prs != trackedPRs || r.members != trackedMembers {
				return []string{fmt.Sprintf("%d PRs and %d members, want %d and %d", r.prs, r.members, trackedPRs, trackedMembers)}
			}
			return nil
		}(),
		"1 spend":             append(checkSpend(r), checkBinds(r)...),
		"2 watched freshness": checkWatchedFresh(r),
		"3 backlog emitted":   checkBacklogEmitted(r),
		"4 interactive wait":  checkInteractive(r),
	}
}

func TestTrackedSetLoad(t *testing.T) {
	t.Parallel()
	start := time.Now()
	r := newHarness(t, trackedOpts()).run()
	t.Logf("%s\n(wall clock: %s)", trackedSummary(r), time.Since(start).Round(time.Millisecond))
	checks := trackedChecks(r)
	keys := make([]string, 0, len(checks))
	for k := range checks {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		for _, msg := range checks[k] {
			t.Errorf("property %s: %s", k, msg)
		}
	}
	if len(r.errs) > 0 {
		t.Errorf("the daemon logged errors the run survived: %v", r.errs[:min(5, len(r.errs))])
	}
}

func trackedSummary(r *result) string {
	var peak int64
	for _, s := range r.samples {
		peak = max(peak, s.backlog["keepalive"])
	}
	last := metricSample{}
	if n := len(r.samples); n > 0 {
		last = r.samples[n-1]
	}
	return fmt.Sprintf("%d tracked PRs (%d watched members, %d watched directly), %d fake minutes\n"+
		"keep-alive backlog: peak %d, at the end %d; depth at the end %d; tracked %v; watched %d; limit %d\n%s",
		r.prs, r.members, trackedDirect, int(r.end.Sub(r.t0).Minutes()),
		peak, last.backlog["keepalive"], last.depth["keepalive"], last.tracked, last.watched, last.limit, r.driver)
}

// The checks are shown by deliberately wrong inputs: each MUST report the
// defect it is for.

func TestTrackedChecksRejectAWatchedPRThatWaitedTooLong(t *testing.T) {
	r := &result{t0: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), members: 2, prs: 4}
	r.end = r.t0.Add(trackedDuration)
	at := func(m int) time.Time { return r.t0.Add(time.Duration(m) * time.Minute) }
	store := func(node int, minutes ...int) {
		for _, m := range minutes {
			r.stores = append(r.stores, storeRec{at: at(m), node: node, group: groups.Summary})
		}
	}
	every2 := []int{0, 2, 4, 6, 8, 10, 12, 14}
	// PR 0 is refreshed every two minutes; PR 1 is quiet from minute 9 to 15.
	store(0, every2...)
	store(1, 0, 1, 9, 15)
	bad := checkWatchedFresh(r)
	if len(bad) != 1 {
		t.Fatalf("findings = %v, want exactly the one PR with a six-minute gap after the warm-up", bad)
	}
	// A gap before the warm-up is the cold start, not a finding.
	r.stores = nil
	store(0, every2...)
	store(1, 1, 7, 11, 14)
	if bad := checkWatchedFresh(r); len(bad) != 0 {
		t.Errorf("a gap inside the cold start was reported: %v", bad)
	}
	// A silence that runs to the end of the run is a gap.
	r.stores = nil
	store(0, every2...)
	store(1, 0, 2, 4, 6, 8)
	if bad := checkWatchedFresh(r); len(bad) != 1 {
		t.Errorf("a PR silent from minute 8 to the end: findings = %v, want one", bad)
	}
	// A watched PR with no refresh at all, or a late first one, is reported.
	r.stores = nil
	store(0, every2...)
	if bad := checkWatchedFresh(r); len(bad) != 1 {
		t.Errorf("a watched PR never stored: findings = %v, want one", bad)
	}
	store(1, 3, 6, 9, 12, 15)
	if bad := checkWatchedFresh(r); len(bad) == 0 {
		t.Error("a first refresh after three minutes was not reported")
	}
}

func TestTrackedChecksRejectAnUnemittedOrEmptyBacklog(t *testing.T) {
	t0 := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	full := func(keepalive int64) metricSample {
		b := map[string]int64{}
		for _, c := range allClasses {
			b[c] = 0
		}
		b["keepalive"] = keepalive
		return metricSample{backlog: b, tracked: map[string]int64{}, hasWatched: true, hasLimit: true, limit: trackedLimit}
	}
	build := func(mutate func(i int, s *metricSample)) *result {
		r := &result{t0: t0, members: trackedMembers, prs: trackedPRs, end: t0.Add(trackedDuration)}
		for i := 0; i < int(trackedDuration/(sampleEvery*tick)); i++ {
			s := full(150)
			s.at = t0.Add(time.Duration(i+1) * sampleEvery * tick)
			s.tracked = map[string]int64{telemetry.ClassMembers: trackedMembers, telemetry.ClassOtherOpen: trackedPRs - trackedMembers, telemetry.ClassClosedGrace: 0}
			s.watched = trackedMembers + trackedDirect
			if mutate != nil {
				mutate(i, &s)
			}
			r.samples = append(r.samples, s)
		}
		return r
	}
	if bad := checkBacklogEmitted(build(nil)); len(bad) != 0 {
		t.Fatalf("the well-formed samples were rejected: %v", bad)
	}
	for name, mutate := range map[string]func(int, *metricSample){
		"a class without a backlog series": func(_ int, s *metricSample) { delete(s.backlog, "fill") },
		"a backlog that never rises":       func(_ int, s *metricSample) { s.backlog["keepalive"] = 3 },
		"a backlog empty at the end": func(i int, s *metricSample) {
			if i >= 40 {
				s.backlog["keepalive"] = 0
			}
		},
		"no limit gauge":        func(_ int, s *metricSample) { s.hasLimit = false },
		"a wrong tracked count": func(_ int, s *metricSample) { s.tracked[telemetry.ClassMembers] = 99 },
		"a wrong watched count": func(_ int, s *metricSample) { s.watched = 5 },
	} {
		if bad := checkBacklogEmitted(build(mutate)); len(bad) == 0 {
			t.Errorf("%s was not reported", name)
		}
	}
	if bad := checkBacklogEmitted(&result{t0: t0}); len(bad) == 0 {
		t.Error("a run with no telemetry samples was not reported")
	}
}
