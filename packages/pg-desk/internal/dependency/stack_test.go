package dependency

import (
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

func stackRoot(t *testing.T, s *StackSource, id string) (string, bool) {
	t.Helper()
	root, ok, err := s.StackRoot("pr", id)
	if err != nil {
		t.Fatalf("StackRoot(%s): %v", id, err)
	}
	return root, ok
}

// A three-PR stack: every member, whichever end it is asked from, is named by
// the PR that targets the default branch.
func TestStackRoot_ThreePRStack(t *testing.T) {
	st := store.OpenNewSchemaForTest(t)
	seedStack(t, st)
	s := NewStackSource(st, testRepo)
	for n := 1; n <= 3; n++ {
		root, ok := stackRoot(t, s, prID(n))
		if !ok || root != prID(1) {
			t.Errorf("StackRoot(#%d) = %q, %v; want %q, true", n, root, ok, prID(1))
		}
	}
}

// Also on the unmigrated store: the stack source needs only entity rows.
func TestStackRoot_WorksOnUnmigratedStore(t *testing.T) {
	st := store.OpenForTest(t)
	seedStack(t, st)
	root, ok := stackRoot(t, NewStackSource(st, testRepo), prID(3))
	if !ok || root != prID(1) {
		t.Errorf("StackRoot(#3) = %q, %v; want %q, true", root, ok, prID(1))
	}
}

// A PR with no open dependency and no open dependent is in no stack.
func TestStackRoot_LonePRIsNotAStack(t *testing.T) {
	st := store.OpenNewSchemaForTest(t)
	putPR(t, st, prID(1), "open", "feat-a", "main", false)
	putPR(t, st, prID(2), "open", "feat-b", "main", false)
	s := NewStackSource(st, testRepo)
	for n := 1; n <= 2; n++ {
		if root, ok := stackRoot(t, s, prID(n)); ok {
			t.Errorf("StackRoot(#%d) = %q, true; want no stack", n, root)
		}
	}
}

// Only entity type pr has a stack.
func TestStackRoot_OtherEntityTypesHaveNone(t *testing.T) {
	st := store.OpenNewSchemaForTest(t)
	seedStack(t, st)
	root, ok, err := NewStackSource(st, testRepo).StackRoot("issue", prID(1))
	if err != nil || ok || root != "" {
		t.Errorf("StackRoot(issue) = %q, %v, %v; want none", root, ok, err)
	}
}

// Merging the base: the stack regroups on the next read, because the graph is
// derived from the stored rows each time. The merged base leaves; the rest
// are named by the new bottom.
func TestStackRoot_RegroupsWhenTheBaseMerges(t *testing.T) {
	st := store.OpenNewSchemaForTest(t)
	seedStack(t, st)
	putPR(t, st, prID(1), "closed", "feat-a", "main", true)
	s := NewStackSource(st, testRepo)
	if root, ok := stackRoot(t, s, prID(1)); ok {
		t.Errorf("merged base: StackRoot(#1) = %q, true; want none", root)
	}
	for n := 2; n <= 3; n++ {
		// #2's base branch is feat-a, whose PR is merged, so #2 now targets
		// a branch with no open PR: it is the bottom of the remaining stack.
		root, ok := stackRoot(t, s, prID(n))
		if !ok || root != prID(2) {
			t.Errorf("StackRoot(#%d) = %q, %v; want %q, true", n, root, ok, prID(2))
		}
	}
}

// A stack that has lost its last dependency in the middle splits into
// components: a merged middle PR joins nothing on either side of it.
func TestStackRoot_MergedMiddleSplitsTheStack(t *testing.T) {
	st := store.OpenNewSchemaForTest(t)
	seedStack(t, st)
	putPR(t, st, prID(4), "open", "feat-d", "feat-c", false)
	putPR(t, st, prID(2), "closed", "feat-b", "feat-a", true)
	s := NewStackSource(st, testRepo)
	// #1 lost its only dependent (#2 merged): a lone PR.
	if root, ok := stackRoot(t, s, prID(1)); ok {
		t.Errorf("StackRoot(#1) = %q, true; want none", root)
	}
	// #3 now targets feat-b, whose PR is merged: bottom of {#3, #4}.
	for _, n := range []int{3, 4} {
		root, ok := stackRoot(t, s, prID(n))
		if !ok || root != prID(3) {
			t.Errorf("StackRoot(#%d) = %q, %v; want %q, true", n, root, ok, prID(3))
		}
	}
}

// A PR that is not open (closed unmerged, or with no stored row) is in no stack.
func TestStackRoot_UnknownPRHasNone(t *testing.T) {
	st := store.OpenNewSchemaForTest(t)
	seedStack(t, st)
	if root, ok := stackRoot(t, NewStackSource(st, testRepo), "acme/api#99"); ok {
		t.Errorf("StackRoot(unknown) = %q, true; want none", root)
	}
}

// A tree (two PRs on one base) is one component; the root is the base.
func TestStackRoot_BranchingStack(t *testing.T) {
	st := store.OpenNewSchemaForTest(t)
	putPR(t, st, prID(1), "open", "feat-a", "main", false)
	putPR(t, st, prID(2), "open", "feat-b", "feat-a", false)
	putPR(t, st, prID(3), "open", "feat-c", "feat-a", false)
	s := NewStackSource(st, testRepo)
	for n := 1; n <= 3; n++ {
		if root, ok := stackRoot(t, s, prID(n)); !ok || root != prID(1) {
			t.Errorf("StackRoot(#%d) = %q, %v; want %q, true", n, root, ok, prID(1))
		}
	}
}

// An external depends_on link joins PRs that share no branch chain, and a
// component with several roots is named by the smallest id among them.
func TestStackRoot_ExternalLinkJoinsComponents(t *testing.T) {
	st := store.OpenNewSchemaForTest(t)
	putPR(t, st, prID(5), "open", "feat-e", "main", false)
	putPR(t, st, prID(2), "open", "feat-b", "main", false)
	putPR(t, st, prID(7), "open", "feat-g", "main", false)
	addDependsOn(t, st, prID(7), prID(5), "alice")
	addDependsOn(t, st, prID(7), prID(2), "alice")
	s := NewStackSource(st, testRepo)
	for _, n := range []int{2, 5, 7} {
		if root, ok := stackRoot(t, s, prID(n)); !ok || root != prID(2) {
			t.Errorf("StackRoot(#%d) = %q, %v; want %q, true", n, root, ok, prID(2))
		}
	}
}

// A dependency cycle still terminates and is named by its smallest id.
func TestStackRoot_CycleTerminates(t *testing.T) {
	st := store.OpenNewSchemaForTest(t)
	putPR(t, st, prID(4), "open", "feat-d", "main", false)
	putPR(t, st, prID(3), "open", "feat-c", "main", false)
	addDependsOn(t, st, prID(4), prID(3), "alice")
	addDependsOn(t, st, prID(3), prID(4), "alice")
	s := NewStackSource(st, testRepo)
	for _, n := range []int{3, 4} {
		if root, ok := stackRoot(t, s, prID(n)); !ok || root != prID(3) {
			t.Errorf("StackRoot(#%d) = %q, %v; want %q, true", n, root, ok, prID(3))
		}
	}
}

// The same root for every member, asked in any order, and a repeated question
// answers the same (the source memoizes within one evaluation).
func TestStackRoot_StableAcrossCalls(t *testing.T) {
	st := store.OpenNewSchemaForTest(t)
	seedStack(t, st)
	s := NewStackSource(st, testRepo)
	for _, n := range []int{3, 1, 2, 3} {
		if root, ok := stackRoot(t, s, prID(n)); !ok || root != prID(1) {
			t.Errorf("StackRoot(#%d) = %q, %v", n, root, ok)
		}
	}
}
