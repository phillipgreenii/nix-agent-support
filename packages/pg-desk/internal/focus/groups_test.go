package focus

import (
	"reflect"
	"testing"
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
	set, gs := groupRun(f)
	wantCandidates(t, set, pr, anchor, item, alone)
	if len(gs) != 1 {
		t.Fatalf("groups = %+v, want exactly one (a candidate joined to nothing is no group)", gs)
	}
	// Members are in candidate order: pr, then the beads by key.
	if want := []Key{pr, anchor, item}; !reflect.DeepEqual(gs[0].Members, want) {
		t.Errorf("members = %v, want %v", gs[0].Members, want)
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
	set, gs := groupRun(f)
	wantCandidates(t, set, pr, focus, viaMention, viaExternal)
	if len(gs) != 0 {
		t.Errorf("groups = %+v, want none: only a derived work link joins a group", gs)
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
	_, gs := groupRun(f)
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
