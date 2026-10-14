package alertrules

import (
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Shared machinery for the replays of the two lane rules, pg-router-queue-stalled
// and pg-router-queue-slow-growth: a recorded lane (testdata/drain_pg2_*.txt, format
// in that file's header), the scrape series Prometheus would have held for it, and
// Grafana's `for` / `keepFiringFor` state machine.

type span struct{ from, to time.Time }

// A declined offer (R) is in flight from the moment it is made until the handler
// has decided and the offer settles; the daemon logs the decision at the end of that
// stretch. The model is [t-declineBefore, t+declineAfter) around the logged second,
// the offsets that reproduce the five scrapes of the 2026-10-13 storm that read
// pg_router_listener_in_flight == 1 (testdata/drain_pg2_2026-10-13.txt, header).
const (
	declineBefore = 2 * time.Second
	declineAfter  = time.Second
)

// lane is the recorded history of one event type and the role that consumes it.
type lane struct {
	backlog  int         // events held when the recording starts, not arrivals (B)
	enq      []time.Time // events admitted (E)
	evict    []time.Time // events that left the queue (X)
	disp     []span      // dispatches: offer in flight from..to, settled (accepted) at `to` (D)
	declines []time.Time // offers the handler declined, as logged: in flight around the instant, never accepted (R)
	asleep   []span      // the host was asleep: no scrape in [from, to) (S)
}

func parseUTC(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func loadLane(t *testing.T, path string) lane {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var l lane
	for _, line := range strings.Split(string(b), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Fields(line)
		switch {
		case len(f) == 2 && f[0] == "E":
			l.enq = append(l.enq, parseUTC(f[1]))
		case len(f) == 2 && f[0] == "X":
			l.evict = append(l.evict, parseUTC(f[1]))
		case len(f) == 3 && f[0] == "D":
			l.disp = append(l.disp, span{parseUTC(f[1]), parseUTC(f[2])})
		case len(f) == 3 && f[0] == "S":
			l.asleep = append(l.asleep, span{parseUTC(f[1]), parseUTC(f[2])})
		case len(f) == 3 && f[0] == "B":
			n, err := strconv.Atoi(f[2])
			if err != nil || n < 0 {
				t.Fatalf("%s: bad backlog in %q", path, line)
			}
			l.backlog = n
		case len(f) == 2 && f[0] == "R":
			l.declines = append(l.declines, parseUTC(f[1]))
		default:
			t.Fatalf("%s: bad record %q", path, line)
		}
	}
	for _, s := range [][]time.Time{l.enq, l.evict} {
		if !sort.SliceIsSorted(s, func(i, j int) bool { return s[i].Before(s[j]) }) {
			t.Fatalf("%s: records are not in time order", path)
		}
	}
	return l
}

func (l lane) depthAt(t time.Time) int {
	n := l.backlog
	for _, e := range l.enq {
		if !e.After(t) {
			n++
		}
	}
	for _, x := range l.evict {
		if !x.After(t) {
			n--
		}
	}
	return n
}

func (l lane) inFlightAt(t time.Time) bool {
	for _, d := range l.disp {
		if !t.Before(d.from) && t.Before(d.to) {
			return true
		}
	}
	// A declined offer is in flight too, for as long as the decline takes to decide:
	// the gauge cannot tell it from a dispatch (pg2-9nfla).
	for _, r := range l.declines {
		if !t.Before(r.Add(-declineBefore)) && t.Before(r.Add(declineAfter)) {
			return true
		}
	}
	return false
}

func (l lane) asleepAt(t time.Time) bool {
	for _, s := range l.asleep {
		if !t.Before(s.from) && t.Before(s.to) {
			return true
		}
	}
	return false
}

// without returns the lane with every dispatch that settles after cut removed, as
// if its consumer had stopped at cut (a counterfactual on recorded data).
func (l lane) without(cut time.Time) lane {
	out := l
	out.disp = nil
	for _, d := range l.disp {
		if !d.to.After(cut) {
			out.disp = append(out.disp, d)
		}
	}
	return out
}

// obs is one scrape of the lane's series. depth 0 means the gauge has NO series
// (it observes only types present in the queue, pg2-xepg6); inFlight is observed
// for every registered role, so it is present on every scrape.
type obs struct {
	at       time.Time
	depth    float64
	inFlight float64
	gate     bool // pg_router_gates_active has a series (a gate is in force)
	settled  bool // pg_router_throughput_total advanced since the previous scrape
}

// scrapes is what a 30s scraper would have recorded in [from, to]: nothing while
// the host is asleep.
func (l lane) scrapes(from, to time.Time) []obs {
	var out []obs
	for t := from; !t.After(to); t = t.Add(scrape) {
		if l.asleepAt(t) {
			continue
		}
		o := obs{at: t}
		if d := l.depthAt(t); d > 0 {
			o.depth = float64(d)
		}
		if l.inFlightAt(t) {
			o.inFlight = 1
		}
		// A dispatch that settled in (t-scrape, t] advanced the throughput counter.
		for _, d := range l.disp {
			if d.to.After(t.Add(-scrape)) && !d.to.After(t) {
				o.settled = true
			}
		}
		out = append(out, o)
	}
	return out
}

// obsIn returns the scrapes in (from, to], the samples a range selector reads.
func obsIn(series []obs, from, to time.Time) []obs {
	var in []obs
	for _, o := range series {
		if o.at.After(from) && !o.at.After(to) {
			in = append(in, o)
		}
	}
	return in
}

// alertState is one evaluation of a rule: whether its condition held, and whether
// the alert is firing (Alerting, or held by keepFiringFor).
type alertState struct{ cond, firing bool }

// replayAlert evaluates cond every evalEvery while the host is awake (neither
// Grafana nor Prometheus runs asleep) and applies Grafana's state machine: a
// true condition is Pending since its first evaluation and Alerting once it has
// been true for forD of WALL CLOCK time (so a Pending state from before a sleep
// would fire on the first evaluation after the wake if the condition still held);
// a false evaluation resets Pending; a firing alert is kept firing for keepD after
// the condition was last true.
func replayAlert(cond func(time.Time) bool, awake func(time.Time) bool, from, to time.Time, forD, keepD time.Duration) map[time.Time]alertState {
	out := map[time.Time]alertState{}
	var pendingSince, lastTrue time.Time
	firing := false
	for t := from; !t.After(to); t = t.Add(evalEvery) {
		if !awake(t) {
			continue
		}
		c := cond(t)
		switch {
		case c:
			if pendingSince.IsZero() {
				pendingSince = t
			}
			if firing || t.Sub(pendingSince) >= forD {
				firing, lastTrue = true, t
			}
		default:
			pendingSince = time.Time{}
			if firing && t.Sub(lastTrue) > keepD {
				firing = false
			}
		}
		out[t] = alertState{c, firing}
	}
	return out
}

func firstWhere(m map[time.Time]alertState, from, to time.Time, pick func(alertState) bool) (time.Time, bool) {
	var first time.Time
	found := false
	for t, s := range m {
		if t.Before(from) || t.After(to) || !pick(s) {
			continue
		}
		if !found || t.Before(first) {
			first, found = t, true
		}
	}
	return first, found
}

func lastWhere(m map[time.Time]alertState, from, to time.Time, pick func(alertState) bool) (time.Time, bool) {
	var last time.Time
	found := false
	for t, s := range m {
		if t.Before(from) || t.After(to) || !pick(s) {
			continue
		}
		if !found || t.After(last) {
			last, found = t, true
		}
	}
	return last, found
}

func condTrue(s alertState) bool { return s.cond }
func isFiring(s alertState) bool { return s.firing }

func awakeIn(l lane) func(time.Time) bool {
	return func(t time.Time) bool { return !l.asleepAt(t) }
}
