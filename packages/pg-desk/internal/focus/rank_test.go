package focus

import (
	"fmt"
	"math/rand"
	"reflect"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/interpret"
)

// rankDay is the addressed day of most rank tests.
var rankDay = time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)

// rankFixture is a fixture whose focus time zone is pinned to UTC, so a due
// date never depends on the machine's zone.
func rankFixture(t *testing.T) *fixture {
	t.Helper()
	f := newFixture(t)
	f.cfg.Focus.TimeZone = "UTC"
	return f
}

func rankOpts() RankOptions {
	return RankOptions{Date: rankDay, Clock: interpret.FixedClock(fixtureNow)}
}

// rank ranks the fixture's candidate set on rankDay.
func (f *fixture) rank(opts RankOptions) Ranking {
	f.t.Helper()
	in := f.inputs()
	return Rank(in, Candidates(in), opts)
}

func rowKeys(r Ranking) []Key {
	out := make([]Key, len(r.Rows))
	for i, row := range r.Rows {
		out[i] = row.Key
	}
	return out
}

func (r Ranking) row(t *testing.T, k Key) RankedRow {
	t.Helper()
	for _, row := range r.Rows {
		if row.Key == k {
			return row
		}
	}
	t.Fatalf("%v is not a row of the ranking %v", k, rowKeys(r))
	return RankedRow{}
}

func (r Ranking) pos(t *testing.T, k Key) int { return r.row(t, k).Position }

func wantOrder(t *testing.T, r Ranking, want ...Key) {
	t.Helper()
	if got := rowKeys(r); !reflect.DeepEqual(got, want) {
		t.Errorf("order = %v, want %v", got, want)
	}
}

// bead stores a labelled bead and returns its key.
func (f *fixture) bead(id string, s issueSpec) Key {
	f.t.Helper()
	if s.state == "" {
		s.state = "open"
	}
	s.labels = append([]string{PlanableLabel}, s.labels...)
	s.hydrated = true
	return f.issue(id, s)
}

// blockedBy stores n open issues that each name blocker as a blocking edge,
// so blocker has n open direct dependents in the reverse-edge index.
func (f *fixture) dependents(blocker Key, n int, s issueSpec) {
	f.t.Helper()
	for i := 0; i < n; i++ {
		d := s
		if d.state == "" {
			d.state = "open"
		}
		d.deps = []map[string]string{{"id": blocker.ID, "type": "blocks"}}
		f.issue(fmt.Sprintf("dep-%s-%d", blocker.ID, i), d)
	}
}

func TestFocusRankBoundaries(t *testing.T) {
	// All six beads are unstarted with the same priority and age, so only the
	// due key separates them (and the id tiebreak among the "none" class).
	build := func(zone string) (*fixture, map[string]Key) {
		f := rankFixture(t)
		if zone != "" {
			f.cfg.Focus.TimeZone = zone
		}
		k := map[string]Key{
			"dayBefore": f.bead("bd-01", issueSpec{dueDate: "2026-10-09"}),
			"equal":     f.bead("bd-02", issueSpec{dueDate: "2026-10-10"}),
			"plus7":     f.bead("bd-03", issueSpec{dueDate: "2026-10-17"}),
			"plus8":     f.bead("bd-04", issueSpec{dueDate: "2026-10-18"}),
			// 23:30 at -05:00 is 04:30 UTC the next day: Oct 11 in UTC, Oct 10 in New York.
			"offset":  f.bead("bd-05", issueSpec{dueDate: "2026-10-10T23:30:00-05:00"}),
			"empty":   f.bead("bd-06", issueSpec{}),
			"garbage": f.bead("bd-07", issueSpec{dueDate: "next tuesday"}),
		}
		return f, k
	}

	t.Run("UTC", func(t *testing.T) {
		f, k := build("")
		r := f.rank(rankOpts())
		// overdue (before), then due 0 days, +1 (the offset timestamp), +7, then
		// the "none" class: +8, empty and garbage, in key order.
		wantOrder(t, r, k["dayBefore"], k["equal"], k["offset"], k["plus7"], k["plus8"], k["empty"], k["garbage"])
		if got := r.row(t, k["dayBefore"]).Tier; got != TierOverdue {
			t.Errorf("a day before is tier %q, want overdue", got)
		}
		for _, name := range []string{"equal", "plus7", "plus8", "offset", "empty", "garbage"} {
			if got := r.row(t, k[name]).Tier; got != TierNotStarted {
				t.Errorf("%s tier = %q, want not_started", name, got)
			}
		}
		if r.Inputs.UnparseableDue != 1 {
			t.Errorf("UnparseableDue = %d, want 1 (only the garbage value; empty is no date, not a bad one)", r.Inputs.UnparseableDue)
		}
		if got := r.row(t, k["offset"]).Due; got != "2026-10-11" {
			t.Errorf("offset timestamp due = %q, want 2026-10-11 in UTC", got)
		}
		if got := r.row(t, k["empty"]).Due; got != "" {
			t.Errorf("empty due shows %q", got)
		}
	})

	t.Run("New York converts the offset timestamp to its own day", func(t *testing.T) {
		f, k := build("America/New_York")
		// 23:30 at -05:00 on Oct 10 is 04:30 UTC on Oct 11, which is 00:30 EDT
		// (UTC-4) on Oct 11: converted to the zone, then truncated to the day.
		r := f.rank(rankOpts())
		if got := r.row(t, k["offset"]).Due; got != "2026-10-11" {
			t.Errorf("offset timestamp due = %q, want 2026-10-11", got)
		}
	})

	t.Run("a zone west of the offset moves the day back", func(t *testing.T) {
		f := rankFixture(t)
		f.cfg.Focus.TimeZone = "America/Los_Angeles"
		k := f.bead("bd-01", issueSpec{dueDate: "2026-10-10T23:30:00-05:00"})
		// 04:30 UTC on Oct 11 is 21:30 PDT (UTC-7) on Oct 10.
		if got := f.rank(rankOpts()).row(t, k).Due; got != "2026-10-10" {
			t.Errorf("due = %q, want 2026-10-10 in Los Angeles", got)
		}
	})

	t.Run("a timestamp with no offset is read in the focus zone", func(t *testing.T) {
		f := rankFixture(t)
		k := f.bead("bd-01", issueSpec{dueDate: "2026-10-12T23:59:59"})
		if got := f.rank(rankOpts()).row(t, k).Due; got != "2026-10-12" {
			t.Errorf("due = %q, want 2026-10-12", got)
		}
	})
}

func TestFocusDefaultDateIsInjectedClockLocalDay(t *testing.T) {
	f := rankFixture(t)
	f.cfg.Focus.TimeZone = "America/New_York"
	yesterday := f.bead("bd-a", issueSpec{dueDate: "2026-10-09"})
	earlier := f.bead("bd-b", issueSpec{dueDate: "2026-10-08"})
	in := f.inputs()
	set := Candidates(in)

	// 02:00 UTC on Oct 10 is 22:00 on Oct 9 in New York: today is Oct 9, so
	// a due date of Oct 9 is due today, not overdue.
	r := Rank(in, set, RankOptions{Clock: interpret.FixedClock(time.Date(2026, 10, 10, 2, 0, 0, 0, time.UTC))})
	if got := r.row(t, yesterday).Tier; got != TierNotStarted {
		t.Errorf("due Oct 9 on the local day Oct 9: tier = %q, want not_started", got)
	}
	if got := r.row(t, earlier).Tier; got != TierOverdue {
		t.Errorf("due Oct 8 on the local day Oct 9: tier = %q, want overdue", got)
	}

	// 05:00 UTC on Oct 10 is 01:00 on Oct 10 in New York: Oct 9 is overdue.
	r = Rank(in, set, RankOptions{Clock: interpret.FixedClock(time.Date(2026, 10, 10, 5, 0, 0, 0, time.UTC))})
	if got := r.row(t, yesterday).Tier; got != TierOverdue {
		t.Errorf("due Oct 9 on the local day Oct 10: tier = %q, want overdue", got)
	}

	// No clock option: Inputs.Now (12:00 UTC Oct 10 = 08:00 New York).
	r = Rank(in, set, RankOptions{})
	if got := r.row(t, yesterday).Tier; got != TierOverdue {
		t.Errorf("default clock = Inputs.Now: tier = %q, want overdue", got)
	}
	// An explicit Date wins over the clock.
	r = Rank(in, set, RankOptions{Date: time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC), Clock: interpret.FixedClock(fixtureNow)})
	if got := r.row(t, yesterday).Tier; got != TierNotStarted {
		t.Errorf("explicit --date Oct 9: tier = %q, want not_started", got)
	}
}

func TestFocusRankStableAcrossRepeatedCalls(t *testing.T) {
	f := rankFixture(t)
	pr1 := f.pr("o/r#1", prSpec{ownership: "mine"})
	f.pr("o/r#2", prSpec{ownership: "co-owned"})
	b1 := f.bead("bd-1", issueSpec{dueDate: "2026-10-12", priority: "P1"})
	f.bead("bd-2", issueSpec{dueDate: "2026-10-01", priority: "P3", state: "in_progress"})
	f.bead("bd-3", issueSpec{priority: "P0", createdAt: "2026-01-01T00:00:00Z"})
	f.bead("bd-4", issueSpec{priority: "P0", createdAt: "2026-02-01T00:00:00Z"})
	f.issue("PROJ-1", issueSpec{state: "To Do", category: "new", assignee: "Pat Example", priority: "High"})
	f.issue("PROJ-2", issueSpec{state: "In Progress", category: "indeterminate", assignee: "Pat Example", priority: "Low", dueDate: "2026-10-14"})
	f.link(b1, pr1, relationWork)
	in := f.inputs()
	set := Candidates(in)
	want := Rank(in, set, rankOpts())
	if len(want.Rows) < 6 {
		t.Fatalf("only %d rows: the fixture does not exercise the rank", len(want.Rows))
	}
	rng := rand.New(rand.NewSource(7))
	for i := 0; i < 25; i++ {
		shuffled := set
		shuffled.Candidates = append([]Candidate(nil), set.Candidates...)
		rng.Shuffle(len(shuffled.Candidates), func(a, b int) {
			shuffled.Candidates[a], shuffled.Candidates[b] = shuffled.Candidates[b], shuffled.Candidates[a]
		})
		if got := Rank(in, shuffled, rankOpts()); !reflect.DeepEqual(got, want) {
			t.Fatalf("shuffle %d changed the ranking:\n got  %v\n want %v", i, rowKeys(got), rowKeys(want))
		}
	}
}

// onePair builds two overdue (or open) candidates that differ in exactly one
// key and reports the order and deciding key. The winner is given the id that
// LOSES the tiebreak, so only the key under test can put it first.
type rankSpec struct {
	issueSpec
	blocks int
}

func pairOrder(t *testing.T, winner, loser rankSpec) (first, second RankedRow, r Ranking) {
	t.Helper()
	f := rankFixture(t)
	w := f.bead("bd-z", winner.issueSpec)
	l := f.bead("bd-a", loser.issueSpec)
	f.dependents(w, winner.blocks, issueSpec{})
	f.dependents(l, loser.blocks, issueSpec{})
	r = f.rank(rankOpts())
	if len(r.Rows) != 2 {
		t.Fatalf("rows = %v, want the two candidates", rowKeys(r))
	}
	if r.Rows[0].Key != w {
		t.Fatalf("order = %v, want the winner %v first", rowKeys(r), w)
	}
	return r.Rows[0], r.Rows[1], r
}

func TestOverdueTierInnerOrder(t *testing.T) {
	overdue := func(day string, s rankSpec) rankSpec { s.dueDate = day; return s }
	cases := []struct {
		name          string
		winner, loser rankSpec
		key           string
	}{
		{"started first", overdue("2026-10-09", rankSpec{issueSpec: issueSpec{state: "in_progress"}}), overdue("2026-10-01", rankSpec{}), KeyStarted},
		{"most overdue", overdue("2026-10-05", rankSpec{}), overdue("2026-10-08", rankSpec{}), KeyOverdue},
		{"unblocks", overdue("2026-10-08", rankSpec{blocks: 2}), overdue("2026-10-08", rankSpec{blocks: 1}), KeyUnblocks},
		{"priority", overdue("2026-10-08", rankSpec{issueSpec: issueSpec{priority: "P1"}}), overdue("2026-10-08", rankSpec{issueSpec: issueSpec{priority: "P3"}}), KeyPriority},
		{"age oldest first", overdue("2026-10-08", rankSpec{issueSpec: issueSpec{createdAt: "2026-01-01T00:00:00Z"}}), overdue("2026-10-08", rankSpec{issueSpec: issueSpec{createdAt: "2026-02-01T00:00:00Z"}}), KeyAge},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			first, second, _ := pairOrder(t, c.winner, c.loser)
			if first.Tier != TierOverdue || second.Tier != TierOverdue {
				t.Fatalf("tiers = %q, %q, want both overdue", first.Tier, second.Tier)
			}
			if first.DecidingKey != c.key {
				t.Errorf("deciding key = %q, want %q", first.DecidingKey, c.key)
			}
			if second.DecidingKey != "" {
				t.Errorf("last row deciding key = %q, want empty", second.DecidingKey)
			}
		})
	}

	t.Run("tiebreak is kind then key", func(t *testing.T) {
		f := rankFixture(t)
		b := f.bead("bd-b", issueSpec{dueDate: "2026-10-08"})
		a := f.bead("bd-a", issueSpec{dueDate: "2026-10-08"})
		j := f.issue("PROJ-1", issueSpec{state: "To Do", category: "new", assignee: "Pat Example", dueDate: "2026-10-08"})
		p := f.pr("o/r#1", prSpec{ownership: "mine"})
		in := f.inputs()
		// The PR is started, so it leads the overdue tier only if it is overdue;
		// it has no due date and is therefore not in this tier at all.
		r := Rank(in, Candidates(in), rankOpts())
		if got := r.row(t, p).Tier; got != TierStarted {
			t.Errorf("PR tier = %q, want started", got)
		}
		// Overdue rows: jira, then beads by key (age is the shared first-seen
		// time, unblocks 0, no priority).
		var overdueOrder []Key
		for _, row := range r.Rows {
			if row.Tier == TierOverdue {
				overdueOrder = append(overdueOrder, row.Key)
			}
		}
		if want := []Key{j, a, b}; !reflect.DeepEqual(overdueOrder, want) {
			t.Errorf("overdue order = %v, want %v (kind jira before bead, then key)", overdueOrder, want)
		}
		for _, row := range r.Rows {
			if row.Tier == TierOverdue && row.Key == j {
				if row.DecidingKey != KeyTiebreak {
					t.Errorf("deciding key = %q, want tiebreak", row.DecidingKey)
				}
			}
		}
	})
}

func TestTiersAreStrictAndOrdered(t *testing.T) {
	// Evidence pairs of the operator's head-to-head rulings that cross a tier
	// boundary: no later key can overturn an earlier tier.
	f := rankFixture(t)
	over := f.bead("bd-over", issueSpec{dueDate: "2026-10-01", priority: "P4"})
	started := f.bead("bd-started", issueSpec{state: "in_progress", priority: "P4"})
	notStarted := f.bead("bd-p0", issueSpec{priority: "P0", dueDate: "2026-10-11"})
	pr := f.pr("o/r#1", prSpec{ownership: "mine"})
	f.dependents(notStarted, 5, issueSpec{})
	r := f.rank(rankOpts())
	// overdue beats started beats not-started whatever priority, due and unblocks say.
	wantOrder(t, r, over, pr, started, notStarted)
	if r.row(t, over).DecidingKey != KeyTier || r.row(t, started).DecidingKey != KeyTier {
		t.Errorf("deciding keys = %q, %q, want tier on both boundaries", r.row(t, over).DecidingKey, r.row(t, started).DecidingKey)
	}
	if r.row(t, pr).DecidingKey != KeyPriority {
		t.Errorf("PR over started bead: deciding key = %q, want priority (P2 over P4)", r.row(t, pr).DecidingKey)
	}
}

func TestHorizonEquivalenceClass(t *testing.T) {
	t.Run("plus 8 equals none, so unblocks decides", func(t *testing.T) {
		// The winner is due in 8 days (outside the horizon) and blocks one
		// item; the loser has no due date and blocks none.
		first, _, _ := pairOrder(t,
			rankSpec{issueSpec: issueSpec{dueDate: "2026-10-18"}, blocks: 1},
			rankSpec{})
		if first.DecidingKey != KeyUnblocks {
			t.Errorf("deciding key = %q, want unblocks (+8 days is the same class as none)", first.DecidingKey)
		}
		// And the other way: no date but unblocking beats +8 days and nothing.
		first, _, _ = pairOrder(t,
			rankSpec{blocks: 1},
			rankSpec{issueSpec: issueSpec{dueDate: "2026-10-18"}})
		if first.DecidingKey != KeyUnblocks {
			t.Errorf("deciding key = %q, want unblocks", first.DecidingKey)
		}
	})
	t.Run("plus 7 is inside, ahead of none whatever unblocks says", func(t *testing.T) {
		first, _, _ := pairOrder(t,
			rankSpec{issueSpec: issueSpec{dueDate: "2026-10-17"}},
			rankSpec{blocks: 5})
		if first.DecidingKey != KeyDue {
			t.Errorf("deciding key = %q, want due", first.DecidingKey)
		}
	})
	t.Run("two dates outside the horizon are equal", func(t *testing.T) {
		first, _, _ := pairOrder(t,
			rankSpec{issueSpec: issueSpec{dueDate: "2027-03-01"}, blocks: 1},
			rankSpec{issueSpec: issueSpec{dueDate: "2026-10-18"}})
		if first.DecidingKey != KeyUnblocks {
			t.Errorf("deciding key = %q, want unblocks", first.DecidingKey)
		}
	})
	t.Run("nearer date first inside the horizon", func(t *testing.T) {
		first, _, _ := pairOrder(t,
			rankSpec{issueSpec: issueSpec{dueDate: "2026-10-11"}},
			rankSpec{issueSpec: issueSpec{dueDate: "2026-10-13"}, blocks: 3})
		if first.DecidingKey != KeyDue {
			t.Errorf("deciding key = %q, want due", first.DecidingKey)
		}
	})
}

func TestOperatorHeadToHeadsHoldInEveryKeyOrder(t *testing.T) {
	// Each ruling of the spec's evidence table that this packet's keys decide,
	// built as a pair whose later keys point the OTHER way (a reversed-key
	// negative control for each).
	started := issueSpec{state: "in_progress"}
	cases := []struct {
		name          string
		winner, loser rankSpec
		key           string
	}{
		{"started P2 over unstarted P1", rankSpec{issueSpec: issueSpec{state: "in_progress", priority: "P2"}}, rankSpec{issueSpec: issueSpec{priority: "P1"}}, KeyTier},
		{"started blocking nothing over unstarted unblocker", rankSpec{issueSpec: started}, rankSpec{blocks: 3}, KeyTier},
		{"started P1 no deadline over unstarted P3 due tomorrow", rankSpec{issueSpec: issueSpec{state: "in_progress", priority: "P1"}}, rankSpec{issueSpec: issueSpec{priority: "P3", dueDate: "2026-10-11"}}, KeyTier},
		{"unstarted P3 due in 3 days over unstarted P1 no deadline", rankSpec{issueSpec: issueSpec{priority: "P3", dueDate: "2026-10-13"}}, rankSpec{issueSpec: issueSpec{priority: "P1"}}, KeyDue},
		{"started P3 due tomorrow over started P1 no deadline", rankSpec{issueSpec: issueSpec{state: "in_progress", priority: "P3", dueDate: "2026-10-11"}}, rankSpec{issueSpec: issueSpec{state: "in_progress", priority: "P1"}}, KeyDue},
		{"unstarted P2 unblocking 3 over unstarted P1 blocking none", rankSpec{issueSpec: issueSpec{priority: "P2"}, blocks: 3}, rankSpec{issueSpec: issueSpec{priority: "P1"}}, KeyUnblocks},
		{"unstarted due in 3 days over unstarted no deadline unblocking 3", rankSpec{issueSpec: issueSpec{dueDate: "2026-10-13"}}, rankSpec{blocks: 3}, KeyDue},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			first, _, _ := pairOrder(t, c.winner, c.loser)
			if first.DecidingKey != c.key {
				t.Errorf("deciding key = %q, want %q", first.DecidingKey, c.key)
			}
		})
	}
	t.Run("overdue unstarted P1 over started P3 with no deadline", func(t *testing.T) {
		first, _, _ := pairOrder(t,
			rankSpec{issueSpec: issueSpec{priority: "P1", dueDate: "2026-10-09"}},
			rankSpec{issueSpec: issueSpec{state: "in_progress", priority: "P3"}})
		if first.DecidingKey != KeyTier || first.Tier != TierOverdue {
			t.Errorf("first = tier %q key %q, want overdue by tier", first.Tier, first.DecidingKey)
		}
	})
}

func TestPriorityInheritance(t *testing.T) {
	f := rankFixture(t)
	// A PR with no priority of its own inherits the highest of its group.
	prInherit := f.pr("o/r#1", prSpec{ownership: "mine"})
	beadP1 := f.bead("bd-p1", issueSpec{priority: "P1"})
	beadP3 := f.bead("bd-p3", issueSpec{priority: "P3"})
	f.link(beadP1, prInherit, relationWork)
	f.link(beadP3, prInherit, relationWork)
	// A PR in no group is P2.
	prAlone := f.pr("o/r#2", prSpec{ownership: "mine"})
	// An unmapped tracker value sorts after P4, and is counted.
	unmapped := f.issue("PROJ-1", issueSpec{state: "To Do", category: "new", assignee: "Pat Example", priority: "Critical-ish"})
	// A mapped tracker value ranks through the table (default Atlassian names).
	high := f.issue("PROJ-2", issueSpec{state: "To Do", category: "new", assignee: "Pat Example", priority: "High"})
	lowest := f.issue("PROJ-3", issueSpec{state: "To Do", category: "new", assignee: "Pat Example", priority: "lowest"})
	p4 := f.bead("bd-p4", issueSpec{priority: "P4"})
	r := f.rank(rankOpts())

	if got := r.row(t, prInherit).Priority; got != "P1" {
		t.Errorf("PR priority = %q, want P1 (highest of its group)", got)
	}
	if got := r.row(t, prAlone).Priority; got != "P2" {
		t.Errorf("lone PR priority = %q, want P2", got)
	}
	// The group is one row: the PR (started) represents it.
	if cover := r.Covered[beadP1]; cover != prInherit {
		t.Errorf("Covered[%v] = %v, want %v", beadP1, cover, prInherit)
	}
	if got := r.row(t, high).Priority; got != "P1" {
		t.Errorf("High maps to %q, want P1", got)
	}
	if got := r.row(t, lowest).Priority; got != "P4" {
		t.Errorf("lowest maps to %q, want P4 (case-insensitive)", got)
	}
	if got := r.row(t, unmapped).Priority; got != "Critical-ish" {
		t.Errorf("unmapped priority shows %q, want the raw value", got)
	}
	if r.Inputs.UnmappedPriority != 1 {
		t.Errorf("UnmappedPriority = %d, want 1", r.Inputs.UnmappedPriority)
	}
	// Not-started tier, no dates, all unblocks 0: priority orders them.
	if !(r.pos(t, high) < r.pos(t, p4)) {
		t.Errorf("High (P1) must rank before a P4 bead: %v", rowKeys(r))
	}
	if !(r.pos(t, p4) < r.pos(t, unmapped)) || !(r.pos(t, lowest) < r.pos(t, unmapped)) {
		t.Errorf("an unmapped value must sort after P4: %v", rowKeys(r))
	}

	t.Run("a configured map replaces the default", func(t *testing.T) {
		f := rankFixture(t)
		f.cfg.Focus.PriorityMap = map[string]string{"Blocker": "P0", "Minor": "p3"}
		blocker := f.issue("PROJ-1", issueSpec{state: "To Do", category: "new", assignee: "Pat Example", priority: "blocker"})
		high := f.issue("PROJ-2", issueSpec{state: "To Do", category: "new", assignee: "Pat Example", priority: "High"})
		direct := f.issue("PROJ-3", issueSpec{state: "To Do", category: "new", assignee: "Pat Example", priority: "P2"})
		r := f.rank(rankOpts())
		if got := r.row(t, blocker).Priority; got != "P0" {
			t.Errorf("Blocker = %q, want P0", got)
		}
		if got := r.row(t, high).Priority; got != "High" {
			t.Errorf("High under a replaced map = %q, want the raw (unmapped) value", got)
		}
		if got := r.row(t, direct).Priority; got != "P2" {
			t.Errorf("direct P2 = %q, want P2 even when the map does not name it", got)
		}
		if r.Inputs.UnmappedPriority != 1 {
			t.Errorf("UnmappedPriority = %d, want 1", r.Inputs.UnmappedPriority)
		}
	})

	t.Run("an issue with no priority sorts after P4 without being counted", func(t *testing.T) {
		f := rankFixture(t)
		none := f.issue("PROJ-1", issueSpec{state: "To Do", category: "new", assignee: "Pat Example"})
		p4 := f.bead("bd-p4", issueSpec{priority: "P4"})
		r := f.rank(rankOpts())
		wantOrder(t, r, p4, none)
		if r.Inputs.UnmappedPriority != 0 {
			t.Errorf("UnmappedPriority = %d, want 0 (absent is not unmapped)", r.Inputs.UnmappedPriority)
		}
		if got := r.row(t, none).Priority; got != "" {
			t.Errorf("priority shown = %q, want empty", got)
		}
	})
}

func TestAgeTrackerCreationElseFirstSeen(t *testing.T) {
	f := rankFixture(t)
	// d has a tracker creation time in May and a first-seen time long before:
	// the tracker value must win, so d does NOT lead.
	a := f.bead("bd-a", issueSpec{createdAt: "2026-01-01T00:00:00Z"})
	b := f.bead("bd-b", issueSpec{})
	c := f.bead("bd-c", issueSpec{})
	d := f.bead("bd-d", issueSpec{createdAt: "2026-05-01T00:00:00Z"})
	e := f.bead("bd-e", issueSpec{}) // no creation time, no first-seen time
	in := f.inputs()
	for k, ts := range map[Key]string{
		a: "2026-10-09T00:00:00Z", b: "2026-02-01T00:00:00Z", c: "2026-03-01T00:00:00Z", d: "2025-01-01T00:00:00Z", e: "",
	} {
		ent := in.Entities[k]
		ent.FirstSeenAt = ts
		in.Entities[k] = ent
	}
	r := Rank(in, Candidates(in), rankOpts())
	// Oldest first by the effective age: a (Jan, tracker), b (Feb, first-seen),
	// c (Mar, first-seen), d (May, tracker), then e (no age at all).
	wantOrder(t, r, a, b, c, d, e)
	if got := r.row(t, a).DecidingKey; got != KeyAge {
		t.Errorf("deciding key = %q, want age", got)
	}
	if r.Inputs.AgeFallback != 3 {
		t.Errorf("AgeFallback = %d, want 3 (b, c and e ranked without a creation time)", r.Inputs.AgeFallback)
	}
}

func TestPRRanksOnFirstSeenAndCountsAsFallback(t *testing.T) {
	f := rankFixture(t)
	old := f.pr("o/r#2", prSpec{ownership: "mine"})
	young := f.pr("o/r#1", prSpec{ownership: "mine"})
	in := f.inputs()
	for k, ts := range map[Key]string{old: "2026-01-01T00:00:00Z", young: "2026-09-01T00:00:00Z"} {
		ent := in.Entities[k]
		ent.FirstSeenAt = ts
		in.Entities[k] = ent
	}
	r := Rank(in, Candidates(in), rankOpts())
	wantOrder(t, r, old, young)
	if r.Inputs.AgeFallback != 2 {
		t.Errorf("AgeFallback = %d, want 2 (every PR ranks on first-seen)", r.Inputs.AgeFallback)
	}
}

func TestPRUnblocksFromDependencyResolver(t *testing.T) {
	f := rankFixture(t)
	// Ids are chosen so the id tiebreak would give the REVERSE of the
	// unblocks order.
	none := f.pr("o/r#1", prSpec{ownership: "mine", branch: "none"})
	one := f.pr("o/r#2", prSpec{ownership: "mine", branch: "one"})
	two := f.pr("o/r#3", prSpec{ownership: "mine", branch: "two"})
	// Two stacked PRs on top of "two" (stack source), one external depends_on
	// on "one", and a merged PR stacked on "none", which no longer waits.
	f.pr("o/r#4", prSpec{ownership: "mine", branch: "s1", base: "two"})
	f.pr("o/r#5", prSpec{ownership: "mine", branch: "s2", base: "two"})
	ext := f.pr("o/r#6", prSpec{ownership: "mine", branch: "e1"})
	f.external(ext, one, "depends_on", "waits on one")
	f.pr("o/r#7", prSpec{state: "merged", merged: true, branch: "m1", base: "none"})
	r := f.rank(rankOpts())
	if !(r.pos(t, two) < r.pos(t, one) && r.pos(t, one) < r.pos(t, none)) {
		t.Errorf("order = %v, want #3 (2 dependents), #2 (1), #1 (0, the merged dependent does not count)", rowKeys(r))
	}
	if got := r.row(t, two).DecidingKey; got != KeyUnblocks {
		t.Errorf("deciding key under #3 = %q, want unblocks", got)
	}
}

func TestIssueUnblocksFromReverseEdgeIndex(t *testing.T) {
	f := rankFixture(t)
	none := f.bead("bd-1", issueSpec{})
	one := f.bead("bd-2", issueSpec{})
	two := f.bead("bd-3", issueSpec{})
	f.dependents(one, 1, issueSpec{})
	f.dependents(two, 2, issueSpec{})
	// A closed dependent and an inactive one do not count.
	f.issue("dep-closed", issueSpec{state: "closed", deps: []map[string]string{{"id": "bd-1", "type": "blocks"}}})
	f.issue("dep-inactive", issueSpec{state: "open", inactive: true, deps: []map[string]string{{"id": "bd-1", "type": "blocks"}}})
	// A parent-child edge is not a blocking edge.
	f.issue("dep-child", issueSpec{state: "open", deps: []map[string]string{{"id": "bd-1", "type": "parent-child"}}})
	r := f.rank(rankOpts())
	wantOrder(t, r, two, one, none)
	if r.Inputs.UnblocksUnavailable != 0 {
		t.Errorf("UnblocksUnavailable = %d, want 0 (all three carry issue_deps)", r.Inputs.UnblocksUnavailable)
	}
}

func TestUnblocksZeroForIssuesUntilHydrationEnabled(t *testing.T) {
	build := func(hydrated bool) (*fixture, Key, Key) {
		f := rankFixture(t)
		// bd-a would win the tiebreak; bd-z has three dependents but, without
		// hydration, its stored facts carry no issue_deps.
		a := f.bead("bd-a", issueSpec{})
		z := f.issue("bd-z", issueSpec{state: "open", labels: []string{PlanableLabel}, hydrated: hydrated})
		f.dependents(z, 3, issueSpec{})
		return f, a, z
	}
	f, a, z := build(false)
	r := f.rank(rankOpts())
	wantOrder(t, r, a, z)
	if r.Inputs.UnblocksUnavailable != 1 {
		t.Errorf("UnblocksUnavailable = %d, want 1", r.Inputs.UnblocksUnavailable)
	}
	if got := r.row(t, a).DecidingKey; got != KeyTiebreak {
		t.Errorf("deciding key = %q, want tiebreak (the key is 0 for both)", got)
	}
	f, a, z = build(true)
	r = f.rank(rankOpts())
	wantOrder(t, r, z, a)
	if r.Inputs.UnblocksUnavailable != 0 {
		t.Errorf("UnblocksUnavailable = %d, want 0 once hydrated", r.Inputs.UnblocksUnavailable)
	}
}

func TestStartedDefinitionPerType(t *testing.T) {
	f := rankFixture(t)
	seedPR := f.pr("o/r#1", prSpec{ownership: "mine"})
	reviewPR := f.pr("o/r#2", prSpec{requests: []string{fixtureSelf}})
	activeBead := f.bead("bd-active", issueSpec{state: "in_progress"})
	idleBead := f.bead("bd-idle", issueSpec{})
	blockedBead := f.bead("bd-blocked", issueSpec{state: "blocked"})
	jiraActive := f.issue("PROJ-1", issueSpec{state: "In Review", category: "indeterminate", assignee: "Pat Example"})
	jiraNew := f.issue("PROJ-2", issueSpec{state: "To Do", category: "new", assignee: "Pat Example"})
	r := f.rank(rankOpts())
	want := map[Key]string{
		seedPR: TierStarted, reviewPR: TierStarted, activeBead: TierStarted, jiraActive: TierStarted,
		idleBead: TierNotStarted, blockedBead: TierNotStarted, jiraNew: TierNotStarted,
	}
	for k, tier := range want {
		if got := r.row(t, k).Tier; got != tier {
			t.Errorf("%v tier = %q, want %q", k, got, tier)
		}
	}
}

func TestLinkedOnlyCandidateIsNotStarted(t *testing.T) {
	f := rankFixture(t)
	seed := f.bead("bd-seed", issueSpec{})
	teamPR := f.pr("o/r#9", prSpec{ownership: "team"})
	unassigned := f.issue("PROJ-7", issueSpec{state: "In Progress", category: "indeterminate"})
	unlabelled := f.issue("bd-unlabelled", issueSpec{state: "in_progress"})
	f.link(seed, teamPR, relationMentions)
	f.link(seed, unassigned, relationJira)
	f.link(seed, unlabelled, relationMentions)
	r := f.rank(rankOpts())
	for _, k := range []Key{teamPR, unassigned, unlabelled} {
		row := r.row(t, k)
		if row.Seed || row.Via == nil {
			t.Fatalf("%v is not linked-only: %+v", k, row.Candidate)
		}
		if row.Tier != TierNotStarted {
			t.Errorf("linked-only %v tier = %q, want not_started", k, row.Tier)
		}
	}
}

func TestLinkedOnlyJiraIssueIsNotStartedEvenInIndeterminateCategory(t *testing.T) {
	f := rankFixture(t)
	seed := f.pr("o/r#1", prSpec{ownership: "mine"})
	linked := f.issue("PROJ-1", issueSpec{state: "In Progress", category: "indeterminate", assignee: "Someone Else"})
	f.link(linked, seed, relationJira)
	r := f.rank(rankOpts())
	if got := r.row(t, linked).Tier; got != TierNotStarted {
		t.Errorf("tier = %q, want not_started", got)
	}
	if got := r.row(t, seed).Tier; got != TierStarted {
		t.Errorf("seed PR tier = %q, want started", got)
	}
}

func TestStartedFollowsJiraStatusCategory(t *testing.T) {
	f := rankFixture(t)
	// The category decides: a status named nothing like "In Progress" is
	// started in category indeterminate; one named exactly "In Progress" is
	// NOT started in category new (the name list is not read).
	waiting := f.issue("PROJ-1", issueSpec{state: "Waiting on Vendor", category: "indeterminate", assignee: "Pat Example"})
	misnamed := f.issue("PROJ-2", issueSpec{state: "In Progress", category: "new", assignee: "Pat Example"})
	f.issue("PROJ-3", issueSpec{state: "Complete", category: "done", assignee: "Pat Example"})
	r := f.rank(rankOpts())
	if got := r.row(t, waiting).Tier; got != TierStarted {
		t.Errorf("indeterminate category tier = %q, want started", got)
	}
	if got := r.row(t, misnamed).Tier; got != TierNotStarted {
		t.Errorf("category new named In Progress: tier = %q, want not_started", got)
	}
	if len(r.Rows) != 2 {
		t.Errorf("rows = %v, want the done-category issue out of the set", rowKeys(r))
	}
	if r.Inputs.StatusCategoryAbsent != 0 {
		t.Errorf("StatusCategoryAbsent = %d, want 0", r.Inputs.StatusCategoryAbsent)
	}
}

func TestAbsentCategoryFallsBackToStateName(t *testing.T) {
	f := rankFixture(t)
	named := f.issue("PROJ-1", issueSpec{state: "In Progress", assignee: "Pat Example"})
	other := f.issue("PROJ-2", issueSpec{state: "Open", assignee: "Pat Example"})
	r := f.rank(rankOpts())
	if got := r.row(t, named).Tier; got != TierStarted {
		t.Errorf("default name list: tier = %q, want started", got)
	}
	if got := r.row(t, other).Tier; got != TierNotStarted {
		t.Errorf("unlisted name: tier = %q, want not_started", got)
	}

	f = rankFixture(t)
	f.cfg.Jira = &config.JiraConfig{InProgressStatuses: []string{"Doing"}}
	doing := f.issue("PROJ-1", issueSpec{state: "doing", assignee: "Pat Example"})
	was := f.issue("PROJ-2", issueSpec{state: "In Progress", assignee: "Pat Example"})
	r = f.rank(rankOpts())
	if got := r.row(t, doing).Tier; got != TierStarted {
		t.Errorf("configured list, case-insensitive: tier = %q, want started", got)
	}
	if got := r.row(t, was).Tier; got != TierNotStarted {
		t.Errorf("a configured list replaces the default: tier = %q, want not_started", got)
	}
}

func TestRankInputsCountsAbsentCategory(t *testing.T) {
	f := rankFixture(t)
	f.issue("PROJ-1", issueSpec{state: "In Progress", assignee: "Pat Example"})
	f.issue("PROJ-2", issueSpec{state: "To Do", assignee: "Pat Example"})
	f.issue("PROJ-3", issueSpec{state: "In Progress", category: "indeterminate", assignee: "Pat Example"})
	// Beads and PRs have no category by nature and are not counted.
	f.bead("bd-1", issueSpec{})
	f.pr("o/r#1", prSpec{ownership: "mine"})
	r := f.rank(rankOpts())
	if r.Inputs.StatusCategoryAbsent != 2 {
		t.Errorf("StatusCategoryAbsent = %d, want 2", r.Inputs.StatusCategoryAbsent)
	}
}

func TestRankInputsCountsEveryDegradation(t *testing.T) {
	f := rankFixture(t)
	f.bead("bd-1", issueSpec{dueDate: "soon", priority: "urgent", createdAt: "2026-01-01T00:00:00Z"})
	f.issue("PROJ-1", issueSpec{state: "To Do", assignee: "Pat Example", dueDate: "x", priority: "y"})
	f.pr("o/r#1", prSpec{ownership: "mine"})
	r := f.rank(rankOpts())
	want := RankInputs{UnparseableDue: 2, UnmappedPriority: 2, AgeFallback: 2, UnblocksUnavailable: 1, StatusCategoryAbsent: 1}
	if r.Inputs != want {
		t.Errorf("Inputs = %+v, want %+v", r.Inputs, want)
	}
	// The count is per call, not accumulated.
	if again := f.rank(rankOpts()); again.Inputs != want {
		t.Errorf("second call Inputs = %+v, want %+v", again.Inputs, want)
	}
}

func TestCapLineAndForcePullRaisesCap(t *testing.T) {
	f := rankFixture(t)
	var keys []Key
	for i := 1; i <= 8; i++ {
		keys = append(keys, f.bead(fmt.Sprintf("bd-%d", i), issueSpec{}))
	}
	in := f.inputs()
	set := Candidates(in)
	inPlan := func(r Ranking) (n int) {
		for i, row := range r.Rows {
			if row.InPlan != (i < r.Cap) {
				t.Errorf("row %d (%v) InPlan = %v with cap %d", i+1, row.Key, row.InPlan, r.Cap)
			}
			if row.InPlan {
				n++
			}
		}
		return n
	}
	base := rankOpts()
	base.Cap = 3
	r3 := Rank(in, set, base)
	if r3.Cap != 3 || inPlan(r3) != 3 {
		t.Errorf("cap 3: Cap = %d, in plan = %d", r3.Cap, inPlan(r3))
	}
	// A force-pull raises the cap line by one: the order is unchanged and the
	// next row joins the plan. (The lock transaction that records it is the
	// select and pull verbs' own test.)
	base.Cap = 4
	r4 := Rank(in, set, base)
	if !reflect.DeepEqual(rowKeys(r3), rowKeys(r4)) {
		t.Errorf("the cap moved the order: %v vs %v", rowKeys(r3), rowKeys(r4))
	}
	if inPlan(r4) != 4 || !r4.Rows[3].InPlan || r3.Rows[3].InPlan {
		t.Errorf("raising the cap to 4 must bring row 4 into the plan")
	}
	// Default cap.
	base.Cap = 0
	if r := Rank(in, set, base); r.Cap != DefaultCap || inPlan(r) != DefaultCap {
		t.Errorf("default: Cap = %d, in plan = %d, want %d", r.Cap, inPlan(r), DefaultCap)
	}
	// A cap above the row count is fine.
	base.Cap = 50
	if r := Rank(in, set, base); inPlan(r) != len(keys) {
		t.Errorf("cap 50: in plan = %d, want all %d", inPlan(r), len(keys))
	}
}

func TestFinishedRowKeepsItsPlaceAndDoesNotUseTheCap(t *testing.T) {
	f := rankFixture(t)
	a := f.bead("bd-a", issueSpec{})
	b := f.bead("bd-b", issueSpec{})
	done := f.pr("o/r#1", prSpec{state: "merged", merged: true, ownership: "mine"})
	in := f.inputs()
	set := Candidates(in)
	if byKey(set)[done].Key == done {
		t.Fatal("a merged PR must not be a candidate on its own")
	}
	// Hand the rank a set that still holds the finished PR (a draft row whose
	// source finished after it was drafted).
	set.Candidates = append(set.Candidates, Candidate{Key: done, Kind: KindPR, Seed: true})
	opts := rankOpts()
	opts.Cap = 2
	r := Rank(in, set, opts)
	row := r.row(t, done)
	if !row.Finished || row.InPlan {
		t.Errorf("finished row = %+v, want Finished and not InPlan", row)
	}
	if !r.row(t, a).InPlan || !r.row(t, b).InPlan {
		t.Errorf("the finished row used a cap slot: %v", r.Rows)
	}
	if row.Position != 1 {
		t.Errorf("a finished started PR keeps its rank place: position %d, want 1", row.Position)
	}
}

func TestSourceLinkDoesNotJoinCorrelationGroupInTheRank(t *testing.T) {
	f := rankFixture(t)
	pr := f.pr("o/r#1", prSpec{ownership: "mine"})
	focus := f.bead("bd-focus", issueSpec{priority: "P0"})
	f.link(focus, pr, "source")
	r := f.rank(rankOpts())
	if got := r.row(t, pr).Priority; got != "P2" {
		t.Errorf("PR priority = %q, want P2: the focus bead's P0 must not leak through a source link", got)
	}
	if len(r.Rows) != 2 || len(r.Covered) != 0 {
		t.Errorf("rows = %v, covered = %v: a source link joins no group", rowKeys(r), r.Covered)
	}
}

func TestSlotRuleGroupsAndMergeFeedCovered(t *testing.T) {
	f := rankFixture(t)
	// An epic with an open child: the child takes the slot, the epic is covered.
	epic := f.bead("bd-epic", issueSpec{issueType: "epic"})
	child := f.bead("bd-child", issueSpec{parent: "bd-epic"})
	// A group of a PR and two beads: the PR represents it.
	pr := f.pr("o/r#1", prSpec{ownership: "mine"})
	g1 := f.bead("bd-g1", issueSpec{})
	g2 := f.bead("bd-g2", issueSpec{})
	f.link(g1, pr, relationWork)
	f.link(g2, pr, relationWork)
	// A merge: bd-kept absorbed bd-gone.
	kept := f.bead("bd-kept", issueSpec{})
	gone := f.bead("bd-gone", issueSpec{})
	f.external(kept, gone, relationReferences, MergeReason)
	// An epic child that the store does not hold.
	known := f.bead("bd-known", issueSpec{issueType: "epic", deps: []map[string]string{{"id": "bd-ghost", "type": "parent-child"}}})

	r := f.rank(rankOpts())
	if got := r.Covered[epic]; got != child {
		t.Errorf("Covered[epic] = %v, want %v (slot rule)", got, child)
	}
	if r.Covered[g1] != pr || r.Covered[g2] != pr {
		t.Errorf("Covered = %v, want both group beads covered by %v", r.Covered, pr)
	}
	if got := r.Covered[gone]; got != kept {
		t.Errorf("Covered[%v] = %v, want %v (merge link)", gone, got, kept)
	}
	for _, k := range []Key{epic, g1, g2} {
		for _, row := range r.Rows {
			if row.Key == k {
				t.Errorf("%v is covered and must not be a row", k)
			}
		}
	}
	if len(r.EpicsInPlay) != 1 || r.EpicsInPlay[0].Key != epic {
		t.Errorf("EpicsInPlay = %+v, want just %v", r.EpicsInPlay, epic)
	}
	if r.UnknownChildren[known] != 1 {
		t.Errorf("UnknownChildren = %v, want one unknown child of %v", r.UnknownChildren, known)
	}
	// The slot rule and the groups feed the cap: epic, g1 and g2 add no row.
	wantRows := map[Key]bool{pr: true, child: true, known: true, kept: true}
	if got := rowKeys(r); len(got) != len(wantRows) {
		t.Errorf("rows = %v, want exactly %v", got, wantRows)
	}
	for _, k := range rowKeys(r) {
		if !wantRows[k] {
			t.Errorf("unexpected row %v", k)
		}
	}
}

func TestPositionsAreDenseAndDecidingKeysChain(t *testing.T) {
	f := rankFixture(t)
	for i := 0; i < 5; i++ {
		f.bead(fmt.Sprintf("bd-%d", i), issueSpec{})
	}
	r := f.rank(rankOpts())
	for i, row := range r.Rows {
		if row.Position != i+1 {
			t.Errorf("row %d position = %d", i, row.Position)
		}
		last := i == len(r.Rows)-1
		if last && row.DecidingKey != "" || !last && row.DecidingKey != KeyTiebreak {
			t.Errorf("row %d deciding key = %q", i, row.DecidingKey)
		}
	}
	if empty := Rank(Inputs{Config: fixtureConfig()}, CandidateSet{}, rankOpts()); len(empty.Rows) != 0 || empty.Cap != DefaultCap {
		t.Errorf("empty ranking = %+v", empty)
	}
}

// TestRankReadsNoStoreAndHasNoSideEffects: Rank over the same Inputs twice
// leaves Inputs unchanged.
func TestRankDoesNotMutateItsInputs(t *testing.T) {
	f := rankFixture(t)
	f.bead("bd-1", issueSpec{dueDate: "2026-10-01"})
	f.pr("o/r#1", prSpec{ownership: "mine"})
	in := f.inputs()
	before := fmt.Sprintf("%v", in)
	set := Candidates(in)
	setBefore := fmt.Sprintf("%v", set)
	Rank(in, set, rankOpts())
	if fmt.Sprintf("%v", in) != before || fmt.Sprintf("%v", set) != setBefore {
		t.Error("Rank changed its inputs")
	}
}
