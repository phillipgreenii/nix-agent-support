package focus

import (
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

func TestLabelledBeadIsASeed(t *testing.T) {
	for _, state := range []string{"open", "in_progress", "blocked"} {
		f := newFixture(t)
		k := f.issue("bd-1", issueSpec{state: state, labels: []string{"other", PlanableLabel}})
		s := f.set()
		wantCandidates(t, s, k)
		if c := byKey(s)[k]; !c.Seed || c.Kind != KindBead || c.Via != nil {
			t.Errorf("state %s: candidate = %+v, want a bead seed with no via", state, c)
		}
	}
}

func TestUnlabelledAssignedBeadIsNotACandidate(t *testing.T) {
	f := newFixture(t)
	f.issue("bd-1", issueSpec{state: "open", assignee: "Pat Example"})
	f.issue("bd-2", issueSpec{state: "in_progress", assignee: fixtureSelf})
	wantNone(t, f.set())
}

func TestBeadOwnerAloneIsNotACandidate(t *testing.T) {
	f := newFixture(t)
	f.issue("bd-1", issueSpec{state: "open", owner: "pat@example.test"})
	wantNone(t, f.set())
}

func TestDeferredLabelledBeadIsNotACandidate(t *testing.T) {
	f := newFixture(t)
	k := f.issue("bd-1", issueSpec{state: "deferred", labels: []string{PlanableLabel}})
	in := f.inputs()
	wantNone(t, Candidates(in))
	if got := ExplainCandidate(in, k); got.IsCandidate || len(got.Reasons) != 1 || got.Reasons[0] != ReasonDeferred {
		t.Errorf("explain = %+v, want not a candidate, reason deferred", got)
	}
}

func TestClosedLabelledBeadIsNotACandidate(t *testing.T) {
	f := newFixture(t)
	k := f.issue("bd-1", issueSpec{state: "closed", labels: []string{PlanableLabel}})
	in := f.inputs()
	wantNone(t, Candidates(in))
	if got := ExplainCandidate(in, k); got.IsCandidate || got.Reasons[0] != ReasonTerminal {
		t.Errorf("explain = %+v, want terminal", got)
	}
}

// A labelled bead a worker claims (the assignee becomes a session actor id)
// stays a seed: assignment never matters for a bead.
func TestLabelledBeadClaimedByWorkerStaysACandidate(t *testing.T) {
	f := newFixture(t)
	k := f.issue("bd-1", issueSpec{state: "in_progress", labels: []string{PlanableLabel}, assignee: "0a1b2c-drain"})
	wantCandidates(t, f.set(), k)
}

func TestBlockedLabelledChildIsCandidate(t *testing.T) {
	f := newFixture(t)
	epic := f.issue("bd-epic", issueSpec{state: "open", issueType: "epic"})
	child := f.issue("bd-child", issueSpec{state: "blocked", labels: []string{PlanableLabel}})
	f.link(child, epic, relationParent)
	s := f.set()
	wantCandidates(t, s, child, epic)
	if c := byKey(s)[child]; !c.Seed {
		t.Errorf("the blocked labelled child = %+v, want a seed", c)
	}
}

func TestJiraCandidacyUsesAssigneeIdentityList(t *testing.T) {
	tests := []struct {
		name       string
		assignee   string
		identities []string // nil keeps the fixture's list; empty (non-nil) clears it
		want       bool
	}{
		{name: "display name", assignee: "Pat Example", want: true},
		{name: "email", assignee: "pat@example.test", want: true},
		{name: "a multi-word name", assignee: "Mary Jane Watson", want: true},
		{name: "surrounding whitespace is trimmed", assignee: "  Pat Example\t", want: true},
		{name: "case-sensitive", assignee: "pat example", want: false},
		{name: "a substring is not a match", assignee: "Pat", want: false},
		{name: "another person", assignee: "Someone Else", want: false},
		{name: "a session actor id is out", assignee: "81b6af17-a7c2-49e5-939e-4338367af166-drain", want: false},
		{name: "the self login is not an identity", assignee: fixtureSelf, want: false},
		{name: "unassigned", assignee: "", want: false},
		{name: "an empty list yields none", assignee: "Pat Example", identities: []string{}, want: false},
		{name: "a blank identity matches nothing", assignee: "", identities: []string{"  "}, want: false},
		{name: "an identity entry is trimmed too", assignee: "Pat Example", identities: []string{" Pat Example "}, want: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			if tc.identities != nil {
				f.cfg.Focus.OperatorIdentities = tc.identities
			}
			k := f.issue("PROJ-1", issueSpec{state: "In Progress", category: "indeterminate", assignee: tc.assignee})
			s := f.set()
			if tc.want {
				wantCandidates(t, s, k)
				if c := byKey(s)[k]; !c.Seed || c.Kind != KindJira {
					t.Errorf("candidate = %+v, want a Jira seed", c)
				}
			} else {
				wantNone(t, s)
			}
		})
	}
}

// Candidacy reads the stored row, never which query listed it: an issue
// that happens to be stored (as any gathered one is) is judged by its
// assignee alone.
func TestTrimmedAssigneeMatch(t *testing.T) {
	f := newFixture(t)
	k := f.issue("PROJ-1", issueSpec{state: "To Do", category: "new", assignee: " \tPat Example \n"})
	wantCandidates(t, f.set(), k)
}

func TestJiraIssueDoneByCategoryIsNotACandidate(t *testing.T) {
	f := newFixture(t)
	f.issue("PROJ-1", issueSpec{state: "Complete", category: "done", assignee: "Pat Example"})
	wantNone(t, f.set())
}

func TestPRCandidatePredicate(t *testing.T) {
	tests := []struct {
		name string
		spec prSpec
		want bool
	}{
		{name: "mine", spec: prSpec{ownership: "mine"}, want: true},
		{name: "co-owned", spec: prSpec{ownership: "co-owned"}, want: true},
		{name: "a requested review of the operator", spec: prSpec{ownership: "team", requests: []string{"x", fixtureSelf}}, want: true},
		{name: "a requested review with no interpretation row", spec: prSpec{requests: []string{fixtureSelf}}, want: true},
		{name: "another team PR is not a seed", spec: prSpec{ownership: "team", requests: []string{"someone-else"}}},
		{name: "no ownership and no request", spec: prSpec{}},
		{name: "a closed mine PR", spec: prSpec{state: "closed", ownership: "mine"}},
		{name: "a merged mine PR", spec: prSpec{state: "merged", ownership: "mine"}},
		{name: "the merged flag alone", spec: prSpec{ownership: "mine", merged: true}},
		{name: "an inactive mine PR", spec: prSpec{ownership: "mine", inactive: true}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			k := f.pr("7", tc.spec)
			s := f.set()
			if tc.want {
				wantCandidates(t, s, k)
				if c := byKey(s)[k]; !c.Seed || c.Kind != KindPR {
					t.Errorf("candidate = %+v, want a PR seed", c)
				}
			} else {
				wantNone(t, s)
			}
		})
	}
}

func TestPRReviewRequestNeedsASelfLogin(t *testing.T) {
	f := newFixture(t)
	f.cfg.SelfLogin = ""
	f.pr("7", prSpec{requests: []string{""}})
	wantNone(t, f.set())
}

func TestEmptyCandidateSetExitsZero(t *testing.T) {
	f := newFixture(t)
	s := f.set()
	wantNone(t, s)
	if s.Counts != (SourceCounts{}) {
		t.Errorf("counts = %+v, want zero", s.Counts)
	}
	// An empty store is a valid result with no panic on a zero Inputs either.
	if got := Candidates(Inputs{}); len(got.Candidates) != 0 {
		t.Errorf("zero Inputs: %v", got)
	}
}

func TestMintedBeadIsNeverACandidate(t *testing.T) {
	f := newFixture(t)
	// Labelled, and linked to a seed: both routes are shut by source_id.
	minted := f.issue("bd-minted", issueSpec{state: "open", labels: []string{PlanableLabel}, metadata: map[string]string{MetaSourceID: "pr:7"}})
	seed := f.pr("7", prSpec{ownership: "mine"})
	f.link(minted, seed, relationWork)
	in := f.inputs()
	wantCandidates(t, Candidates(in), seed)
	if got := ExplainCandidate(in, minted); got.IsCandidate || len(got.Reasons) != 1 || got.Reasons[0] != ReasonMintedBead {
		t.Errorf("explain = %+v, want minted-bead", got)
	}
}

func TestWorkItemBeadWithDedupKeyIsNotACandidate(t *testing.T) {
	f := newFixture(t)
	item := f.issue("bd-review-7", issueSpec{state: "open", labels: []string{PlanableLabel}, assignee: "Pat Example", metadata: map[string]string{MetaDedupKey: "k"}})
	seed := f.pr("7", prSpec{ownership: "mine"})
	f.link(item, seed, relationWork)
	in := f.inputs()
	wantCandidates(t, Candidates(in), seed)
	if got := ExplainCandidate(in, item); got.IsCandidate || len(got.Reasons) != 1 || got.Reasons[0] != ReasonWorkItemBead {
		t.Errorf("explain = %+v, want work-item-bead", got)
	}
}

func TestHiddenEntityIsNotACandidate(t *testing.T) {
	f := newFixture(t)
	pr := f.pr("7", prSpec{ownership: "mine"})
	bead := f.labelledBead("bd-1")
	jira := f.issue("PROJ-1", issueSpec{state: "To Do", assignee: "Pat Example"})
	visible := f.pr("8", prSpec{ownership: "mine"})
	f.hide(pr)
	f.hide(bead)
	f.hide(jira)
	in := f.inputs()
	wantCandidates(t, Candidates(in), visible)
	if got := ExplainCandidate(in, pr); got.IsCandidate || got.Reasons[0] != ReasonHidden {
		t.Errorf("explain = %+v, want hidden", got)
	}
}

// A hidden annotation whose value is false hides nothing.
func TestHiddenFalseHidesNothing(t *testing.T) {
	f := newFixture(t)
	k := f.pr("7", prSpec{ownership: "mine"})
	if err := f.st.SetAnnotation(store.KVAnnotation{
		Repo: testRepo, EntityType: "pr", EntityID: "7", Key: store.AnnotationHidden,
		Value: `{"value":false,"reason":"unhidden"}`, Origin: "pg-desk", SetBy: "operator", SetAt: fixtureAt,
	}); err != nil {
		t.Fatal(err)
	}
	wantCandidates(t, f.set(), k)
}

// A wip-marked entity stays a candidate.
func TestWIPEntityStaysACandidate(t *testing.T) {
	f := newFixture(t)
	k := f.pr("7", prSpec{ownership: "mine"})
	if err := f.st.SetAnnotation(store.KVAnnotation{
		Repo: testRepo, EntityType: "pr", EntityID: "7", Key: store.AnnotationWIP,
		Value: "true", Origin: "pg-desk", SetBy: "operator", SetAt: fixtureAt,
	}); err != nil {
		t.Fatal(err)
	}
	wantCandidates(t, f.set(), k)
}

func TestInactiveEntitiesExcluded(t *testing.T) {
	f := newFixture(t)
	f.pr("7", prSpec{ownership: "mine", inactive: true})
	f.issue("PROJ-1", issueSpec{state: "To Do", assignee: "Pat Example", inactive: true})
	bead := f.issue("bd-1", issueSpec{state: "open", labels: []string{PlanableLabel}, inactive: true})
	in := f.inputs()
	wantNone(t, Candidates(in))
	if got := ExplainCandidate(in, bead); got.IsCandidate || got.Reasons[0] != ReasonInactive {
		t.Errorf("explain = %+v, want inactive", got)
	}
}

func TestEntitiesOfOtherTypesAreIgnored(t *testing.T) {
	f := newFixture(t)
	f.writeEntity(Key{"thread", "T1"}, `{"thread_show":{}}`, true)
	wantNone(t, f.set())
}

// ---- links (RV-D) ----

// linkRelations are the relations that yield a candidate, with the entity
// types that can carry them.
func TestLinkedEntityIsACandidateOneHop(t *testing.T) {
	prSeed := func(f *fixture) Key { return f.pr("7", prSpec{ownership: "mine"}) }
	tests := []struct {
		name     string
		relation string
		external bool
		// build returns the seed, the other entity and the link's direction.
		build func(f *fixture) (seed, other Key, seedIsFrom bool)
	}{
		{name: "parent: seed child to epic", relation: relationParent, build: func(f *fixture) (Key, Key, bool) {
			return f.labelledBead("bd-child"), f.issue("bd-epic", issueSpec{state: "open", issueType: "epic"}), true
		}},
		{name: "parent: seed epic to its child", relation: relationParent, build: func(f *fixture) (Key, Key, bool) {
			return f.issue("bd-epic", issueSpec{state: "open", issueType: "epic", labels: []string{PlanableLabel}}), f.issue("bd-child", issueSpec{state: "open"}), false
		}},
		{name: "mentions: a seed PR mentions an issue", relation: relationMentions, build: func(f *fixture) (Key, Key, bool) {
			return prSeed(f), f.issue("PROJ-9", issueSpec{state: "To Do"}), true
		}},
		{name: "mentions: an issue names a seed PR", relation: relationMentions, build: func(f *fixture) (Key, Key, bool) {
			return prSeed(f), f.issue("PROJ-9", issueSpec{state: "To Do"}), false
		}},
		{name: "work: a PR and its work bead", relation: relationWork, build: func(f *fixture) (Key, Key, bool) {
			return prSeed(f), f.issue("bd-work", issueSpec{state: "open"}), true
		}},
		{name: "work: the bead seeds its PR", relation: relationWork, build: func(f *fixture) (Key, Key, bool) {
			return f.labelledBead("bd-1"), f.pr("7", prSpec{}), false
		}},
		{name: "jira: a seed PR names a ticket", relation: relationJira, build: func(f *fixture) (Key, Key, bool) {
			return prSeed(f), f.issue("PROJ-9", issueSpec{state: "To Do"}), true
		}},
		{name: "jira: the ticket seeds the PR that names it", relation: relationJira, build: func(f *fixture) (Key, Key, bool) {
			return f.issue("PROJ-9", issueSpec{state: "To Do", assignee: "Pat Example"}), f.pr("7", prSpec{}), false
		}},
		{name: "depends_on: a seed PR depends on another PR", relation: relationDependsOn, build: func(f *fixture) (Key, Key, bool) {
			return prSeed(f), f.pr("8", prSpec{}), true
		}},
		{name: "depends_on: a PR that depends on a seed PR", relation: relationDependsOn, build: func(f *fixture) (Key, Key, bool) {
			return prSeed(f), f.pr("8", prSpec{}), false
		}},
		{name: "external references: seed to issue", relation: relationReferences, external: true, build: func(f *fixture) (Key, Key, bool) {
			return prSeed(f), f.issue("PROJ-9", issueSpec{state: "To Do"}), true
		}},
		{name: "external references: issue to seed", relation: relationReferences, external: true, build: func(f *fixture) (Key, Key, bool) {
			return prSeed(f), f.issue("PROJ-9", issueSpec{state: "To Do"}), false
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			seed, other, seedIsFrom := tc.build(f)
			from, to := other, seed
			if seedIsFrom {
				from, to = seed, other
			}
			if tc.external {
				f.external(from, to, tc.relation, "")
			} else {
				f.link(from, to, tc.relation)
			}
			s := f.set()
			wantCandidates(t, s, seed, other)
			c := byKey(s)[other]
			if c.Seed || c.Via == nil || c.Via.SeedKey != seed || c.Via.Relation != tc.relation || c.Via.More != 0 {
				t.Errorf("linked candidate = %+v (via %+v), want a linked-only candidate via %v/%s", c, c.Via, seed, tc.relation)
			}
			if s.Counts.Linked != 1 {
				t.Errorf("Linked = %d, want 1", s.Counts.Linked)
			}
		})
	}
}

func TestDependsOnYieldsOnlyWhenItLeavesAPR(t *testing.T) {
	f := newFixture(t)
	bead := f.labelledBead("bd-1")
	other := f.issue("bd-2", issueSpec{state: "open"})
	pr := f.pr("7", prSpec{ownership: "mine"})
	f.link(bead, other, relationDependsOn) // leaves an issue: nothing
	f.link(other, pr, relationDependsOn)   // leaves an issue, arrives at a seed PR: nothing
	s := f.set()
	wantCandidates(t, s, bead, pr)
}

func TestLegacyReferencesLinkYieldsNothing(t *testing.T) {
	in := Inputs{
		Repo: testRepo, Now: fixtureNow, Config: fixtureConfig(),
		Entities: map[Key]store.Entity{
			{"pr", "7"}:         {Repo: testRepo, EntityType: "pr", EntityID: "7", Facts: `{"pr_show":{"state":"open"}}`},
			{"issue", "PROJ-1"}: {Repo: testRepo, EntityType: "issue", EntityID: "PROJ-1", Facts: `{"issue_show":{"state":"To Do"}}`},
		},
		Interps: map[Key]store.Interpretation{{"pr", "7"}: {Ownership: "mine"}},
		Links: []store.XrefLink{{
			Repo: testRepo, FromType: "pr", FromID: "7", ToType: "issue", ToID: "PROJ-1",
			Relation: relationReferences, Origin: "derived:legacy",
		}},
	}
	wantCandidates(t, Candidates(in), Key{"pr", "7"})
	// The same link under an external origin yields.
	in.Links[0].Origin = "external:operator"
	wantCandidates(t, Candidates(in), Key{"pr", "7"}, Key{"issue", "PROJ-1"})
}

func TestLinkCandidacyIsNotTransitive(t *testing.T) {
	f := newFixture(t)
	seed := f.labelledBead("bd-seed")
	hop1 := f.issue("bd-hop1", issueSpec{state: "open"})
	hop2 := f.issue("bd-hop2", issueSpec{state: "open"})
	f.link(seed, hop1, relationParent)
	f.link(hop1, hop2, relationParent)
	in := f.inputs()
	wantCandidates(t, Candidates(in), seed, hop1)
	if got := ExplainCandidate(in, hop2); got.IsCandidate || got.Reasons[0] != ReasonNotSeedOrLinked {
		t.Errorf("explain(hop2) = %+v, want not-seed-or-linked", got)
	}
}

func TestThreadAndBuildLinksYieldNoCandidates(t *testing.T) {
	f := newFixture(t)
	seed := f.pr("7", prSpec{ownership: "mine"})
	thread := Key{"thread", "T1"}
	f.writeEntity(thread, `{"thread_show":{}}`, true)
	build := Key{"build", "run-1"}
	f.link(thread, seed, relationMentions)
	f.link(seed, build, "ci")
	f.link(seed, Key{"other", "x"}, relationMentions)
	s := f.set()
	wantCandidates(t, s, seed)
}

func TestCiAndSelfLinksYieldNothing(t *testing.T) {
	f := newFixture(t)
	seed := f.pr("7", prSpec{ownership: "mine"})
	other := f.issue("PROJ-9", issueSpec{state: "To Do"})
	f.link(seed, other, "ci")
	f.link(seed, other, "self")
	f.link(seed, seed, "self")
	wantCandidates(t, f.set(), seed)
}

func TestLinkedCandidateExclusionsStillApply(t *testing.T) {
	f := newFixture(t)
	seed := f.pr("7", prSpec{ownership: "mine"})
	cases := map[string]Key{
		ReasonMintedBead:   f.issue("bd-minted", issueSpec{state: "open", metadata: map[string]string{MetaSourceID: "pr:7"}}),
		ReasonWorkItemBead: f.issue("bd-item", issueSpec{state: "open", metadata: map[string]string{MetaDedupKey: "k"}}),
		ReasonHidden:       f.issue("PROJ-hidden", issueSpec{state: "To Do"}),
		ReasonDeferred:     f.issue("bd-parked", issueSpec{state: "deferred"}),
		ReasonInactive:     f.issue("PROJ-gone", issueSpec{state: "To Do", inactive: true}),
		ReasonTerminal:     f.issue("PROJ-done", issueSpec{state: "Done", category: "done"}),
	}
	cases[ReasonTerminal+"-pr"] = f.pr("9", prSpec{state: "merged"})
	f.hide(cases[ReasonHidden])
	for _, k := range cases {
		f.link(seed, k, relationMentions)
	}
	in := f.inputs()
	wantCandidates(t, Candidates(in), seed)
	for reason, k := range cases {
		want := reason
		if reason == ReasonTerminal+"-pr" {
			want = ReasonTerminal
		}
		got := ExplainCandidate(in, k)
		if got.IsCandidate || len(got.Reasons) == 0 || got.Reasons[0] != want {
			t.Errorf("explain(%v) = %+v, want reason %s", k, got, want)
		}
	}
}

func TestHiddenOrExcludedSeedSeedsNothing(t *testing.T) {
	f := newFixture(t)
	hiddenSeed := f.pr("7", prSpec{ownership: "mine"})
	mintedSeed := f.issue("bd-minted", issueSpec{state: "open", labels: []string{PlanableLabel}, metadata: map[string]string{MetaSourceID: "x"}})
	parkedSeed := f.issue("bd-parked", issueSpec{state: "deferred", labels: []string{PlanableLabel}})
	a := f.issue("PROJ-a", issueSpec{state: "To Do"})
	b := f.issue("PROJ-b", issueSpec{state: "To Do"})
	c := f.issue("PROJ-c", issueSpec{state: "To Do"})
	f.hide(hiddenSeed)
	f.link(hiddenSeed, a, relationMentions)
	f.link(mintedSeed, b, relationParent)
	f.link(c, parkedSeed, relationParent)
	wantNone(t, f.set())
}

func TestLinkToEntityNotInStoreYieldsNothing(t *testing.T) {
	f := newFixture(t)
	seed := f.pr("7", prSpec{ownership: "mine"})
	f.link(seed, Key{"issue", "PROJ-never-gathered"}, relationJira)
	wantCandidates(t, f.set(), seed)
}

func TestLinkedCandidateShowsVia(t *testing.T) {
	f := newFixture(t)
	pr := f.pr("7", prSpec{ownership: "mine"})
	jira := f.issue("PROJ-1", issueSpec{state: "To Do", assignee: "Pat Example"})
	bead := f.labelledBead("bd-1")
	shared := f.issue("PROJ-9", issueSpec{state: "To Do"})
	// Three seeds join the shared issue; the first in (kind, key) order is the
	// PR, so it is the via and two further seeds are counted.
	f.link(bead, shared, relationParent)
	f.link(shared, jira, relationParent)
	f.link(pr, shared, relationMentions)
	f.link(pr, shared, relationJira) // two relations between the same pair: the smaller name wins
	in := f.inputs()
	s := Candidates(in)
	got := byKey(s)[shared]
	want := Via{SeedKey: pr, Relation: relationJira, More: 2}
	if got.Seed || got.Via == nil || *got.Via != want {
		t.Fatalf("via = %+v, want %+v", got.Via, want)
	}
	if ex := ExplainCandidate(in, shared); !ex.IsCandidate || ex.Seed || ex.Via == nil || *ex.Via != want {
		t.Errorf("explain = %+v, want the same via", ex)
	}
}

func TestSeedAndLinkedItemAppearsOnce(t *testing.T) {
	f := newFixture(t)
	a := f.labelledBead("bd-a")
	b := f.labelledBead("bd-b")
	f.link(a, b, relationParent)
	f.link(b, a, relationMentions)
	s := f.set()
	wantCandidates(t, s, a, b)
	for _, k := range []Key{a, b} {
		if c := byKey(s)[k]; !c.Seed || c.Via != nil {
			t.Errorf("%v = %+v, want a seed with no via", k, c)
		}
	}
	if s.Counts.Linked != 0 || s.Counts.Bead != 2 {
		t.Errorf("counts = %+v, want Bead 2 and Linked 0", s.Counts)
	}
}

func TestMergedAbsorbedKeyIsNotReadmittedByLinks(t *testing.T) {
	f := newFixture(t)
	kept := f.pr("7", prSpec{ownership: "mine"})
	absorbed := f.pr("8", prSpec{ownership: "mine"}) // would be a seed on its own
	linked := f.issue("PROJ-1", issueSpec{state: "To Do"})
	f.external(kept, absorbed, relationReferences, MergeReason)
	f.link(kept, absorbed, relationDependsOn)
	f.link(absorbed, linked, relationMentions)
	in := f.inputs()
	if in.Absorbed[absorbed] != kept {
		t.Fatalf("Absorbed = %v, want %v -> %v", in.Absorbed, absorbed, kept)
	}
	s := Candidates(in)
	wantCandidates(t, s, kept)
	if got := ExplainCandidate(in, absorbed); got.IsCandidate || got.Reasons[0] != ReasonAbsorbed {
		t.Errorf("explain(absorbed) = %+v, want absorbed", got)
	}
}

// A references link of another reason is a plain link, not a merge.
func TestExternalReferencesWithoutTheMergeReasonIsNotAMerge(t *testing.T) {
	f := newFixture(t)
	a := f.pr("7", prSpec{ownership: "mine"})
	b := f.pr("8", prSpec{})
	f.external(a, b, relationReferences, "some other reason")
	in := f.inputs()
	if len(in.Absorbed) != 0 {
		t.Fatalf("Absorbed = %v, want none", in.Absorbed)
	}
	wantCandidates(t, Candidates(in), a, b)
}

// ---- epics (RV-D) ----

func TestEpicIsACandidateWhenLabelled(t *testing.T) {
	f := newFixture(t)
	k := f.issue("bd-epic", issueSpec{state: "open", issueType: "epic", labels: []string{PlanableLabel}})
	wantCandidates(t, f.set(), k)
}

func TestEpicIsACandidateWhenLinkedToASeed(t *testing.T) {
	f := newFixture(t)
	epic := f.issue("bd-epic", issueSpec{state: "open", issueType: "epic"})
	child := f.labelledBead("bd-child")
	f.link(child, epic, relationParent)
	s := f.set()
	wantCandidates(t, s, epic, child)
	if c := byKey(s)[epic]; c.Seed || c.Via == nil || c.Via.SeedKey != child || c.Via.Relation != relationParent {
		t.Errorf("epic = %+v (via %+v), want a linked candidate via the child", c, c.Via)
	}
}

func TestLabellingAnEpicMakesItsDirectChildrenCandidates(t *testing.T) {
	f := newFixture(t)
	epic := f.issue("bd-epic", issueSpec{state: "open", issueType: "epic", labels: []string{PlanableLabel}})
	c1 := f.issue("bd-c1", issueSpec{state: "open"})
	c2 := f.issue("bd-c2", issueSpec{state: "blocked"})
	grandchild := f.issue("bd-gc", issueSpec{state: "open"})
	f.link(c1, epic, relationParent)
	f.link(c2, epic, relationParent)
	f.link(grandchild, c1, relationParent)
	wantCandidates(t, f.set(), epic, c1, c2)
}

func TestUnlabelledUnlinkedEpicIsNotACandidate(t *testing.T) {
	f := newFixture(t)
	f.issue("bd-epic", issueSpec{state: "open", issueType: "epic"})
	wantNone(t, f.set())
}

// ---- counts, notices, order ----

func TestCountsAndNotices(t *testing.T) {
	f := newFixture(t)
	f.pr("7", prSpec{ownership: "mine"})
	f.pr("8", prSpec{requests: []string{fixtureSelf}})
	jira := f.issue("PROJ-1", issueSpec{state: "To Do", assignee: "Pat Example"})
	f.labelledBead("bd-1")
	f.issue("bd-2", issueSpec{state: "open"})
	f.issue("bd-3", issueSpec{state: "blocked"})
	f.issue("bd-4", issueSpec{state: "deferred"})
	linkedPR := f.pr("9", prSpec{})
	f.link(jira, linkedPR, relationWork)
	s := f.set()
	want := SourceCounts{PR: 3, Jira: 1, Bead: 1, Linked: 1, OpenBeads: 3, UnlabelledOpenBeads: 2}
	if s.Counts != want {
		t.Errorf("counts = %+v, want %+v", s.Counts, want)
	}
	if len(s.Notices) != 0 {
		t.Errorf("notices = %v, want none", s.Notices)
	}
}

func TestEmptyIdentityListIsANotice(t *testing.T) {
	f := newFixture(t)
	f.cfg.Focus.OperatorIdentities = nil
	f.pr("7", prSpec{ownership: "mine"})
	f.labelledBead("bd-1")
	s := f.set()
	if !hasNotice(s, NoticeEmptyIdentityList) || len(s.Notices) != 1 {
		t.Errorf("notices = %v, want exactly the empty-identity-list notice", s.Notices)
	}
	if len(s.Candidates) != 2 {
		t.Errorf("candidates = %v, want the PR and the bead (the other sources are unaffected)", s.Candidates)
	}
}

func TestNoBeadCandidatesNoticeNamesTheLabel(t *testing.T) {
	f := newFixture(t)
	f.issue("bd-1", issueSpec{state: "open"})
	f.issue("bd-2", issueSpec{state: "in_progress"})
	s := f.set()
	if !hasNotice(s, NoticeNoBeadCandidates) {
		t.Fatalf("notices = %v, want no-bead-candidates", s.Notices)
	}
	for _, n := range s.Notices {
		if n.Code == NoticeNoBeadCandidates && !strings.Contains(n.Text, PlanableLabel) {
			t.Errorf("notice text %q does not name the label", n.Text)
		}
	}
	if s.Counts.OpenBeads != 2 || s.Counts.UnlabelledOpenBeads != 2 || s.Counts.Bead != 0 {
		t.Errorf("counts = %+v, want 2 open, 2 unlabelled, 0 candidates", s.Counts)
	}
}

func TestNoBeadPatternMeansNoBeadSeeds(t *testing.T) {
	f := newFixture(t)
	f.cfg.BeadIDPattern = ""
	// Without a pattern no issue is a bead: a labelled one is not a seed.
	f.issue("bd-1", issueSpec{state: "open", labels: []string{PlanableLabel}})
	s := f.set()
	wantNone(t, s)
	if !hasNotice(s, NoticeNoBeadPattern) || hasNotice(s, NoticeNoBeadCandidates) {
		t.Errorf("notices = %v, want no-bead-pattern only", s.Notices)
	}
}

func TestBeadKindFollowsThePattern(t *testing.T) {
	f := newFixture(t)
	jira := f.issue("PROJ-1", issueSpec{state: "To Do", assignee: "Pat Example"})
	bead := f.labelledBead("bd-1")
	// A label on a non-bead issue does not seed it.
	f.issue("PROJ-2", issueSpec{state: "To Do", labels: []string{PlanableLabel}})
	s := f.set()
	wantCandidates(t, s, jira, bead)
	if got := byKey(s); got[jira].Kind != KindJira || got[bead].Kind != KindBead {
		t.Errorf("kinds = %v / %v, want jira / bead", got[jira].Kind, got[bead].Kind)
	}
}

func TestCandidateOrderIsByKindThenKey(t *testing.T) {
	f := newFixture(t)
	f.labelledBead("bd-2")
	f.labelledBead("bd-1")
	f.issue("PROJ-2", issueSpec{state: "To Do", assignee: "Pat Example"})
	f.issue("PROJ-1", issueSpec{state: "To Do", assignee: "Pat Example"})
	f.pr("10", prSpec{ownership: "mine"})
	f.pr("9", prSpec{ownership: "mine"})
	s := f.set()
	want := []Key{{"pr", "10"}, {"pr", "9"}, {"issue", "PROJ-1"}, {"issue", "PROJ-2"}, {"issue", "bd-1"}, {"issue", "bd-2"}}
	if len(s.Candidates) != len(want) {
		t.Fatalf("candidates = %v", s.Candidates)
	}
	for i, k := range want {
		if s.Candidates[i].Key != k {
			t.Errorf("candidate %d = %v, want %v (kind pr, jira, bead; then key)", i, s.Candidates[i].Key, k)
		}
	}
}

// Shuffled store insertion order yields an identical candidate set,
// including the via of every linked candidate.
func TestCandidateSetIsDeterministicUnderInsertionOrder(t *testing.T) {
	build := func(order []int) CandidateSet {
		f := newFixture(t)
		steps := []func(){
			func() { f.pr("7", prSpec{ownership: "mine"}) },
			func() { f.pr("8", prSpec{requests: []string{fixtureSelf}}) },
			func() { f.issue("PROJ-1", issueSpec{state: "To Do", assignee: "Pat Example"}) },
			func() { f.labelledBead("bd-1") },
			func() { f.issue("bd-epic", issueSpec{state: "open", issueType: "epic"}) },
			func() { f.issue("PROJ-9", issueSpec{state: "To Do"}) },
		}
		for _, i := range order {
			steps[i]()
		}
		f.link(Key{"issue", "bd-1"}, Key{"issue", "bd-epic"}, relationParent)
		f.link(Key{"pr", "7"}, Key{"issue", "PROJ-9"}, relationMentions)
		f.link(Key{"pr", "8"}, Key{"issue", "PROJ-9"}, relationJira)
		f.link(Key{"issue", "PROJ-1"}, Key{"issue", "PROJ-9"}, relationParent)
		return f.set()
	}
	base := build([]int{0, 1, 2, 3, 4, 5})
	if len(base.Candidates) != 6 {
		t.Fatalf("base = %v", base.Candidates)
	}
	for _, order := range [][]int{{5, 4, 3, 2, 1, 0}, {2, 5, 0, 4, 1, 3}, {3, 0, 5, 1, 4, 2}} {
		got := build(order)
		if len(got.Candidates) != len(base.Candidates) {
			t.Fatalf("order %v: %d candidates, want %d", order, len(got.Candidates), len(base.Candidates))
		}
		for i := range base.Candidates {
			a, b := base.Candidates[i], got.Candidates[i]
			if a.Key != b.Key || a.Kind != b.Kind || a.Seed != b.Seed || (a.Via == nil) != (b.Via == nil) || (a.Via != nil && *a.Via != *b.Via) {
				t.Errorf("order %v: candidate %d = %+v, want %+v", order, i, b, a)
			}
		}
		if got.Counts != base.Counts {
			t.Errorf("order %v: counts = %+v, want %+v", order, got.Counts, base.Counts)
		}
	}
}

func TestExplainCandidateReasons(t *testing.T) {
	f := newFixture(t)
	f.pr("7", prSpec{})
	f.pr("8", prSpec{ownership: "mine"})
	f.pr("9", prSpec{state: "closed", ownership: "mine", inactive: true})
	in := f.inputs()

	if got := ExplainCandidate(in, Key{"pr", "missing"}); got.IsCandidate || len(got.Reasons) != 1 || got.Reasons[0] != ReasonNotInStore {
		t.Errorf("missing = %+v, want not-in-store", got)
	}
	if got := ExplainCandidate(in, Key{"pr", "7"}); got.IsCandidate || len(got.Reasons) != 1 || got.Reasons[0] != ReasonNotSeedOrLinked {
		t.Errorf("neither = %+v, want not-seed-or-linked", got)
	}
	if got := ExplainCandidate(in, Key{"pr", "8"}); !got.IsCandidate || !got.Seed || got.Via != nil || len(got.Reasons) != 0 {
		t.Errorf("seed = %+v, want a seed", got)
	}
	// Every exclusion that applies is listed, in the documented order.
	got := ExplainCandidate(in, Key{"pr", "9"})
	if got.IsCandidate || len(got.Reasons) != 2 || got.Reasons[0] != ReasonInactive || got.Reasons[1] != ReasonTerminal {
		t.Errorf("inactive and closed = %+v, want [inactive terminal]", got)
	}
}

// Explain and the set agree on every stored entity of a mixed store.
func TestExplainCandidateAgreesWithCandidates(t *testing.T) {
	f := newFixture(t)
	f.pr("7", prSpec{ownership: "mine"})
	f.pr("8", prSpec{})
	f.issue("PROJ-1", issueSpec{state: "To Do", assignee: "Pat Example"})
	f.issue("PROJ-2", issueSpec{state: "To Do"})
	f.labelledBead("bd-1")
	f.issue("bd-2", issueSpec{state: "deferred", labels: []string{PlanableLabel}})
	f.link(Key{"pr", "7"}, Key{"pr", "8"}, relationDependsOn)
	in := f.inputs()
	set := byKey(Candidates(in))
	for k := range in.Entities {
		ex := ExplainCandidate(in, k)
		_, isCand := set[k]
		if ex.IsCandidate != isCand {
			t.Errorf("%v: explain says candidate=%v, set says %v", k, ex.IsCandidate, isCand)
		}
		if ex.IsCandidate == (len(ex.Reasons) > 0) {
			t.Errorf("%v: candidate=%v with reasons %v", k, ex.IsCandidate, ex.Reasons)
		}
	}
}
