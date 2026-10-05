package analyze

import (
	"fmt"
	"math/rand"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/phillipgreenii/beads-exporter/internal/bd"
	"github.com/phillipgreenii/beads-exporter/internal/metrics"
	"github.com/phillipgreenii/beads-exporter/internal/queue"
)

var now = time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

func ts(offset time.Duration) *time.Time {
	t := now.Add(offset)
	return &t
}

func set(ids ...string) map[string]struct{} {
	s := map[string]struct{}{}
	for _, id := range ids {
		s[id] = struct{}{}
	}
	return s
}

func TestCategorizeRuleTable(t *testing.T) {
	cases := []struct {
		name    string
		bead    bd.Bead
		ready   map[string]struct{}
		blocked map[string]struct{}
		want    State
	}{
		{"stored in_progress", bd.Bead{ID: "a", Status: "in_progress"}, nil, nil, InProgress},
		{"stored hooked", bd.Bead{ID: "a", Status: "hooked"}, nil, nil, InProgress},
		{"stored deferred", bd.Bead{ID: "a", Status: "deferred"}, nil, nil, Deferred},
		{"future defer_until", bd.Bead{ID: "a", Status: "open", DeferUntil: ts(time.Hour)}, nil, nil, Deferred},
		{"past defer_until is not deferred", bd.Bead{ID: "a", Status: "open", DeferUntil: ts(-time.Hour)}, set("a"), nil, Ready},
		{"defer_until equal to now is not deferred", bd.Bead{ID: "a", Status: "open", DeferUntil: ts(0)}, set("a"), nil, Ready},
		{"nil defer_until", bd.Bead{ID: "a", Status: "open"}, set("a"), nil, Ready},
		{"in bd blocked", bd.Bead{ID: "a", Status: "open"}, nil, set("a"), Blocked},
		{"stored blocked but not in bd blocked", bd.Bead{ID: "a", Status: "blocked"}, nil, nil, Blocked},
		{"in bd ready", bd.Bead{ID: "a", Status: "open"}, set("a"), nil, Ready},
		{"merge-request tracking", bd.Bead{ID: "a", Status: "open", IssueType: "merge-request"}, nil, nil, Tracking},
		{"molecule tracking", bd.Bead{ID: "a", Status: "open", IssueType: "molecule"}, nil, nil, Tracking},
		{"merge-request in ready stays ready", bd.Bead{ID: "a", Status: "open", IssueType: "merge-request"}, set("a"), nil, Ready},
		{"pinned is other", bd.Bead{ID: "a", Status: "pinned", IssueType: "task"}, nil, nil, Other},
		{"custom status is other", bd.Bead{ID: "a", Status: "review", IssueType: "task"}, nil, nil, Other},
		{"open task in neither set is other", bd.Bead{ID: "a", Status: "open", IssueType: "task"}, nil, nil, Other},
		// Precedence between overlapping rules.
		{"in_progress with future defer_until stays in_progress", bd.Bead{ID: "a", Status: "in_progress", DeferUntil: ts(time.Hour)}, nil, nil, InProgress},
		{"deferred and blocked is deferred", bd.Bead{ID: "a", Status: "deferred"}, nil, set("a"), Deferred},
		{"future defer_until and blocked is deferred", bd.Bead{ID: "a", Status: "open", DeferUntil: ts(time.Hour)}, nil, set("a"), Deferred},
		{"blocked and ready is blocked", bd.Bead{ID: "a", Status: "open"}, set("a"), set("a"), Blocked},
		{"in_progress and blocked is in_progress", bd.Bead{ID: "a", Status: "in_progress"}, nil, set("a"), InProgress},
		{"blocked merge-request is blocked", bd.Bead{ID: "a", Status: "open", IssueType: "merge-request"}, nil, set("a"), Blocked},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Categorize(tc.bead, tc.ready, tc.blocked, now); got != tc.want {
				t.Fatalf("state = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestReadyExcludedTypesPinned(t *testing.T) {
	if got := ReadyExcludedTypes(); !reflect.DeepEqual(got, []string{"merge-request", "molecule"}) {
		t.Fatalf("ReadyExcludedTypes = %v", got)
	}
}

// TestCategorizeProperty checks, over many randomised populations, that every
// not-closed bead lands in exactly one state and that the states sum to the
// not-closed total.
func TestCategorizeProperty(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	statuses := []string{"open", "in_progress", "hooked", "blocked", "deferred", "pinned", "review"}
	types := []string{"task", "bug", "epic", "merge-request", ""}
	for round := 0; round < 200; round++ {
		n := rng.Intn(60)
		var beads []bd.Bead
		var ready, blocked []bd.Bead
		for i := 0; i < n; i++ {
			b := bd.Bead{
				ID:        fmt.Sprintf("alpha-%d", i),
				Status:    statuses[rng.Intn(len(statuses))],
				IssueType: types[rng.Intn(len(types))],
				Priority:  rng.Intn(5),
			}
			switch rng.Intn(3) {
			case 0:
				b.DeferUntil = ts(time.Duration(rng.Intn(48)-24) * time.Hour)
			}
			created := now.Add(-time.Duration(rng.Intn(1000)) * time.Hour)
			b.CreatedAt = &created
			beads = append(beads, b)
			if rng.Intn(2) == 0 {
				ready = append(ready, b)
			}
			if rng.Intn(4) == 0 {
				blocked = append(blocked, b)
			}
		}
		m := Summarize(Input{Beads: beads, Ready: ready, Blocked: blocked, Now: now, LabelCap: 5})
		total := 0
		for _, s := range States() {
			c, ok := m.States[s]
			if !ok {
				t.Fatalf("state %s missing (zero-fill)", s)
			}
			total += c
		}
		if total != len(beads) {
			t.Fatalf("round %d: states sum to %d, want %d", round, total, len(beads))
		}
		if len(m.States) != len(States()) {
			t.Fatalf("round %d: %d states, want %d", round, len(m.States), len(States()))
		}
		// Independent recount through Categorize.
		counts := map[State]int{}
		rs, bs := IDSet(ready), IDSet(blocked)
		for _, b := range beads {
			counts[Categorize(b, rs, bs, now)]++
		}
		for _, s := range States() {
			if counts[s] != m.States[s] {
				t.Fatalf("round %d state %s: %d vs %d", round, s, counts[s], m.States[s])
			}
		}
	}
}

func TestSummarizeZeroFillAndStored(t *testing.T) {
	beads := []bd.Bead{
		{ID: "a", Status: "open", IssueType: "task", Priority: 1, CreatedAt: ts(-time.Hour)},
		{ID: "b", Status: "review", IssueType: "task", Priority: 1, CreatedAt: ts(-time.Hour)},
		{ID: "c", Status: "newstatus", IssueType: "", Priority: 0, CreatedAt: ts(-time.Hour)},
	}
	m := Summarize(Input{
		Beads: beads, ClosedCount: 9, Now: now, LabelCap: 3,
		Statuses: []string{"open", "in_progress", "closed", "review", "archived"},
	})
	want := map[string]int{"open": 1, "in_progress": 0, "closed": 9, "review": 1, "archived": 0, "newstatus": 1}
	if !reflect.DeepEqual(m.Stored, want) {
		t.Fatalf("Stored = %v, want %v", m.Stored, want)
	}
	if m.ByType["unknown"] != 1 || m.ByType["task"] != 2 {
		t.Fatalf("ByType = %v", m.ByType)
	}
	if m.ByPriority["1"] != 2 || m.ByPriority["0"] != 1 {
		t.Fatalf("ByPriority = %v", m.ByPriority)
	}
}

func TestStoredClosedFromCountOverridesNothingElse(t *testing.T) {
	m := Summarize(Input{Beads: nil, ClosedCount: 4, Statuses: []string{"open", "closed"}, Now: now, LabelCap: 1})
	if m.Stored["closed"] != 4 || m.Stored["open"] != 0 {
		t.Fatalf("Stored = %v", m.Stored)
	}
}

func TestUnknownStatuses(t *testing.T) {
	beads := []bd.Bead{{Status: "open"}, {Status: "zeta"}, {Status: "alpha"}, {Status: "zeta"}}
	got := UnknownStatuses(beads, []string{"open"})
	if !reflect.DeepEqual(got, []string{"alpha", "zeta"}) {
		t.Fatalf("UnknownStatuses = %v", got)
	}
	if got := UnknownStatuses(beads, []string{"open", "alpha", "zeta"}); len(got) != 0 {
		t.Fatalf("all known: %v", got)
	}
}

func TestOldestPerState(t *testing.T) {
	old := ts(-100 * time.Hour)
	mid := ts(-50 * time.Hour)
	started := ts(-10 * time.Hour)
	beads := []bd.Bead{
		{ID: "ip1", Status: "in_progress", CreatedAt: old, StartedAt: started},
		{ID: "ip2", Status: "in_progress", CreatedAt: old}, // never started: falls back to created
		{ID: "r1", Status: "open", CreatedAt: mid},
		{ID: "r2", Status: "open", CreatedAt: old},
		{ID: "r3", Status: "open"}, // no timestamp: ignored for oldest
	}
	m := Summarize(Input{Beads: beads, Ready: beads[2:], Now: now, LabelCap: 1})
	if got := m.Oldest[InProgress]; !got.Equal(*old) {
		t.Fatalf("in_progress oldest = %v, want %v (fallback to created)", got, *old)
	}
	if got := m.Oldest[Ready]; !got.Equal(*old) {
		t.Fatalf("ready oldest = %v, want %v", got, *old)
	}
	if _, ok := m.Oldest[Blocked]; ok {
		t.Fatal("empty state must have no oldest")
	}
	// started_at is preferred over created_at for in_progress when set.
	m = Summarize(Input{Beads: beads[:1], Now: now, LabelCap: 1})
	if got := m.Oldest[InProgress]; !got.Equal(*started) {
		t.Fatalf("in_progress oldest = %v, want started_at %v", got, *started)
	}
}

func lc(label string, n int) LabelCount { return LabelCount{Label: label, Count: n} }

func beadsWithLabels(sets ...[]string) []bd.Bead {
	var out []bd.Bead
	for i, ls := range sets {
		out = append(out, bd.Bead{ID: fmt.Sprintf("alpha-%d", i), Labels: ls})
	}
	return out
}

func TestLabelCapTieBreakAtTheCap(t *testing.T) {
	// b, c, d tie at 2; cap 2 keeps a (3) then the alphabetically first tie (b).
	beads := beadsWithLabels(
		[]string{"a", "b"}, []string{"a", "c"}, []string{"a", "d"},
		[]string{"b"}, []string{"c"}, []string{"d"},
	)
	got := LabelCap(beads, 2)
	want := []LabelCount{lc("a", 3), lc("b", 2), lc("__other__", 4)}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("LabelCap = %v, want %v", got, want)
	}
}

func TestLabelCapRemainderCountsBeadsOnce(t *testing.T) {
	// The bead carrying both x and y (both outside the cap) counts once.
	beads := beadsWithLabels([]string{"a"}, []string{"a"}, []string{"a", "x", "y"}, []string{"x"})
	got := LabelCap(beads, 1)
	want := []LabelCount{lc("a", 3), lc("__other__", 2)}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("LabelCap = %v, want %v", got, want)
	}
}

func TestLabelCapNoRemainderWhenEverythingFits(t *testing.T) {
	beads := beadsWithLabels([]string{"a"}, []string{"a", "b"})
	got := LabelCap(beads, 5)
	want := []LabelCount{lc("a", 2), lc("b", 1)}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("LabelCap = %v, want %v", got, want)
	}
}

func TestLabelCapOfOne(t *testing.T) {
	beads := beadsWithLabels([]string{"a"}, []string{"b"}, []string{"b"}, []string{"c", "a"})
	got := LabelCap(beads, 1)
	want := []LabelCount{lc("a", 2), lc("__other__", 3)}
	// a and b tie at 2; a wins alphabetically. Beads carrying b or c: 3.
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("LabelCap = %v, want %v", got, want)
	}
}

func TestLabelCapRealLabelNamedOtherNeverCollides(t *testing.T) {
	beads := beadsWithLabels([]string{"__other__"}, []string{"__other__"}, []string{"__other__", "a"}, []string{"a"})
	got := LabelCap(beads, 5)
	// The real __other__ label is not a candidate; its three beads fall into
	// the remainder, and there is exactly one series with that name.
	want := []LabelCount{lc("a", 2), lc("__other__", 3)}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("LabelCap = %v, want %v", got, want)
	}
	n := 0
	for _, l := range got {
		if l.Label == "__other__" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d series named __other__, want exactly 1", n)
	}
}

func TestLabelCapDuplicateLabelOnABeadCountsOnce(t *testing.T) {
	got := LabelCap(beadsWithLabels([]string{"a", "a", "a"}), 3)
	if !reflect.DeepEqual(got, []LabelCount{lc("a", 1)}) {
		t.Fatalf("LabelCap = %v", got)
	}
}

func TestLabelCapVanishedLabelsDisappear(t *testing.T) {
	first := LabelCap(beadsWithLabels([]string{"gone"}, []string{"stay"}), 5)
	second := LabelCap(beadsWithLabels([]string{"stay"}), 5)
	hasGone := func(ls []LabelCount) bool {
		for _, l := range ls {
			if l.Label == "gone" {
				return true
			}
		}
		return false
	}
	if !hasGone(first) || hasGone(second) {
		t.Fatalf("first=%v second=%v", first, second)
	}
}

func TestLabelCapNoLabelsNoSeries(t *testing.T) {
	if got := LabelCap(beadsWithLabels(nil, nil), 3); len(got) != 0 {
		t.Fatalf("LabelCap = %v, want none", got)
	}
}

func TestLabelCapZeroKeepsNothing(t *testing.T) {
	got := LabelCap(beadsWithLabels([]string{"a"}, []string{"b"}), 0)
	if !reflect.DeepEqual(got, []LabelCount{lc("__other__", 2)}) {
		t.Fatalf("LabelCap = %v", got)
	}
	if got := LabelCap(beadsWithLabels([]string{"a"}), -3); !reflect.DeepEqual(got, []LabelCount{lc("__other__", 1)}) {
		t.Fatalf("negative cap: %v", got)
	}
}

func TestQueueResultsClientAndSpawnOnly(t *testing.T) {
	clientQ, _ := queue.New("drain", []string{"--exclude-label", "human", "--exclude-type", "epic"})
	spawnQ, _ := queue.New("prio", []string{"--priority", "1"})
	older := ts(-90 * time.Hour)
	newer := ts(-10 * time.Hour)
	tmpl := bd.Bead{ID: "t", IssueType: "task", IsTemplate: true, CreatedAt: ts(-500 * time.Hour)}
	ready := []bd.Bead{
		{ID: "r1", IssueType: "task", CreatedAt: newer},
		{ID: "r2", IssueType: "task", Labels: []string{"human"}, CreatedAt: ts(-300 * time.Hour)},
		{ID: "r3", IssueType: "epic", CreatedAt: ts(-300 * time.Hour)},
		{ID: "r4", IssueType: "bug", CreatedAt: older},
		tmpl,
	}
	m := Summarize(Input{
		Ready: ready, Now: now, LabelCap: 1,
		Queues: []queue.Queue{clientQ, spawnQ},
		QueueBeads: map[string][]bd.Bead{"prio": {
			{ID: "p1", CreatedAt: newer}, {ID: "p2", CreatedAt: older}, tmpl,
		}},
	})
	if len(m.Queues) != 2 {
		t.Fatalf("queues = %+v", m.Queues)
	}
	if q := m.Queues[0]; q.Name != "drain" || q.Candidates != 2 || q.Oldest == nil || !q.Oldest.Equal(*older) {
		t.Fatalf("client queue = %+v", q)
	}
	if q := m.Queues[1]; q.Name != "prio" || q.Candidates != 2 || q.Oldest == nil || !q.Oldest.Equal(*older) {
		t.Fatalf("spawn-only queue = %+v (templates must be dropped)", q)
	}
}

func TestEmptyQueueHasNoOldest(t *testing.T) {
	q, _ := queue.New("none", []string{"--label", "nothing"})
	m := Summarize(Input{Ready: []bd.Bead{{ID: "a"}}, Queues: []queue.Queue{q}, Now: now, LabelCap: 1})
	if m.Queues[0].Candidates != 0 || m.Queues[0].Oldest != nil {
		t.Fatalf("queue = %+v", m.Queues[0])
	}
	for _, s := range m.Samples("alpha") {
		if s.Family == metrics.FamQueueOldest {
			t.Fatalf("empty queue emitted an oldest series: %+v", s)
		}
	}
}

func TestSamplesFamiliesAndOmission(t *testing.T) {
	q, _ := queue.New("drain", nil)
	beads := []bd.Bead{{ID: "a", Status: "open", IssueType: "task", Priority: 2, CreatedAt: ts(-time.Hour), Labels: []string{"l"}}}
	m := Summarize(Input{Beads: beads, Ready: beads, Queues: []queue.Queue{q}, Statuses: []string{"open", "closed"}, Now: now, LabelCap: 2})
	got := map[string]int{}
	for _, s := range m.Samples("alpha") {
		got[s.Family]++
		if s.Labels[0].Name != "db" || s.Labels[0].Value != "alpha" {
			t.Fatalf("first label of %s = %+v", s.Family, s.Labels[0])
		}
	}
	want := map[string]int{
		metrics.FamIssues: 6, metrics.FamIssuesStored: 2, metrics.FamQueueCandidates: 1, metrics.FamQueueOldest: 1,
		metrics.FamByType: 1, metrics.FamByPriority: 1, metrics.FamByLabel: 1, metrics.FamOldest: 1,
	}
	if !reflect.DeepEqual(got, want) {
		var keys []string
		for k, v := range got {
			keys = append(keys, fmt.Sprintf("%s=%d", k, v))
		}
		sort.Strings(keys)
		t.Fatalf("family counts = %v, want %v", keys, want)
	}
}

func TestThroughputSamples(t *testing.T) {
	ss := Throughput{Created: 3, Closed: 5}.Samples("beta")
	if len(ss) != 2 || ss[0].Family != metrics.FamCreated24h || ss[0].Value != 3 || ss[1].Family != metrics.FamClosed24h || ss[1].Value != 5 {
		t.Fatalf("samples = %+v", ss)
	}
}

func TestSecondsKeepsSubSecondPrecision(t *testing.T) {
	got := Seconds(time.Unix(1700000000, 500_000_000))
	if got != 1700000000.5 {
		t.Fatalf("Seconds = %v", got)
	}
}

// The cases below pin comparisons whose mutants only differ for strings that
// sort before (or after) the constant they are compared with.

func TestCategorizeStringComparisonsAreExact(t *testing.T) {
	cases := []struct {
		name string
		bead bd.Bead
		want State
	}{
		// "archived" sorts before "blocked": only a loose comparison would call it blocked.
		{"status sorting before blocked", bd.Bead{ID: "a", Status: "archived", IssueType: "task"}, Other},
		{"status sorting after blocked", bd.Bead{ID: "a", Status: "review", IssueType: "task"}, Other},
		// "bug" and "epic" sort before "merge-request"; "task" after.
		{"type sorting before merge-request", bd.Bead{ID: "a", Status: "open", IssueType: "bug"}, Other},
		{"type epic not ready is other", bd.Bead{ID: "a", Status: "open", IssueType: "epic"}, Other},
		{"type sorting after merge-request", bd.Bead{ID: "a", Status: "open", IssueType: "task"}, Other},
		{"type sorting between the pinned types", bd.Bead{ID: "a", Status: "open", IssueType: "milestone"}, Other},
	}
	for _, tc := range cases {
		if got := Categorize(tc.bead, nil, nil, now); got != tc.want {
			t.Errorf("%s: state = %s, want %s", tc.name, got, tc.want)
		}
	}
}

func TestStartedAtOnlyOrdersInProgress(t *testing.T) {
	created := ts(-100 * time.Hour)
	started := ts(-5 * time.Hour)
	mk := func(id, status string) bd.Bead {
		return bd.Bead{ID: id, Status: status, IssueType: "task", CreatedAt: created, StartedAt: started}
	}
	// One bead per state, all carrying a start time that differs from creation.
	beads := []bd.Bead{mk("ip", "in_progress"), mk("df", "deferred"), mk("bl", "blocked"), mk("rd", "open"), mk("ot", "pinned")}
	mr := mk("tr", "open")
	mr.IssueType = "merge-request"
	beads = append(beads, mr)
	m := Summarize(Input{Beads: beads, Ready: []bd.Bead{beads[3]}, Now: now, LabelCap: 1})
	for _, s := range States() {
		want := *created
		if s == InProgress {
			want = *started
		}
		if got, ok := m.Oldest[s]; !ok || !got.Equal(want) {
			t.Errorf("oldest[%s] = %v (present %v), want %v", s, got, ok, want)
		}
	}
}

func TestQueueOldestIgnoresCandidatesWithoutCreationTime(t *testing.T) {
	q, _ := queue.New("q", nil)
	only := bd.Bead{ID: "nil-created"}
	older := bd.Bead{ID: "o", CreatedAt: ts(-9 * time.Hour)}
	newer := bd.Bead{ID: "n", CreatedAt: ts(-1 * time.Hour)}

	m := Summarize(Input{Ready: []bd.Bead{only}, Queues: []queue.Queue{q}, Now: now, LabelCap: 1})
	if m.Queues[0].Candidates != 1 || m.Queues[0].Oldest != nil {
		t.Fatalf("queue = %+v: a candidate without created_at counts but has no oldest", m.Queues[0])
	}
	// The undated candidate may come first or last; the oldest is still the older dated one.
	for _, ready := range [][]bd.Bead{{only, newer, older}, {newer, older, only}, {older, newer}} {
		m = Summarize(Input{Ready: ready, Queues: []queue.Queue{q}, Now: now, LabelCap: 1})
		if o := m.Queues[0].Oldest; o == nil || !o.Equal(*older.CreatedAt) {
			t.Fatalf("oldest = %v for %v, want %v", o, ready, older.CreatedAt)
		}
	}
	m = Summarize(Input{Ready: []bd.Bead{newer, older}, Queues: []queue.Queue{q}, Now: now, LabelCap: 1})
	if o := m.Queues[0].Oldest; o == nil || !o.Equal(*older.CreatedAt) {
		t.Fatalf("oldest = %v", o)
	}
}

func TestLabelCapOrderingIsExact(t *testing.T) {
	// Counts: y 5, z 5, a 3, b 3, c 1, d 1, plus labels sorting before and after "__other__".
	counts := map[string]int{"y": 5, "z": 5, "a": 3, "b": 3, "c": 1, "d": 1, "Upper": 2, "1digit": 2}
	var sets [][]string
	for label, n := range counts {
		for i := 0; i < n; i++ {
			sets = append(sets, []string{label})
		}
	}
	want := []LabelCount{lc("y", 5), lc("z", 5), lc("a", 3), lc("b", 3), lc("1digit", 2), lc("Upper", 2), lc("c", 1), lc("d", 1)}
	// Map iteration order is random, so repeat to exercise many input orders.
	for i := 0; i < 60; i++ {
		got := LabelCap(beadsWithLabels(sets...), 20)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("run %d: LabelCap = %v, want %v", i, got, want)
		}
	}
	// Cap in the middle of a tie group keeps the alphabetically first members.
	got := LabelCap(beadsWithLabels(sets...), 5)
	wantCut := append(append([]LabelCount{}, want[:5]...), lc("__other__", 2+1+1))
	if !reflect.DeepEqual(got, wantCut) {
		t.Fatalf("capped LabelCap = %v, want %v", got, wantCut)
	}
}
