package attention

import (
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/interpret"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// stackPR is an own PR whose CI is failing, so it raises pr.own-ci-failing, a
// self-broken candidate.
func stackPR(n int, branch, base, state string) prSpec {
	return prSpec{
		number: n, ownership: "mine", panel: interpret.PanelMineAwaitingMe,
		ci: "failure", state: state, branch: branch, base: base,
	}
}

// seedBrokenStack stores a three-PR stack, every PR failing CI: #1 targets
// main, #2 targets #1's branch, #3 targets #2's branch. states[i] is the
// stored state of PR i+1.
func seedBrokenStack(t *testing.T, st *store.Store, states [3]string) {
	t.Helper()
	put(t, st, stackPR(1, "feat-a", "main", states[0]))
	put(t, st, stackPR(2, "feat-b", "feat-a", states[1]))
	put(t, st, stackPR(3, "feat-c", "feat-b", states[2]))
}

func assertListed(t *testing.T, res Result, want map[int]bool) {
	t.Helper()
	for n, w := range want {
		if _, got := itemFor(res, n); got != w {
			t.Errorf("PR %d listed = %v, want %v; items: %+v", n, got, w, res.Items)
		}
	}
}

func assertBlockedBy(t *testing.T, res Result, n int) {
	t.Helper()
	tr := res.Traces[Ref("pr", pid(n))]
	if len(tr.Dropped) != 1 || tr.Dropped[0].By != NameBlockedByOpenDependency || tr.Dropped[0].Candidate.Kind != KindOwnCIFailing {
		t.Errorf("PR %d dropped = %+v, want pr.own-ci-failing dropped by %s", n, tr.Dropped, NameBlockedByOpenDependency)
	}
}

// The bead's acceptance: in a three-PR stack only the bottom PR is actionable;
// each PR above it fires once the PR below it has merged.
func TestDependencySuppression_ThreePRStack(t *testing.T) {
	// All three open: only the base of the stack is listed.
	st := store.OpenNewSchemaForTest(t)
	seedBrokenStack(t, st, [3]string{"open", "open", "open"})
	res := evaluate(t, st, baseConfig())
	assertListed(t, res, map[int]bool{1: true, 2: false, 3: false})
	assertBlockedBy(t, res, 2)
	assertBlockedBy(t, res, 3)

	// #1 merges: #2 fires on the very next read; #3 still waits on #2.
	st = store.OpenNewSchemaForTest(t)
	seedBrokenStack(t, st, [3]string{"merged", "open", "open"})
	res = evaluate(t, st, baseConfig())
	assertListed(t, res, map[int]bool{1: false, 2: true, 3: false})
	assertBlockedBy(t, res, 3)

	// #1 and #2 merged: #3 fires.
	st = store.OpenNewSchemaForTest(t)
	seedBrokenStack(t, st, [3]string{"merged", "merged", "open"})
	res = evaluate(t, st, baseConfig())
	assertListed(t, res, map[int]bool{1: false, 2: false, 3: true})
	if d := res.Traces[Ref("pr", pid(3))].Dropped; len(d) != 0 {
		t.Errorf("PR 3 dropped = %+v, want nothing once the last dependency merged", d)
	}
}

// The same transition on ONE store: re-storing the base PR as merged is all it
// takes, because the dependencies are derived at every read.
func TestDependencySuppression_FiresOnNextReadAfterLastDependencyMerges(t *testing.T) {
	st := store.OpenNewSchemaForTest(t)
	put(t, st, stackPR(1, "feat-a", "main", "open"))
	put(t, st, stackPR(2, "feat-b", "feat-a", "open"))
	assertListed(t, evaluate(t, st, baseConfig()), map[int]bool{1: true, 2: false})

	put(t, st, stackPR(1, "feat-a", "main", "merged"))
	assertListed(t, evaluate(t, st, baseConfig()), map[int]bool{1: false, 2: true})
}

// A closed (unmerged) dependency no longer holds anything back either.
func TestDependencySuppression_ClosedDependencyReleases(t *testing.T) {
	st := store.OpenNewSchemaForTest(t)
	put(t, st, stackPR(1, "feat-a", "main", "closed"))
	put(t, st, stackPR(2, "feat-b", "feat-a", "open"))
	assertListed(t, evaluate(t, st, baseConfig()), map[int]bool{2: true})
}

// The operator's example: a broken PR that depends on two sibling PRs (an
// external depends_on link each) fires only after the LAST one merges.
func TestDependencySuppression_TwoSiblingDependencies(t *testing.T) {
	link := func(st *store.Store, from, to int) {
		t.Helper()
		err := st.AddExternalXref(store.XrefLink{
			Repo: testRepo, FromType: "pr", FromID: pid(from), ToType: "pr", ToID: pid(to),
			Relation: "depends_on", Actor: "op", ActedAt: "2026-10-06T10:00:00Z",
			FirstSeen: "2026-10-06T10:00:00Z", LastConfirmed: "2026-10-06T10:00:00Z",
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	seed := func(s1, s2 string) *store.Store {
		st := store.OpenNewSchemaForTest(t)
		put(t, st, prSpec{number: 1, ownership: "team", state: s1, branch: "sib-a", base: "main"})
		put(t, st, prSpec{number: 2, ownership: "team", state: s2, branch: "sib-b", base: "main"})
		put(t, st, stackPR(3, "feat-c", "main", "open"))
		link(st, 3, 1)
		link(st, 3, 2)
		return st
	}
	assertListed(t, evaluate(t, seed("open", "open"), baseConfig()), map[int]bool{3: false})
	assertListed(t, evaluate(t, seed("merged", "open"), baseConfig()), map[int]bool{3: false})
	assertListed(t, evaluate(t, seed("open", "merged"), baseConfig()), map[int]bool{3: false})
	assertListed(t, evaluate(t, seed("merged", "merged"), baseConfig()), map[int]bool{3: true})
}

// Only a self-broken candidate is claimed: a candidate that waits on someone
// else's action is listed even while a dependency is open.
func TestDependencySuppression_OnlyClaimsSelfBrokenCandidates(t *testing.T) {
	st := store.OpenNewSchemaForTest(t)
	put(t, st, stackPR(1, "feat-a", "main", "open"))
	// #2 sits on #1 and has changes requested (not broken): listed.
	put(t, st, prSpec{
		number: 2, ownership: "mine", panel: interpret.PanelMineAwaitingMe, ci: "success",
		approvals: interpret.Approvals{HumanChangesRequested: true}, branch: "feat-b", base: "feat-a",
	})
	// #3 sits on #1 and is a review request of me (a team PR): listed.
	put(t, st, prSpec{number: 3, ownership: "team", panel: interpret.PanelTeamAwaitingMe, branch: "feat-c", base: "feat-a"})
	// #4 sits on #1 and is approved and ready to land: listed.
	put(t, st, prSpec{
		number: 4, ownership: "mine", panel: interpret.PanelMineAwaitingMe, ci: "success",
		approvals: interpret.Approvals{HumanApproved: true}, branch: "feat-d", base: "feat-a",
	})
	res := evaluate(t, st, baseConfig())
	assertListed(t, res, map[int]bool{1: true, 2: true, 3: true, 4: true})
}

// A merge conflict is the entity itself being broken, so a conflict-only
// pr.own-needs-action candidate is claimed; one that also carries review
// feedback is not.
func TestDependencySuppression_ConflictIsSelfBroken(t *testing.T) {
	st := store.OpenNewSchemaForTest(t)
	put(t, st, stackPR(1, "feat-a", "main", "open"))
	put(t, st, prSpec{number: 2, ownership: "mine", panel: interpret.PanelMineAwaitingMe, ci: "success", conflict: true, branch: "feat-b", base: "feat-a"})
	put(t, st, prSpec{
		number: 3, ownership: "mine", panel: interpret.PanelMineAwaitingMe, ci: "success", conflict: true,
		approvals: interpret.Approvals{HumanChangesRequested: true}, branch: "feat-c", base: "feat-a",
	})
	res := evaluate(t, st, baseConfig())
	assertListed(t, res, map[int]bool{1: true, 2: false, 3: true})
	if d := res.Traces[Ref("pr", pid(2))].Dropped; len(d) != 1 || d[0].By != NameBlockedByOpenDependency || d[0].Candidate.Kind != KindOwnNeedsAction {
		t.Errorf("PR 2 dropped = %+v, want pr.own-needs-action dropped by %s", d, NameBlockedByOpenDependency)
	}
}

// Stack suppression needs no migration (the stack source reads entity rows), so
// it runs on the version 1 store too. A PR with no stored dependencies is
// unaffected.
func TestDependencySuppression_OldSchemaStore(t *testing.T) {
	st := store.OpenForTest(t)
	put(t, st, stackPR(1, "feat-a", "main", "open"))
	put(t, st, stackPR(2, "feat-b", "feat-a", "open"))
	put(t, st, stackPR(5, "solo", "main", "open"))
	res := evaluate(t, st, baseConfig())
	if !res.Degraded {
		t.Error("an unmigrated store must still report degraded")
	}
	assertListed(t, res, map[int]bool{1: true, 2: false, 5: true})
}

// An explicit suppress annotation and the hidden flag are checked BEFORE the
// context suppressor, so the recorded reason is the annotation's.
func TestDependencySuppression_AnnotationsComeFirst(t *testing.T) {
	st := store.OpenNewSchemaForTest(t)
	put(t, st, stackPR(1, "feat-a", "main", "open"))
	put(t, st, stackPR(2, "feat-b", "feat-a", "open"))
	if err := st.SetAnnotation(store.KVAnnotation{Repo: testRepo, EntityType: "pr", EntityID: pid(2), Key: store.KeySuppress(KindOwnCIFailing), Value: "true", Origin: "test", SetBy: "test", SetAt: "2026-10-06T10:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	res := evaluate(t, st, baseConfig())
	tr := res.Traces[Ref("pr", pid(2))]
	if len(tr.Dropped) != 1 || tr.Dropped[0].By != store.KeySuppress(KindOwnCIFailing) {
		t.Errorf("dropped = %+v, want the annotation named", tr.Dropped)
	}
}
