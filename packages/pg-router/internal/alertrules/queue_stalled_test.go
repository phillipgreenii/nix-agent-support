package alertrules

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"
)

// pg-router-queue-stalled: a backlog with nothing processing it (operator ruling
// 2026-10-12: "fire if queue > 0 and nothing is being processed"). A type has a
// backlog when pg_router_queue_depth held on every scrape of a 10m window; nothing
// processes it when the role bound to the type had no dispatch in flight
// (pg_router_listener_in_flight) for a sustained stretch of that window, and none
// settled in it. A declined offer is in flight for the second or two the handler
// takes to decline it, and a decline is not processing (pg2-9nfla), so a stretch
// counts only when it spans stalledBusyRun consecutive scrapes. The expression is
// modelled here (no PromQL engine is a dependency of this module; the real PromQL
// is exercised by grafana/alerting/rule-tests/pg-router-queue-stalled.test.yaml) and
// replayed over testdata/drain_pg2_2026-10-12.txt, a recording of the drain.pg2 lane,
// and testdata/drain_pg2_2026-10-13.txt, the same lane through a decline storm.

const (
	stalledWindow     = 10 * time.Minute
	stalledMinSamples = 18 // 90% of the 20 samples a 30s scrape yields over 10m
	stalledFor        = 5 * time.Minute
	stalledKeep       = 10 * time.Minute
	// stalledBusyRun is the in-flight stretch that counts as a dispatch running: the
	// 3m lookback min_over_time(pg_router_listener_in_flight[3m]) is 1 only when the
	// last 6 scrapes (30s apart) all read 1. A decline is decided in about 2s, so a
	// storm of them (one every 13s) leaves a handful of consecutive scrapes in flight
	// at worst: 2 in Prometheus's own record of the 2026-10-13 storm (17 runs of 1, one
	// of them a pair; the promtool case of that series is in rule-tests), and 4 at one
	// scrape phase of 30 under the replay model, which is wider than the scrapes that
	// really read 1. Never 6.
	stalledBusyRun = 6

	laneFixture  = "testdata/drain_pg2_2026-10-12.txt"
	stormFixture = "testdata/drain_pg2_2026-10-13.txt"
)

// laneEnd is the last instant of the recorded day at which the host is known to
// have been awake: the router event log goes quiet at 00:25:00Z on 2026-10-13 and
// the dark wakes that follow are not modelled, so the replays stop there rather
// than treat hours of sleep as hours of idle consumer.
var laneEnd = parseUTC("2026-10-13T00:25:00Z")

// stalledRoles is the type-to-role table the rule's label_replace chain encodes:
// one entry per role of the daemon config of 2026-10-12 (16 roles), in the order of
// the chain. desk-pr binds two types, hence the alternation in its regex. A role
// added to the config MUST be added here and to the rule.
var stalledRoles = []struct{ typeRE, role string }{
	{"drain[.]zr", "drain-zr"},
	{"drain[.]pg2", "drain-pg2"},
	{"review[.]ready", "review"},
	{"feedback[.]ready", "feedback"},
	{"work[.]ready", "worker"},
	{"pr[.](changed|reconcile)", "desk-pr"},
	{"issue[.]changed", "desk-issue"},
	{"thread[.]changed", "desk-thread"},
	{"desk[.]heartbeat", "desk-heartbeat"},
	{"desk[.]reconcile", "desk-reconcile"},
	{"pg-router-probe[.]tick", "pg-router-probe"},
	{"ccpool-probe[.]tick", "ccpool-probe"},
	{"disk[.]check", "disk-watchdog"},
	{"escalated[.]pg2", "pg2-escalation-triager"},
	{"escalated[.]zr", "zr-escalation-triager"},
	{"split-review[.]pg2", "pg2-split-triager"},
}

// stalledTypes is every event type the daemon config of 2026-10-12 binds, with the
// role that binds it.
var stalledTypes = map[string]string{
	"drain.zr":             "drain-zr",
	"drain.pg2":            "drain-pg2",
	"review.ready":         "review",
	"feedback.ready":       "feedback",
	"work.ready":           "worker",
	"pr.changed":           "desk-pr",
	"pr.reconcile":         "desk-pr",
	"issue.changed":        "desk-issue",
	"thread.changed":       "desk-thread",
	"desk.heartbeat":       "desk-heartbeat",
	"desk.reconcile":       "desk-reconcile",
	"pg-router-probe.tick": "pg-router-probe",
	"ccpool-probe.tick":    "ccpool-probe",
	"disk.check":           "disk-watchdog",
	"escalated.pg2":        "pg2-escalation-triager",
	"escalated.zr":         "zr-escalation-triager",
	"split-review.pg2":     "pg2-split-triager",
}

// wantStalledExpr is the expr of rule pg-router-queue-stalled, built from the table
// above so a role cannot be in one and missing from the other.
func wantStalledExpr() string {
	var b strings.Builder
	b.WriteString("max without (role) (")
	b.WriteString(strings.Repeat("label_replace(", len(stalledRoles)))
	b.WriteString(`min_over_time(pg_router_queue_depth[10m]) > 0 and count_over_time(pg_router_queue_depth[10m]) >= 18`)
	for _, r := range stalledRoles {
		fmt.Fprintf(&b, `, "role", %q, "type", %q)`, r.role, r.typeRE)
	}
	b.WriteString(` and on(role) (max by (role) (max_over_time(min_over_time(pg_router_listener_in_flight[3m])[10m:30s])) == 0) unless on(type) (sum by (type) (increase(pg_router_throughput_total[10m])) > 0)) unless on() (max(last_over_time(pg_router_gates_active[10m])) > 0)`)
	return b.String()
}

// stalledCond models the expression at instant t. The depth gauge has no series
// while a type is drained, so "above 0 on every scrape" is "present on at least 18
// of the 20"; the in-flight gauge is present on every scrape; and a gate series
// anywhere in the window suppresses every type.
func stalledCond(series []obs, t time.Time) bool {
	return stalledCondMin(series, t, stalledMinSamples)
}

// stalledCondMin is stalledCond with the presence guard's threshold as a parameter,
// so a test can show what the guard buys by weakening it.
func stalledCondMin(series []obs, t time.Time, minPresent int) bool {
	return stalledCondWith(series, t, minPresent, stalledBusyRun)
}

// stalledCondRun is stalledCond with the in-flight stretch that counts as running as
// a parameter. run 1 is the test the rule had before pg2-9nfla (the role was in
// flight on ANY scrape of the window), which reads a declined offer as activity.
func stalledCondRun(series []obs, t time.Time, run int) bool {
	return stalledCondWith(series, t, stalledMinSamples, run)
}

// stalledCondWith evaluates the expression at t. A scrape's `settled` is whether the
// throughput counter advanced since the previous scrape (a dispatch settled in it);
// increase() over the window is above 0 when any scrape has it.
func stalledCondWith(series []obs, t time.Time, minPresent, run int) bool {
	present, gated, completed := 0, false, false
	for _, o := range obsIn(series, t.Add(-stalledWindow), t) {
		if o.depth > 0 {
			present++
		}
		gated = gated || o.gate
		completed = completed || o.settled
	}
	return present >= minPresent && !inFlightRun(series, t, run) && !completed && !gated
}

// inFlightRun reports whether the role read in flight on run consecutive scrapes
// that completed inside the window ending at t:
// max_over_time(min_over_time(pg_router_listener_in_flight[2m])[10m:30s]) is above 0.
// The subquery looks back from each of its steps, so a stretch that began before the
// window still counts while its run completes inside it, and the role has to stay
// quiet for the window after the stretch's LAST scrape, as before. Scrapes more than
// a scrape interval apart (a sleep between them) are not consecutive.
func inFlightRun(series []obs, t time.Time, run int) bool {
	n := 0
	var prev time.Time
	for _, o := range obsIn(series, t.Add(-stalledWindow-time.Duration(run)*scrape), t) {
		switch {
		case o.inFlight == 0:
			n = 0
		case n > 0 && o.at.Sub(prev) <= scrape+time.Second:
			n++
		default:
			n = 1
		}
		prev = o.at
		if n >= run && o.at.After(t.Add(-stalledWindow)) {
			return true
		}
	}
	return false
}

func replayStalled(series []obs, awake func(time.Time) bool, from, to time.Time) map[time.Time]alertState {
	return replayAlert(func(t time.Time) bool { return stalledCond(series, t) }, awake, from, to, stalledFor, stalledKeep)
}

// legacyStalledCond models the completion-based expression this rule had before
// the 2026-10-12 ruling (min_over_time(depth[30m]) >= 10 and count_over_time >= 54
// unless a throughput increase in 30m, `for: 30m`, keepFiringFor 30m). It exists
// only so the replay below can prove the fixture reproduces the false positive
// that ruling removed; the live rule is stalledCond.
func legacyStalledCond(series []obs, accepts []time.Time, t time.Time) bool {
	present, low := 0, false
	for _, o := range obsIn(series, t.Add(-30*time.Minute), t) {
		if o.depth > 0 {
			present++
			low = low || o.depth < 10
		}
	}
	if present < 54 || low {
		return false
	}
	// increase() reads the counter between the window's first scrape and its last.
	for _, a := range accepts {
		if a.After(t.Add(-30*time.Minute+scrape)) && !a.After(t) {
			return false
		}
	}
	return true
}

func acceptTimes(l lane) []time.Time {
	var out []time.Time
	for _, d := range l.disp {
		out = append(out, d.to)
	}
	return out
}

func TestStalledRuleMatchesModel(t *testing.T) {
	r := ruleBlock(t, "pg-router-queue-stalled")
	if got, want := ruleExpr(t, r), wantStalledExpr(); got != want {
		t.Errorf("queue-stalled expr drifted from the modelled one:\n got %q\nwant %q", got, want)
	}
	// keepFiringFor is camelCase: file provisioning ignores keep_firing_for.
	for _, need := range []string{
		"for: 5m", "keepFiringFor: 10m", "noDataState: OK", "execErrState: Error",
		"severity: warning", "condition: C", "type: gt", "params: [0]", "reducer: last", "from: 600",
		"title: pg-router queue is stalled (by type)", "{{ $labels.type }}",
	} {
		if !strings.Contains(r, need) {
			t.Errorf("queue-stalled rule lost %q", need)
		}
	}
	if strings.Contains(r, "keep_firing_for") {
		t.Error("snake_case keep_firing_for is silently ignored by file provisioning; use keepFiringFor")
	}
	// The completion-based form is gone: no 30m window and no depth floor of 10. The
	// throughput counter appears only as the short-dispatch guard (a 10m window).
	expr := ruleExpr(t, r)
	for _, gone := range []string{"[30m]", ">= 10 and", "increase(pg_router_throughput_total[30m])"} {
		if strings.Contains(expr, gone) {
			t.Errorf("queue-stalled expr still carries the completion-based %q", gone)
		}
	}
	if !strings.Contains(expr, "unless on(type) (sum by (type) (increase(pg_router_throughput_total[10m])) > 0)") {
		t.Error("queue-stalled lost its short-dispatch guard (a dispatch that settled in the window is processing)")
	}
	// pg2-9nfla: a declined attempt is in flight for the second or two it takes to
	// decline it, so ANY in-flight scrape of the window cannot count as activity; the
	// role has to be in flight on consecutive scrapes (a 3m lookback) for that.
	if strings.Contains(expr, "max_over_time(pg_router_listener_in_flight[10m])") {
		t.Error("queue-stalled reads any in-flight scrape as activity, which a decline storm abuses (pg2-9nfla)")
	}
	if !strings.Contains(expr, "max_over_time(min_over_time(pg_router_listener_in_flight[3m])[10m:30s])") {
		t.Error("queue-stalled lost its sustained in-flight test (a dispatch running, not a declined attempt)")
	}
	i, j := strings.Index(r, "annotations:"), strings.Index(r, "data:")
	if i < 0 || j < i {
		t.Fatal("annotations block not found")
	}
	if ann := r[i:j]; strings.Contains(ann, "throughput") || strings.Contains(ann, "did not advance") {
		t.Errorf("queue-stalled annotations still describe the completion-based rule: %q", ann)
	}
	// Fingerprint continuity: the helper `role` label is dropped again, so the alert
	// keeps the labels of the rule it replaced and its open bead keeps matching.
	if !strings.HasPrefix(expr, "max without (role) (") {
		t.Error("queue-stalled must drop its helper role label (max without (role)) to keep its alert fingerprint")
	}
}

// The label_replace chain is the type-to-role join. Apply its clauses the way
// PromQL does (the regex is anchored, the first label_replace to match sets role,
// later ones leave a series that already has one) and pin the mapping for every
// type the daemon binds, so a clause cannot drift from the config it mirrors.
func TestStalledTypeToRoleTable(t *testing.T) {
	expr := ruleExpr(t, ruleBlock(t, "pg-router-queue-stalled"))
	if got := strings.Count(expr, "label_replace("); got != len(stalledRoles) {
		t.Fatalf("expr has %d label_replace clauses, the table has %d roles", got, len(stalledRoles))
	}
	clauses := regexp.MustCompile(`, "role", "([^"]+)", "type", "([^"]+)"\)`).FindAllStringSubmatch(expr, -1)
	if len(clauses) != len(stalledRoles) {
		t.Fatalf("parsed %d clauses, want %d", len(clauses), len(stalledRoles))
	}
	roleFor := func(typ string) string {
		for _, c := range clauses {
			if regexp.MustCompile("^(?:" + c[2] + ")$").MatchString(typ) {
				return c[1]
			}
		}
		return ""
	}
	for typ, want := range stalledTypes {
		if got := roleFor(typ); got != want {
			t.Errorf("type %q maps to role %q, want %q", typ, got, want)
		}
	}
	roles := map[string]bool{}
	for _, role := range stalledTypes {
		roles[role] = true
	}
	if len(roles) != len(stalledRoles) {
		t.Errorf("the config has %d distinct roles, the rule's table has %d", len(roles), len(stalledRoles))
	}
	// Anchoring: a type that merely contains or extends a bound one is not mapped.
	for _, typ := range []string{"drain.other", "xdrain.pg2", "drain.pg2.extra", "pr.changed2", "desk.other", "review", ""} {
		if got := roleFor(typ); got != "" {
			t.Errorf("unbound type %q maps to role %q, want none", typ, got)
		}
	}
	// A bound type matches exactly one clause.
	for typ := range stalledTypes {
		n := 0
		for _, c := range clauses {
			if regexp.MustCompile("^(?:" + c[2] + ")$").MatchString(typ) {
				n++
			}
		}
		if n != 1 {
			t.Errorf("type %q matches %d clauses, want exactly 1", typ, n)
		}
	}
}

// The headline case of the 2026-10-12 ruling. The drain.pg2 dispatch of 17:47:55Z to
// 18:53:11Z (63.7 minutes, healthy: transcript advancing, gates empty) held depth at
// 10 or more throughout; the completion-based rule paged for it from 18:48Z. The new
// rule never evaluates true while a dispatch is in flight.
func TestStalledHealthyLongDispatchDoesNotPage(t *testing.T) {
	l := loadLane(t, laneFixture)
	from, to := parseUTC("2026-10-12T17:00:00Z"), parseUTC("2026-10-12T19:30:00Z")
	series := l.scrapes(from.Add(-time.Hour), to)

	// Non-vacuity: the fixture holds the depth floor and reproduces the old page at
	// 18:48Z (starts_at 2026-10-12T18:48:00Z in the alert record).
	legacy := replayAlert(func(t time.Time) bool { return legacyStalledCond(series, acceptTimes(l), t) },
		awakeIn(l), from, to, 30*time.Minute, 30*time.Minute)
	first, ok := firstWhere(legacy, from, to, isFiring)
	if !ok || first.Before(parseUTC("2026-10-12T18:46:00Z")) || first.After(parseUTC("2026-10-12T18:50:00Z")) {
		t.Fatalf("the completion-based rule first fired at %v (found=%v); the alert record says 18:48:00Z", first, ok)
	}
	if d := l.depthAt(parseUTC("2026-10-12T18:30:00Z")); d < 10 {
		t.Fatalf("depth during the healthy dispatch is %d, want >= 10", d)
	}

	m := replayStalled(series, awakeIn(l), from, to)
	if tm, bad := firstWhere(m, from, to, condTrue); bad {
		t.Errorf("the 63.7 minute healthy dispatch evaluated true at %s", tm.Format("15:04:05"))
	}
	if tm, bad := firstWhere(m, from, to, isFiring); bad {
		t.Errorf("the 63.7 minute healthy dispatch paged at %s", tm.Format("15:04:05"))
	}
}

// Replay of the whole recorded day. Every long dispatch (4h50m across a sleep, 63.7,
// 69.7 and 44.9 minutes) is quiet. The one stretch where the lane really sat idle
// with a backlog while the host was awake and other roles were dispatching,
// 23:03:11Z to 23:33:06Z (about 20 events queued), pages once, 15 to 20 minutes in.
func TestStalledRecordedDay(t *testing.T) {
	l := loadLane(t, laneFixture)
	from := parseUTC("2026-10-12T08:32:00Z")
	series := l.scrapes(from, laneEnd)
	m := replayStalled(series, awakeIn(l), from, laneEnd)

	idleFrom, idleTo := parseUTC("2026-10-12T23:03:11Z"), parseUTC("2026-10-12T23:33:06Z")
	if tm, bad := firstWhere(m, from, idleFrom.Add(9*time.Minute), condTrue); bad {
		t.Errorf("condition true at %s, before the idle stretch had lasted a window", tm.Format("15:04:05"))
	}
	firstCond, ok := firstWhere(m, from, laneEnd, condTrue)
	if !ok || firstCond.Before(idleFrom.Add(9*time.Minute)) || firstCond.After(idleFrom.Add(12*time.Minute)) {
		t.Fatalf("first true condition at %v (found=%v), want 9-12 minutes into the idle stretch", firstCond, ok)
	}
	firstFire, ok := firstWhere(m, from, laneEnd, isFiring)
	if !ok || firstFire.Before(idleFrom.Add(14*time.Minute)) || firstFire.After(idleFrom.Add(20*time.Minute)) {
		t.Fatalf("first page at %v (found=%v), want 14-20 minutes into the idle stretch", firstFire, ok)
	}
	// The condition stops with the idle stretch: the next dispatch is in flight from
	// 23:33:06Z and counts as running once it has been in flight on stalledBusyRun
	// scrapes, two minutes on. The alert resolves keepFiringFor later; it never fires
	// again.
	runs := time.Duration(stalledBusyRun) * scrape
	lastCond, _ := lastWhere(m, from, laneEnd, condTrue)
	if lastCond.After(idleTo.Add(runs + evalEvery)) {
		t.Errorf("condition still true at %s, after the lane resumed at %s", lastCond.Format("15:04:05"), idleTo.Format("15:04:05"))
	}
	lastFire, _ := lastWhere(m, from, laneEnd, isFiring)
	if lastFire.Before(idleTo) || lastFire.After(idleTo.Add(runs+stalledKeep+2*time.Minute)) {
		t.Errorf("last firing evaluation at %s, want within keepFiringFor of the resume at %s", lastFire.Format("15:04:05"), idleTo.Format("15:04:05"))
	}
	if tm, bad := firstWhere(m, idleTo.Add(runs+stalledKeep+3*time.Minute), laneEnd, isFiring); bad {
		t.Errorf("fired again at %s", tm.Format("15:04:05"))
	}
}

// The decline storm of 2026-10-13 (pg2-9nfla; found by pg2-hyeno, the post-apply
// verification of this rule): the account's five-hour usage limit made the handler
// decline every drain-pg2 offer from 20:54:26Z to 21:59:59Z (one every 13 seconds,
// each decided in about two), while 19 to 24 events waited and nothing was accepted
// from 20:53:50Z until the dispatch that went in flight at 22:00:10Z settled at
// 22:02:39Z. A declined attempt is not processing, but it is in flight, so a scrape
// that landed inside one read the role as busy and restarted the 10 minute window: the
// rule as shipped held its condition for about 7 of those 69 minutes.
//
// The recording has the declines (R) and the accepts (D); the in-flight series is
// replayed at every scrape phase, because which scrapes a decline catches depends on
// where the scrape grid falls and the real grid (:14 and :44) is only one of 30. Its
// replays stop at 22:29:00Z: the dispatch in flight at the end of the recording has no
// record.
var (
	stormScrapeStart = parseUTC("2026-10-13T20:30:00Z")
	stormEnd         = parseUTC("2026-10-13T22:29:00Z")
	stormResume      = parseUTC("2026-10-13T22:00:10Z") // the dispatch accepted at 22:02:39Z goes in flight
)

// The model has to catch the scrapes Prometheus caught, or the replay below proves
// nothing about the storm: the five samples of pg_router_listener_in_flight{role="drain-pg2"}
// that read 1 between 21:09Z and 21:34Z (the verification read them off the live
// series), each 0 to 2 seconds before a logged decline.
func TestStormModelCatchesTheScrapesPrometheusCaught(t *testing.T) {
	l := loadLane(t, stormFixture)
	for _, hms := range []string{"21:15:44", "21:16:14", "21:21:14", "21:29:14", "21:33:44"} {
		if at := atDay("2026-10-13", hms); !l.inFlightAt(at) {
			t.Errorf("the scrape at %s read in flight in Prometheus, the model has the role idle", at.Format("15:04:05"))
		}
	}
	n := 0
	for _, r := range l.declines {
		if !r.Before(parseUTC("2026-10-13T20:54:26Z")) && !r.After(parseUTC("2026-10-13T21:59:59Z")) {
			n++
		}
	}
	if n < 293 {
		t.Errorf("%d declines recorded in the storm, the verification counted 293 usage-limit declines", n)
	}
	// Non-vacuity of the lane itself: a backlog of 19 to 24 events throughout.
	for at := parseUTC("2026-10-13T20:56:00Z"); at.Before(parseUTC("2026-10-13T22:00:00Z")); at = at.Add(time.Minute) {
		if d := l.depthAt(at); d < 15 || d > 30 {
			t.Fatalf("depth at %s is %d, want the recorded 19 to 24 (give or take the re-emits in flight)", at.Format("15:04:05"), d)
		}
	}
}

func TestStalledDeclineStormHoldsTheConditionUntilDispatchesResume(t *testing.T) {
	l := loadLane(t, stormFixture)
	// The throughput step of the 20:53:50Z accept is in the window until 21:04:20Z at
	// the latest (the first scrape after it is at most 30s later); the next dispatch is
	// in flight from 22:00:10Z and counts as running 90 to 120 seconds into it, so the
	// condition holds on every evaluation from 21:05:00Z to 22:00:00Z.
	holdFrom, holdTo := parseUTC("2026-10-13T21:05:00Z"), parseUTC("2026-10-13T22:00:00Z")
	from := parseUTC("2026-10-13T20:45:00Z")

	// The storm never reads as a running dispatch: a few consecutive scrapes at worst
	// are in flight, never stalledBusyRun, whatever the scrape phase.
	maxRun := 0
	for phase := time.Duration(0); phase < scrape; phase += time.Second {
		series := l.scrapes(stormScrapeStart.Add(phase), stormEnd)
		if run := longestRun(series, parseUTC("2026-10-13T20:54:00Z"), parseUTC("2026-10-13T21:59:50Z")); run > maxRun {
			maxRun = run
		}
	}
	t.Logf("longest stretch of consecutive in-flight scrapes inside the storm, over all 30 scrape phases: %d (a running dispatch needs %d)", maxRun, stalledBusyRun)
	if maxRun < 2 || maxRun >= stalledBusyRun {
		t.Errorf("the storm reads in flight on up to %d consecutive scrapes, want 2 to %d (more than 1 or the model is too narrow to be a test; under %d or the rule reads the storm as a dispatch)", maxRun, stalledBusyRun-1, stalledBusyRun)
	}

	for phase := time.Duration(0); phase < scrape; phase += time.Second {
		series := l.scrapes(stormScrapeStart.Add(phase), stormEnd)
		m := replayStalled(series, awakeIn(l), from, stormEnd)

		for e := holdFrom; !e.After(holdTo); e = e.Add(evalEvery) {
			if !m[e].cond {
				t.Fatalf("phase %s: condition false at %s, inside the idle stretch (last accept 20:53:50Z, next dispatch in flight from %s)",
					phase, e.Format("15:04:05"), stormResume.Format("15:04:05"))
			}
		}
		// It pages once `for` has elapsed on the first true evaluation (the window clears
		// of the 20:53:50Z accept's throughput step 10 minutes after the first scrape that
		// saw it, 21:04:00Z to 21:04:30Z) and keeps paging until the dispatch is running.
		first, ok := firstWhere(m, from, stormEnd, isFiring)
		if !ok || first.Before(parseUTC("2026-10-13T21:08:30Z")) || first.After(parseUTC("2026-10-13T21:10:30Z")) {
			t.Fatalf("phase %s: first page at %v (found=%v), want about 21:09Z (the verification's estimate: 21:09Z to 22:10Z)", phase, first, ok)
		}
		for e := first; !e.After(holdTo); e = e.Add(evalEvery) {
			if !m[e].firing {
				t.Fatalf("phase %s: alert not firing at %s, it resolved inside the idle stretch", phase, e.Format("15:04:05"))
			}
		}
		// Once the dispatch is running the condition ends and the alert resolves
		// keepFiringFor later.
		if tm, bad := firstWhere(m, stormResume.Add(3*time.Minute), stormEnd, condTrue); bad {
			t.Fatalf("phase %s: condition true at %s, with a dispatch running since %s", phase, tm.Format("15:04:05"), stormResume.Format("15:04:05"))
		}
		if last, _ := lastWhere(m, from, stormEnd, isFiring); last.After(stormResume.Add(3*time.Minute + stalledKeep + evalEvery)) {
			t.Fatalf("phase %s: still firing at %s", phase, last.Format("15:04:05"))
		}
	}
}

// What the rule did with the same recording before pg2-9nfla: a scrape in flight on
// ANY scrape of the window (run 1) restarts it, so the lane read busy through almost
// all of the idle stretch. The verification saw the condition hold from 21:44Z to
// about 21:51Z (Pending 21:44, Alerting 21:49, Recovering 21:51); on the real scrape
// grid (:14 and :44) the model reads the same few minutes, and the replay above fails
// against this variant of the model.
func TestStalledDeclineStormHeldTheOldConditionForAFewMinutesOnly(t *testing.T) {
	l := loadLane(t, stormFixture)
	series := l.scrapes(stormScrapeStart.Add(14*time.Second), stormEnd)
	trueMinutes, total := 0, 0
	for e := parseUTC("2026-10-13T21:05:00Z"); !e.After(parseUTC("2026-10-13T22:00:00Z")); e = e.Add(evalEvery) {
		total++
		if stalledCondRun(series, e, 1) {
			trueMinutes++
		}
	}
	t.Logf("the pre-pg2-9nfla condition held on %d of %d evaluations of the idle stretch", trueMinutes, total)
	if trueMinutes < 3 || trueMinutes > 12 {
		t.Errorf("the pre-pg2-9nfla condition held on %d of %d evaluations of the idle stretch, want a few (the verification saw about 7 of 69 minutes)", trueMinutes, total)
	}
	for e := parseUTC("2026-10-13T21:05:00Z"); !e.After(parseUTC("2026-10-13T22:00:00Z")); e = e.Add(evalEvery) {
		if !stalledCondRun(series, e, stalledBusyRun) {
			t.Fatalf("the sustained in-flight test is false at %s, inside the same stretch", e.Format("15:04:05"))
		}
	}
}

// longestRun is the longest stretch of consecutive scrapes in series[from..to] that
// read in flight.
func longestRun(series []obs, from, to time.Time) int {
	best, n := 0, 0
	for _, o := range obsIn(series, from, to) {
		if o.inFlight > 0 {
			n++
		} else {
			n = 0
		}
		if n > best {
			best = n
		}
	}
	return best
}

// A running dispatch is still processing, and a stretch that long is what the rule
// reads: in flight on 4 consecutive scrapes (two minutes) anywhere in the window
// keeps the condition false, 3 do not, and the rule goes back to the window of 10
// minutes after the stretch's last scrape.
func TestStalledSustainedInFlightStretchIsProcessing(t *testing.T) {
	start := parseUTC("2026-10-13T10:00:00Z")
	mk := func(stretch int) []obs {
		var series []obs
		for i := 0; i < 240; i++ { // 2h, backlog of 5, one stretch of `stretch` scrapes at 30 minutes
			o := obs{at: start.Add(time.Duration(i) * scrape), depth: 5}
			if i >= 60 && i < 60+stretch {
				o.inFlight = 1
			}
			series = append(series, o)
		}
		return series
	}
	// A short stretch (a declined offer, caught on 1 to 3 scrapes) never makes the
	// role look busy: with the backlog present since the start the condition is true
	// on every evaluation once the window holds 18 scrapes.
	for _, stretch := range []int{1, 2, stalledBusyRun - 1} {
		series := mk(stretch)
		for e := start.Add(10 * time.Minute); e.Before(start.Add(2 * time.Hour)); e = e.Add(evalEvery) {
			if !stalledCond(series, e) {
				t.Fatalf("a stretch of %d scrapes held the condition false at %s", stretch, e.Format("15:04:05"))
			}
		}
	}
	// A stretch of stalledBusyRun or more is a dispatch running: the condition is false
	// from the scrape that completes the run until the window has passed the stretch's
	// LAST scrape, and true again after it.
	for _, stretch := range []int{stalledBusyRun, stalledBusyRun + 2, 40} {
		series := mk(stretch)
		first, last := start.Add(time.Duration(60+stalledBusyRun-1)*scrape), start.Add(time.Duration(60+stretch-1)*scrape)
		if tm := first.Add(-2 * evalEvery); !stalledCond(series, tm) {
			t.Errorf("a stretch of %d scrapes held the condition false at %s, before it completed a run", stretch, tm.Format("15:04:05"))
		}
		for _, tm := range []time.Time{first.Add(10 * time.Second), last.Add(time.Minute), last.Add(stalledWindow - evalEvery)} {
			if stalledCond(series, tm) {
				t.Errorf("a stretch of %d scrapes left the condition true at %s, inside the window after its run", stretch, tm.Format("15:04:05"))
			}
		}
		if tm := last.Add(stalledWindow + evalEvery); !stalledCond(series, tm) {
			t.Errorf("a stretch of %d scrapes still holds the condition false at %s, a window after its last scrape", stretch, tm.Format("15:04:05"))
		}
	}
}

// Counterfactual on the same recorded day: the consumer stops after the healthy
// dispatch settles (18:53:11Z) while depth stays at 10 or more. That is the stall the
// rule exists for, and it pages once a window plus `for` has passed.
func TestStalledConsumerThatStopsPages(t *testing.T) {
	l := loadLane(t, laneFixture).without(parseUTC("2026-10-12T18:53:11Z"))
	from, to := parseUTC("2026-10-12T17:00:00Z"), parseUTC("2026-10-12T20:00:00Z")
	m := replayStalled(l.scrapes(from.Add(-time.Hour), to), awakeIn(l), from, to)
	stop := parseUTC("2026-10-12T18:53:11Z")
	first, ok := firstWhere(m, from, to, isFiring)
	if !ok || first.Before(stop.Add(14*time.Minute)) || first.After(stop.Add(18*time.Minute)) {
		t.Fatalf("a consumer that stopped at %s first paged at %v (found=%v), want 14-18 minutes later", stop.Format("15:04"), first, ok)
	}
	if !m[to].firing {
		t.Error("alert resolved while the consumer was still stopped")
	}
}

// Hand-offs between healthy dispatches are 0 to 150 seconds; a window of 10 minutes
// needs 18 scrapes with nothing in flight, so no hand-off ever evaluates true.
func TestStalledIgnoresHandOffsBetweenDispatches(t *testing.T) {
	var series []obs
	start := parseUTC("2026-10-12T10:00:00Z")
	for i := 0; i < 480; i++ { // 4h; a 20 minute dispatch, then 150s idle, repeated
		at := start.Add(time.Duration(i) * scrape)
		cycle := time.Duration(i) * scrape % (20*time.Minute + 150*time.Second)
		o := obs{at: at, depth: 12}
		if cycle < 20*time.Minute {
			o.inFlight = 1
		}
		series = append(series, o)
	}
	m := replayStalled(series, func(time.Time) bool { return true }, start.Add(15*time.Minute), start.Add(4*time.Hour))
	if tm, bad := firstWhere(m, start, start.Add(4*time.Hour), condTrue); bad {
		t.Fatalf("a 150s hand-off evaluated true at %s", tm.Format("15:04:05"))
	}
}

// A role whose dispatches last a second or two (desk-issue runs 0.4 to 7 s) is in
// flight on about 3% of 30s scrapes even while it drains a backlog at one event a
// minute, so the in-flight gauge alone would read it as idle. A dispatch that settled
// inside the window is processing too (the throughput guard), so the rule stays quiet.
func TestStalledPaceLimitedFastRoleIsNotIdle(t *testing.T) {
	var series []obs
	start := parseUTC("2026-10-12T10:00:00Z")
	for i := 0; i < 480; i++ { // 4h, backlog of 15, one 1s dispatch a minute, never caught in flight
		o := obs{at: start.Add(time.Duration(i) * scrape), depth: 15}
		o.settled = i%2 == 1
		series = append(series, o)
	}
	from, to := start.Add(15*time.Minute), start.Add(4*time.Hour)
	always := func(time.Time) bool { return true }
	m := replayStalled(series, always, from, to)
	if tm, bad := firstWhere(m, from, to, condTrue); bad {
		t.Fatalf("a busy, pace-limited consumer evaluated true at %s", tm.Format("15:04:05"))
	}
	// Non-vacuity: judged by the in-flight gauge alone it is idle and pages.
	inFlightOnly := replayAlert(func(t time.Time) bool {
		stripped := make([]obs, len(series))
		copy(stripped, series)
		for i := range stripped {
			stripped[i].settled = false
		}
		return stalledCond(stripped, t)
	}, always, from, to, stalledFor, stalledKeep)
	if _, ok := firstWhere(inFlightOnly, from, to, isFiring); !ok {
		t.Fatal("test bug: without the throughput guard this consumer should look idle and page")
	}
}

// A sleep is not a stall, and it cannot be carried into one. The backlog is idle
// for 12 minutes (the condition is true and Pending) and the host sleeps at 10:12;
// at the wake the Pending state from before the sleep must NOT fire on the first
// evaluation. The presence guard keeps the condition false until 18 fresh scrapes
// exist, and `for` is earned again afterwards (pg2-9uvm5's wake grace).
func TestStalledWakeAfterSleepDoesNotFireOnStalePending(t *testing.T) {
	start := parseUTC("2026-10-12T10:00:00Z")
	var l lane
	l.enq = []time.Time{start, start, start} // a backlog of 3, never dispatched
	l.asleep = []span{{start.Add(12*time.Minute + 30*time.Second), start.Add(3 * time.Hour)}}
	from, to := parseUTC("2026-10-12T09:50:00Z"), parseUTC("2026-10-12T13:30:00Z")
	series := l.scrapes(from.Add(-time.Hour), to)
	m := replayStalled(series, awakeIn(l), from, to)
	wake := parseUTC("2026-10-12T13:00:00Z")
	if tm, bad := firstWhere(m, wake, wake.Add(8*time.Minute), condTrue); bad {
		t.Fatalf("condition true at %s, inside the 10 minutes after the wake", tm.Format("15:04:05"))
	}
	if tm, bad := firstWhere(m, wake, wake.Add(13*time.Minute), isFiring); bad {
		t.Fatalf("fired at %s on a stale pre-sleep Pending state", tm.Format("15:04:05"))
	}
	first, ok := firstWhere(m, wake, to, isFiring)
	if !ok || first.Before(wake.Add(14*time.Minute)) || first.After(wake.Add(18*time.Minute)) {
		t.Fatalf("after the wake the still-idle lane first paged at %v (found=%v), want 14-18 minutes in", first, ok)
	}

	// Non-vacuity: with the presence guard weakened to one scrape the stale state
	// carries across the sleep and the alert is firing at the very first evaluation
	// after the wake.
	noGuard := replayAlert(func(t time.Time) bool { return stalledCondMin(series, t, 1) },
		awakeIn(l), from, to, stalledFor, stalledKeep)
	if tm, ok := firstWhere(noGuard, wake, to, isFiring); !ok || tm.After(wake.Add(time.Minute)) {
		t.Fatalf("test bug: the guard-less variant should fire at the wake, got %v (found=%v)", tm, ok)
	}
}

// A gate (SYSTEM_PAUSE or any other) halts every listener except its owner, so an
// idle lane with a backlog is expected while one is in force: nothing pages under it,
// the condition stays false for the 10 minutes the gate's last sample stays in the
// window, and the alert then fires normally if the lane is still idle.
func TestStalledGateSuppressesTheRule(t *testing.T) {
	start := parseUTC("2026-10-12T10:00:00Z")
	gateFrom, gateTo := start.Add(10*time.Minute), start.Add(70*time.Minute)
	var series []obs
	for i := 0; i < 360; i++ { // 3h, depth 4, nothing in flight, a gate for an hour
		at := start.Add(time.Duration(i) * scrape)
		series = append(series, obs{at: at, depth: 4, gate: !at.Before(gateFrom) && at.Before(gateTo)})
	}
	always := func(time.Time) bool { return true }
	m := replayStalled(series, always, start.Add(15*time.Minute), start.Add(3*time.Hour))
	if tm, bad := firstWhere(m, gateFrom, gateTo.Add(9*time.Minute), condTrue); bad {
		t.Fatalf("condition true at %s while a gate was in force or in its window", tm.Format("15:04:05"))
	}
	first, ok := firstWhere(m, gateTo, start.Add(3*time.Hour), isFiring)
	if !ok || first.Before(gateTo.Add(14*time.Minute)) || first.After(gateTo.Add(18*time.Minute)) {
		t.Fatalf("after the gate ended the idle lane first paged at %v (found=%v), want 14-18 minutes later", first, ok)
	}
}

// pg2-p93c0's decision survives: a whole-system stall needs no aggregate rule. With
// nothing dispatching, desk.heartbeat (enqueued about once a minute) is itself an
// idle-with-backlog type and pages within a quarter of an hour.
func TestStalledWholeSystemStallPagesViaHeartbeat(t *testing.T) {
	var series []obs
	start := parseUTC("2026-10-12T06:00:00Z")
	for i := 0; i < 240; i++ { // 2h, one heartbeat queued a minute, nothing dispatched
		series = append(series, obs{at: start.Add(time.Duration(i) * scrape), depth: float64(1 + i/2)})
	}
	m := replayStalled(series, func(time.Time) bool { return true }, start.Add(time.Minute), start.Add(2*time.Hour))
	first, ok := firstWhere(m, start, start.Add(2*time.Hour), isFiring)
	if !ok || first.After(start.Add(20*time.Minute)) {
		t.Fatalf("whole-system stall first paged at %v (found=%v), want within 20 minutes", first, ok)
	}
}

// A type that blips into the queue for a scrape or two and drains again (pg2-xepg6)
// is not a backlog: the presence guard needs 18 of 20 scrapes.
func TestStalledIgnoresSparseBlips(t *testing.T) {
	var series []obs
	start := parseUTC("2026-10-12T10:00:00Z")
	for i := 0; i < 360; i++ {
		o := obs{at: start.Add(time.Duration(i) * scrape)}
		if i%20 == 0 { // one scrape of depth 12 every 10 minutes, drained in between
			o.depth = 12
		}
		series = append(series, o)
	}
	m := replayStalled(series, func(time.Time) bool { return true }, start.Add(11*time.Minute), start.Add(3*time.Hour))
	if tm, bad := firstWhere(m, start, start.Add(3*time.Hour), condTrue); bad {
		t.Fatalf("sparse blips evaluated true at %s", tm.Format("15:04:05"))
	}
}

// The alert's own text states the new semantics and nothing of the old.
func TestStalledAnnotationsDescribeIdleWithBacklog(t *testing.T) {
	r := ruleBlock(t, "pg-router-queue-stalled")
	i, j := strings.Index(r, "annotations:"), strings.Index(r, "data:")
	ann := r[i:j]
	for _, need := range []string{"has a backlog but nothing is processing it", "pg_router_listener_in_flight", "pg-router status", "gate"} {
		if !strings.Contains(ann, need) {
			t.Errorf("annotations lost %q", need)
		}
	}
}
