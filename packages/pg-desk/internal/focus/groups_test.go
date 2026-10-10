package focus

import (
	"reflect"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/interpret"
)

// groupRun computes the groups of the candidate set of the fixture.
func groupRun(f *fixture) (CandidateSet, []Group) {
	f.t.Helper()
	in := f.inputs()
	set := Candidates(in)
	return set, Groups(in, set)
}

func TestCorrelationGroupTakesOneSlot(t *testing.T) {
	f := newFixture(t)
	pr := f.pr("o/r#1", prSpec{ownership: "mine"})
	anchor := f.issue("bd-anchor", issueSpec{state: "open", labels: []string{PlanableLabel}})
	item := f.issue("bd-item", issueSpec{state: "open", labels: []string{PlanableLabel}})
	alone := f.issue("bd-alone", issueSpec{state: "open", labels: []string{PlanableLabel}})
	f.link(anchor, pr, relationWork)
	f.link(item, pr, relationWork)
	in := f.inputs()
	set := Candidates(in)
	gs := Groups(in, set)
	wantCandidates(t, set, pr, anchor, item, alone)
	if len(gs) != 1 {
		t.Fatalf("groups = %+v, want exactly one (a candidate joined to nothing is no group)", gs)
	}
	// Members are in candidate order: pr, then the beads by key.
	if want := []Key{pr, anchor, item}; !reflect.DeepEqual(gs[0].Members, want) {
		t.Errorf("members = %v, want %v", gs[0].Members, want)
	}
	// Through the rank: the group is represented by its highest-ranked member
	// (the started PR) and consumes ONE slot, so with a cap of 2 the group and
	// the lone bead fill the plan.
	r := Rank(in, set, RankOptions{Date: rankDay, Cap: 2, Clock: interpret.FixedClock(fixtureNow)})
	wantOrder(t, r, pr, alone)
	if !r.Rows[0].InPlan || !r.Rows[1].InPlan {
		t.Errorf("rows = %+v: the group must take one slot, leaving one for the lone bead", r.Rows)
	}
	if r.Covered[anchor] != pr || r.Covered[item] != pr {
		t.Errorf("Covered = %v, want the anchor and the item covered by the PR", r.Covered)
	}
}

// Only a DERIVED work link joins a group: a source link (a minted focus
// bead to its entity), a mentions link, and an externally recorded work link
// do not.
func TestSourceLinkDoesNotJoinCorrelationGroup(t *testing.T) {
	f := newFixture(t)
	pr := f.pr("o/r#1", prSpec{ownership: "mine"})
	focus := f.issue("bd-focus", issueSpec{state: "open", labels: []string{PlanableLabel}, priority: "P0"})
	viaMention := f.issue("bd-mention", issueSpec{state: "open", labels: []string{PlanableLabel}})
	viaExternal := f.issue("bd-external", issueSpec{state: "open", labels: []string{PlanableLabel}})
	f.link(focus, pr, "source")
	f.link(viaMention, pr, relationMentions)
	f.external(viaExternal, pr, relationWork, "operator link")
	in := f.inputs()
	set := Candidates(in)
	gs := Groups(in, set)
	wantCandidates(t, set, pr, focus, viaMention, viaExternal)
	if len(gs) != 0 {
		t.Errorf("groups = %+v, want none: only a derived work link joins a group", gs)
	}
	// End to end through the rank: nothing is grouped, so each is a row of its
	// own and the focus bead's P0 does not leak into its PR's priority.
	r := Rank(in, set, RankOptions{Date: rankDay, Clock: interpret.FixedClock(fixtureNow)})
	if len(r.Rows) != 4 || len(r.Covered) != 0 {
		t.Errorf("rows = %v, covered = %v, want four separate rows", rowKeys(r), r.Covered)
	}
	if got := r.row(t, pr).Priority; got != "P2" {
		t.Errorf("PR priority = %q, want P2: a source link must not carry the bead's P0", got)
	}
}

// A work link to or from an entity that is not a candidate joins nothing.
func TestCorrelationGroupIgnoresNonCandidates(t *testing.T) {
	f := newFixture(t)
	pr := f.pr("o/r#1", prSpec{ownership: "mine"})
	workItem := f.issue("bd-wi", issueSpec{state: "open", labels: []string{PlanableLabel}, metadata: map[string]string{MetaDedupKey: "k"}})
	f.link(workItem, pr, relationWork)
	_, gs := groupRun(f)
	if len(gs) != 0 {
		t.Errorf("groups = %+v, want none: a work-item bead is not a candidate", gs)
	}
}

// Group-level inputs: the raw per-member due and priority are exposed, and
// the earliest due and the highest priority are picked for the rank to read.
func TestCorrelatedDueDateInheritance(t *testing.T) {
	f := newFixture(t)
	pr := f.pr("o/r#1", prSpec{ownership: "mine"})
	b1 := f.issue("bd-1", issueSpec{state: "open", labels: []string{PlanableLabel}, dueDate: "2026-10-15", priority: "P3"})
	b2 := f.issue("bd-2", issueSpec{state: "open", labels: []string{PlanableLabel}, dueDate: "2026-10-12T09:00:00Z", priority: "P1"})
	b3 := f.issue("bd-3", issueSpec{state: "open", labels: []string{PlanableLabel}, dueDate: "next tuesday", priority: "urgent"})
	for _, b := range []Key{b1, b2, b3} {
		f.link(b, pr, relationWork)
	}
	f.cfg.Focus.TimeZone = "UTC"
	in := f.inputs()
	set := Candidates(in)
	gs := Groups(in, set)
	if len(gs) != 1 {
		t.Fatalf("groups = %+v, want one", gs)
	}
	g := gs[0]
	if g.EarliestDue != "2026-10-12T09:00:00Z" {
		t.Errorf("EarliestDue = %q, want the earliest parseable member due", g.EarliestDue)
	}
	if g.HighestPriority != "P1" {
		t.Errorf("HighestPriority = %q, want P1", g.HighestPriority)
	}
	wantDue := map[Key]string{pr: "", b1: "2026-10-15", b2: "2026-10-12T09:00:00Z", b3: "next tuesday"}
	if !reflect.DeepEqual(g.MemberDue, wantDue) {
		t.Errorf("MemberDue = %v, want %v (raw strings, unparsed values kept for the rank's counter)", g.MemberDue, wantDue)
	}
	wantPrio := map[Key]string{pr: "", b1: "P3", b2: "P1", b3: "urgent"}
	if !reflect.DeepEqual(g.MemberPriority, wantPrio) {
		t.Errorf("MemberPriority = %v, want %v", g.MemberPriority, wantPrio)
	}

	// End to end through the rank: the group is ONE row (the started PR),
	// carrying the earliest due DAY of the group (read in the focus zone) and
	// its highest priority; the two degradations are counted.
	r := Rank(in, set, RankOptions{Date: rankDay, Clock: interpret.FixedClock(fixtureNow)})
	if len(r.Rows) != 1 || r.Rows[0].Key != pr {
		t.Fatalf("rows = %v, want the PR alone", rowKeys(r))
	}
	if r.Rows[0].Due != "2026-10-12" || r.Rows[0].Priority != "P1" {
		t.Errorf("inherited due/priority = %q/%q, want 2026-10-12/P1", r.Rows[0].Due, r.Rows[0].Priority)
	}
	for _, b := range []Key{b1, b2, b3} {
		if r.Covered[b] != pr {
			t.Errorf("Covered[%v] = %v, want %v", b, r.Covered[b], pr)
		}
	}
	if r.Inputs.UnparseableDue != 1 || r.Inputs.UnmappedPriority != 1 {
		t.Errorf("Inputs = %+v, want one unparseable due (b3) and one unmapped priority (b3)", r.Inputs)
	}
	// The inherited date decides the TIER: a group whose earliest due is before
	// the addressed day is overdue, though the PR has no date of its own.
	later := Rank(in, set, RankOptions{Date: time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC), Clock: interpret.FixedClock(fixtureNow)})
	if later.Rows[0].Tier != TierOverdue {
		t.Errorf("tier on Oct 13 = %q, want overdue through the inherited due date", later.Rows[0].Tier)
	}
}

func TestGroupsOfNothingAreEmpty(t *testing.T) {
	f := newFixture(t)
	if _, gs := groupRun(f); len(gs) != 0 {
		t.Errorf("groups = %+v, want none", gs)
	}
	g := Group{}
	if g.EarliestDue != "" || g.HighestPriority != "" {
		t.Error("a zero group has no inherited values")
	}
}

// A chain of work links is one group (the component, not one hop).
func TestCorrelationGroupIsTheConnectedComponent(t *testing.T) {
	f := newFixture(t)
	pr1 := f.pr("o/r#1", prSpec{ownership: "mine"})
	pr2 := f.pr("o/r#2", prSpec{ownership: "mine"})
	bead := f.issue("bd-1", issueSpec{state: "open", labels: []string{PlanableLabel}})
	f.link(bead, pr1, relationWork)
	f.link(bead, pr2, relationWork)
	_, gs := groupRun(f)
	if len(gs) != 1 || !reflect.DeepEqual(gs[0].Members, []Key{pr1, pr2, bead}) {
		t.Errorf("groups = %+v, want one group of both PRs and the bead", gs)
	}
}
