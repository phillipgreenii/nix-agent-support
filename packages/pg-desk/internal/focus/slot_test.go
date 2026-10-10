package focus

import (
	"reflect"
	"testing"
)

// slotRun computes the candidate set and applies the slot rule over it in
// candidate order (the rank only reorders; the rule is order-independent
// except for Slotted's order).
func slotRun(f *fixture) (Inputs, CandidateSet, SlotResult) {
	f.t.Helper()
	in := f.inputs()
	set := Candidates(in)
	return in, set, ApplySlotRule(in, set.Candidates)
}

func slottedKeys(r SlotResult) []Key {
	var out []Key
	for _, c := range r.Slotted {
		out = append(out, c.Key)
	}
	return out
}

func entryKeys(r SlotResult) []Key {
	var out []Key
	for _, e := range r.EpicsInPlay {
		out = append(out, e.Key)
	}
	return out
}

func wantKeys(t *testing.T, what string, got []Key, want ...Key) {
	t.Helper()
	if len(got) == 0 && len(want) == 0 {
		return
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s = %v, want %v", what, got, want)
	}
}

func epic(f *fixture, id string, s issueSpec) Key {
	s.issueType = "epic"
	if s.state == "" {
		s.state = "open"
	}
	return f.issue(id, s)
}

func TestEpicSlotOneChildHidesEpic(t *testing.T) {
	f := newFixture(t)
	e := epic(f, "bd-epic", issueSpec{labels: []string{PlanableLabel}})
	c := f.issue("bd-child", issueSpec{state: "open", labels: []string{PlanableLabel}, parent: "bd-epic"})
	_, set, r := slotRun(f)
	wantCandidates(t, set, e, c)
	wantKeys(t, "slotted", slottedKeys(r), c)
	if r.Suppressed[e] != c || len(r.Suppressed) != 1 {
		t.Errorf("suppressed = %v, want only the epic covered by its child", r.Suppressed)
	}
	if want := []EpicEntry{{Key: e, InPlay: 0, Total: 1}}; !reflect.DeepEqual(r.EpicsInPlay, want) {
		t.Errorf("epics in play = %v, want %v", r.EpicsInPlay, want)
	}
}

func TestEpicWithNoOpenChildIsCandidate(t *testing.T) {
	f := newFixture(t)
	// an epic with no child at all, and one whose only child is closed
	bare := epic(f, "bd-bare", issueSpec{labels: []string{PlanableLabel}})
	done := epic(f, "bd-done", issueSpec{labels: []string{PlanableLabel}})
	f.issue("bd-done.1", issueSpec{state: "closed", parent: "bd-done"})
	_, _, r := slotRun(f)
	wantKeys(t, "slotted", slottedKeys(r), bare, done)
	if len(r.Suppressed) != 0 || len(r.EpicsInPlay) != 0 {
		t.Errorf("suppressed %v, block %v: want neither", r.Suppressed, r.EpicsInPlay)
	}
}

// An open child that is not a candidate still covers its epic.
func TestEpicChildThatIsNotACandidateCounts(t *testing.T) {
	f := newFixture(t)
	e := epic(f, "bd-epic", issueSpec{labels: []string{PlanableLabel}})
	c := f.issue("bd-child", issueSpec{state: "open", parent: "bd-epic"})
	_, set, r := slotRun(f)
	wantCandidates(t, set, e)
	wantKeys(t, "slotted", slottedKeys(r))
	if r.Suppressed[e] != c {
		t.Errorf("suppressed = %v, want the epic covered by its non-candidate child", r.Suppressed)
	}
	wantKeys(t, "block", entryKeys(r), e)
}

// A child the store does not hold yet is unknown, not absent: the epic is
// listed as it would be with no known child, and the count is reported.
func TestEpicChildNotInStoreYet(t *testing.T) {
	f := newFixture(t)
	e := epic(f, "bd-epic", issueSpec{
		labels: []string{PlanableLabel},
		deps: []map[string]string{
			{"id": "bd-epic.1", "type": "parent-child"},
			{"id": "bd-epic.2", "type": "parent-child"},
			{"id": "bd-other", "type": "blocks"},
		},
	})
	_, _, r := slotRun(f)
	wantKeys(t, "slotted", slottedKeys(r), e)
	if r.UnknownChildren[e] != 2 || len(r.UnknownChildren) != 1 {
		t.Errorf("unknown children = %v, want 2 for the epic (a blocks edge is not a child)", r.UnknownChildren)
	}
	if len(r.Suppressed) != 0 || len(r.EpicsInPlay) != 0 {
		t.Errorf("suppressed %v, block %v: an epic with no known child is listed as a candidate", r.Suppressed, r.EpicsInPlay)
	}

	// Once the child is stored it is known, and covers the epic.
	f2 := newFixture(t)
	e2 := epic(f2, "bd-epic", issueSpec{
		labels: []string{PlanableLabel},
		deps:   []map[string]string{{"id": "bd-epic.1", "type": "parent-child"}},
	})
	f2.issue("bd-epic.1", issueSpec{state: "open", parent: "bd-epic"})
	_, _, r2 := slotRun(f2)
	if len(r2.UnknownChildren) != 0 {
		t.Errorf("unknown children = %v, want none once the child is stored", r2.UnknownChildren)
	}
	if _, ok := r2.Suppressed[e2]; !ok {
		t.Errorf("suppressed = %v, want the epic covered by the stored child", r2.Suppressed)
	}
}

func TestEpicSlotChildStatusMatrix(t *testing.T) {
	cases := []struct {
		name     string
		spec     issueSpec
		hide     bool
		covers   bool // the child covers (suppresses) the epic
		wantKind string
	}{
		{name: "open", spec: issueSpec{state: "open"}, covers: true},
		{name: "in_progress", spec: issueSpec{state: "in_progress"}, covers: true},
		{name: "blocked", spec: issueSpec{state: "blocked"}, covers: true},
		{name: "deferred", spec: issueSpec{state: "deferred"}},
		{name: "closed", spec: issueSpec{state: "closed"}},
		{name: "inactive", spec: issueSpec{state: "open", inactive: true}},
		{name: "hidden", spec: issueSpec{state: "open"}, hide: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			e := epic(f, "bd-epic", issueSpec{labels: []string{PlanableLabel}})
			tc.spec.parent = "bd-epic"
			c := f.issue("bd-child", tc.spec)
			if tc.hide {
				f.hide(c)
			}
			in, _, r := slotRun(f)
			if got := len(OpenChildren(in, e)) == 1; got != tc.covers {
				t.Errorf("open child = %v, want %v", got, tc.covers)
			}
			if tc.covers {
				if r.Suppressed[e] != c || len(r.Slotted) != 0 {
					t.Errorf("suppressed %v slotted %v: want the epic covered", r.Suppressed, slottedKeys(r))
				}
				return
			}
			wantKeys(t, "slotted", slottedKeys(r), e)
			if len(r.Suppressed) != 0 {
				t.Errorf("suppressed = %v, want none", r.Suppressed)
			}
		})
	}
}

// A blocked child that is a candidate is a candidate in its own right and
// still covers its epic.
func TestEpicSlotBlockedChildCountsAsOpen(t *testing.T) {
	f := newFixture(t)
	e := epic(f, "bd-epic", issueSpec{labels: []string{PlanableLabel}})
	c := f.issue("bd-child", issueSpec{state: "blocked", labels: []string{PlanableLabel}, parent: "bd-epic"})
	_, set, r := slotRun(f)
	wantCandidates(t, set, e, c)
	wantKeys(t, "slotted", slottedKeys(r), c)
	if r.Suppressed[e] != c {
		t.Errorf("suppressed = %v, want the epic covered by the blocked child", r.Suppressed)
	}
}

func TestEpicSlotTwoChildrenTwoSlots(t *testing.T) {
	f := newFixture(t)
	e := epic(f, "bd-epic", issueSpec{labels: []string{PlanableLabel}})
	c1 := f.issue("bd-c1", issueSpec{state: "open", labels: []string{PlanableLabel}, parent: "bd-epic"})
	c2 := f.issue("bd-c2", issueSpec{state: "in_progress", labels: []string{PlanableLabel}, parent: "bd-epic"})
	f.issue("bd-c3", issueSpec{state: "open", parent: "bd-epic"}) // a third, not a candidate
	_, _, r := slotRun(f)
	wantKeys(t, "slotted", slottedKeys(r), c1, c2)
	if r.Suppressed[e] != c1 {
		t.Errorf("suppressed = %v, want the FIRST candidate child (bd-c1)", r.Suppressed)
	}
	if want := []EpicEntry{{Key: e, Total: 3}}; !reflect.DeepEqual(r.EpicsInPlay, want) {
		t.Errorf("epics in play = %v, want %v", r.EpicsInPlay, want)
	}
}

// A's child epic B has an open child C: A and B are both in the block and C
// takes the slot; the in-plan indicator counts descendants transitively.
func TestEpicSlotNestedEpicsTransitive(t *testing.T) {
	f := newFixture(t)
	a := epic(f, "bd-a", issueSpec{labels: []string{PlanableLabel}})
	b := epic(f, "bd-b", issueSpec{parent: "bd-a"})
	c := f.issue("bd-c", issueSpec{state: "open", labels: []string{PlanableLabel}, parent: "bd-b"})
	in := f.inputs()
	in.PlanKeys[c] = true
	set := Candidates(in)
	wantCandidates(t, set, a, c)
	r := ApplySlotRule(in, set.Candidates)
	wantKeys(t, "slotted", slottedKeys(r), c)
	if r.Suppressed[a] != b {
		t.Errorf("suppressed = %v, want A covered by its open child B", r.Suppressed)
	}
	want := []EpicEntry{{Key: a, InPlay: 1, Total: 2}, {Key: b, InPlay: 1, Total: 1}}
	if !reflect.DeepEqual(r.EpicsInPlay, want) {
		t.Errorf("epics in play = %v, want %v", r.EpicsInPlay, want)
	}

	// A is not itself a candidate: it is still listed, through nested B.
	f2 := newFixture(t)
	a2 := epic(f2, "bd-a", issueSpec{})
	b2 := epic(f2, "bd-b", issueSpec{parent: "bd-a"})
	c2 := f2.issue("bd-c", issueSpec{state: "open", labels: []string{PlanableLabel}, parent: "bd-b"})
	_, _, r2 := slotRun(f2)
	wantKeys(t, "slotted", slottedKeys(r2), c2)
	wantKeys(t, "block", entryKeys(r2), a2, b2)
}

func TestEpicsInPlayBlockMembership(t *testing.T) {
	f := newFixture(t)
	// 1: not a candidate, with an open child that is a candidate: listed.
	e1 := epic(f, "bd-e1", issueSpec{})
	c1 := f.issue("bd-e1.1", issueSpec{state: "open", labels: []string{PlanableLabel}, parent: "bd-e1"})
	// 2: a candidate suppressed by an open child that is not a candidate: listed.
	e2 := epic(f, "bd-e2", issueSpec{labels: []string{PlanableLabel}})
	f.issue("bd-e2.1", issueSpec{state: "open", parent: "bd-e2"})
	// 3: not a candidate, open child not a candidate: in neither place.
	e3 := epic(f, "bd-e3", issueSpec{})
	f.issue("bd-e3.1", issueSpec{state: "open", parent: "bd-e3"})
	// 4: not a candidate, no open child: in neither place.
	epic(f, "bd-e4", issueSpec{})
	f.issue("bd-e4.1", issueSpec{state: "closed", parent: "bd-e4"})
	// 5: a candidate with no open child: ranks normally, not in the block.
	e5 := epic(f, "bd-e5", issueSpec{labels: []string{PlanableLabel}})
	_, _, r := slotRun(f)
	wantKeys(t, "block", entryKeys(r), e1, e2)
	wantKeys(t, "slotted", slottedKeys(r), c1, e5)
	for _, k := range []Key{e3, e5} {
		for _, in := range entryKeys(r) {
			if in == k {
				t.Errorf("%v is in the block, want neither", k)
			}
		}
	}
	if _, ok := r.Suppressed[e2]; !ok || len(r.Suppressed) != 1 {
		t.Errorf("suppressed = %v, want only e2", r.Suppressed)
	}
}

func TestEpicsInPlayBlockIsSortedAndNotCounted(t *testing.T) {
	f := newFixture(t)
	// Created out of order; the block is (kind, key): jira before bead, then key.
	z := epic(f, "bd-z", issueSpec{labels: []string{PlanableLabel}})
	a := epic(f, "bd-a", issueSpec{labels: []string{PlanableLabel}})
	j := f.issue("EP-9", issueSpec{state: "In Progress", category: "indeterminate", assignee: "Pat Example", issueType: "Epic"})
	for _, p := range []string{"bd-z", "bd-a"} {
		f.issue(p+".1", issueSpec{state: "open", labels: []string{PlanableLabel}, parent: p})
	}
	f.issue("EP-9a", issueSpec{state: "To Do", category: "new", assignee: "Pat Example", parent: "EP-9"})
	_, set, r := slotRun(f)
	wantKeys(t, "block", entryKeys(r), j, a, z)
	// Four children are candidates; none of the three epics holds a slot.
	wantKeys(t, "slotted", slottedKeys(r),
		Key{"issue", "EP-9a"}, Key{"issue", "bd-a.1"}, Key{"issue", "bd-z.1"})
	if len(set.Candidates) != 6 {
		t.Errorf("candidates = %d, want 6 (three epics and three children)", len(set.Candidates))
	}
	for _, c := range r.Slotted {
		for _, e := range r.EpicsInPlay {
			if c.Key == e.Key {
				t.Errorf("%v is both slotted and in the block", c.Key)
			}
		}
	}
}

// The connector now maps a Jira child's parent, so the rule holds for Jira
// epics: the epic and its assigned child never use more than one slot.
func TestJiraEpicSlotRuleAppliesWhenParentMapped(t *testing.T) {
	f := newFixture(t)
	e := f.issue("EP-1", issueSpec{state: "In Progress", category: "indeterminate", assignee: "Pat Example", issueType: "Epic"})
	c := f.issue("EP-2", issueSpec{state: "In Progress", category: "indeterminate", assignee: "Pat Example", parent: "EP-1"})
	_, set, r := slotRun(f)
	wantCandidates(t, set, e, c)
	wantKeys(t, "slotted", slottedKeys(r), c)
	if r.Suppressed[e] != c {
		t.Errorf("suppressed = %v, want the Jira epic covered by its child", r.Suppressed)
	}
	wantKeys(t, "block", entryKeys(r), e)

	// A child in a done status category does not cover it.
	f2 := newFixture(t)
	e2 := f2.issue("EP-1", issueSpec{state: "In Progress", category: "indeterminate", assignee: "Pat Example", issueType: "Epic"})
	f2.issue("EP-2", issueSpec{state: "Complete", category: "done", assignee: "Pat Example", parent: "EP-1"})
	// ... nor one done by state name when no category exists
	f2.issue("EP-3", issueSpec{state: "Closed", parent: "EP-1"})
	// ... but an unassigned, unlabelled open child does.
	_, _, r2 := slotRun(f2)
	wantKeys(t, "slotted", slottedKeys(r2), e2)
	f2.issue("EP-4", issueSpec{state: "To Do", category: "new", parent: "EP-1"})
	_, _, r3 := slotRun(f2)
	if _, ok := r3.Suppressed[e2]; !ok || len(r3.Slotted) != 0 {
		t.Errorf("suppressed %v slotted %v, want the epic covered by the not-done child", r3.Suppressed, slottedKeys(r3))
	}
}

func TestIsEpicAndOpenChildren(t *testing.T) {
	f := newFixture(t)
	byType := f.issue("bd-t", issueSpec{state: "open", issueType: "EPIC"})
	byParent := f.issue("bd-p", issueSpec{state: "open"})
	f.issue("bd-p.2", issueSpec{state: "open", parent: "bd-p"})
	f.issue("bd-p.1", issueSpec{state: "blocked", parent: "bd-p"})
	plain := f.issue("bd-x", issueSpec{state: "open", issueType: "task"})
	pr := f.pr("o/r#1", prSpec{ownership: "mine"})
	in := f.inputs()
	for k, want := range map[Key]bool{byType: true, byParent: true, plain: false, pr: false, {"issue", "nope"}: false} {
		if got := IsEpic(in, k); got != want {
			t.Errorf("IsEpic(%v) = %v, want %v", k, got, want)
		}
	}
	wantKeys(t, "open children", OpenChildren(in, byParent), Key{"issue", "bd-p.1"}, Key{"issue", "bd-p.2"})
}

// A stored parent cycle is corrupt data; the rule must terminate.
func TestSlotRuleSurvivesAParentCycle(t *testing.T) {
	f := newFixture(t)
	epic(f, "bd-a", issueSpec{labels: []string{PlanableLabel}, parent: "bd-b"})
	epic(f, "bd-b", issueSpec{labels: []string{PlanableLabel}, parent: "bd-a"})
	_, _, r := slotRun(f)
	if len(r.Suppressed) != 2 {
		t.Errorf("suppressed = %v, want both covered by each other", r.Suppressed)
	}
}
