package dependency

import (
	"fmt"
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

const testRepo = "acme/api"

func prID(n int) string { return fmt.Sprintf("acme/api#%d", n) }

// putPR stores a PR row whose facts carry the fields the dependency data
// reads: state, head branch and base branch.
func putPR(t *testing.T, st *store.Store, id, state, branch, base string, merged bool) {
	t.Helper()
	facts := fmt.Sprintf(`{"pr_show":{"state":%q,"branch":%q,"base":%q,"merged":%v}}`, state, branch, base, merged)
	if err := st.UpsertEntity(store.Entity{Repo: testRepo, EntityType: "pr", EntityID: id, Facts: facts, AsOf: "2026-10-01T00:00:00Z"}); err != nil {
		t.Fatalf("seed %s: %v", id, err)
	}
}

func addDependsOn(t *testing.T, st *store.Store, from, to, actor string) {
	t.Helper()
	err := st.AddExternalXref(store.XrefLink{
		Repo: testRepo, FromType: "pr", FromID: from, ToType: "pr", ToID: to,
		Relation: RelationDependsOn, Actor: actor, ActedAt: "2026-10-01T12:00:00Z",
		FirstSeen: "2026-10-01T12:00:00Z", LastConfirmed: "2026-10-01T12:00:00Z",
	})
	if err != nil {
		t.Fatalf("add external link: %v", err)
	}
}

// seedStack stores a three-PR stack: #1 targets main, #2 targets #1's branch,
// #3 targets #2's branch.
func seedStack(t *testing.T, st *store.Store) {
	t.Helper()
	putPR(t, st, prID(1), "open", "feat-a", "main", false)
	putPR(t, st, prID(2), "open", "feat-b", "feat-a", false)
	putPR(t, st, prID(3), "open", "feat-c", "feat-b", false)
}

func ids(edges []Edge) []string {
	out := []string{}
	for _, e := range edges {
		out = append(out, e.ID)
	}
	return out
}

func deps(t *testing.T, r *Resolver, id string) []Edge {
	t.Helper()
	e, err := r.DependenciesOf("pr", id)
	if err != nil {
		t.Fatalf("DependenciesOf(%s): %v", id, err)
	}
	return e
}

func dependents(t *testing.T, r *Resolver, id string) []Edge {
	t.Helper()
	e, err := r.DependentsOf("pr", id)
	if err != nil {
		t.Fatalf("DependentsOf(%s): %v", id, err)
	}
	return e
}

// The stack source: A depends on B when A's base equals B's head branch in
// the same repository and B is open.
func TestStackSource_ThreePRStack(t *testing.T) {
	st := store.OpenNewSchemaForTest(t)
	seedStack(t, st)
	r := NewResolver(st, testRepo)

	if got := ids(deps(t, r, prID(1))); len(got) != 0 {
		t.Errorf("deps of the stack root = %v; want none", got)
	}
	got := deps(t, r, prID(2))
	want := []Edge{{Type: "pr", ID: prID(1), State: StateOpen, Sources: []string{SourceStack}, Origins: []string{"derived:stack"}}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("deps of #2 = %+v; want %+v", got, want)
	}
	if got := ids(deps(t, r, prID(3))); !reflect.DeepEqual(got, []string{prID(2)}) {
		t.Errorf("deps of #3 = %v; want only its direct base #2", got)
	}
	if got := ids(dependents(t, r, prID(1))); !reflect.DeepEqual(got, []string{prID(2)}) {
		t.Errorf("dependents of #1 = %v", got)
	}
	if got := ids(dependents(t, r, prID(3))); len(got) != 0 {
		t.Errorf("dependents of the stack tip = %v; want none", got)
	}
}

// Derived at read time: the same Resolver code sees a base PR that was stored
// AFTER its dependent, and a new Resolver sees a merge the moment the row
// changes (no write by this package).
func TestStackSource_ReadTimeAndMerge(t *testing.T) {
	st := store.OpenNewSchemaForTest(t)
	putPR(t, st, prID(2), "open", "feat-b", "feat-a", false) // dependent hydrated first
	if got := deps(t, NewResolver(st, testRepo), prID(2)); len(got) != 0 {
		t.Fatalf("deps with the base PR not stored = %v; want none", ids(got))
	}
	putPR(t, st, prID(1), "open", "feat-a", "main", false) // base hydrates later
	open, err := NewResolver(st, testRepo).OpenDependenciesOf("pr", prID(2))
	if err != nil || !reflect.DeepEqual(ids(open), []string{prID(1)}) {
		t.Fatalf("open deps = %v, %v; want [#1]", ids(open), err)
	}
	putPR(t, st, prID(1), "closed", "feat-a", "main", true) // base merges
	r := NewResolver(st, testRepo)
	if got := deps(t, r, prID(2)); len(got) != 0 {
		t.Errorf("deps after the base merged = %v; want none (stack needs B open)", ids(got))
	}
	open, err = r.OpenDependenciesOf("pr", prID(2))
	if err != nil || len(open) != 0 {
		t.Errorf("open deps after the base merged = %v, %v; want none", ids(open), err)
	}
	if got := dependents(t, r, prID(1)); len(got) != 0 {
		t.Errorf("dependents of a merged PR = %v; want none", ids(got))
	}
}

func TestStackSource_NotAnEdge(t *testing.T) {
	cases := []struct {
		name string
		seed func(t *testing.T, st *store.Store)
	}{
		{"closed unmerged base", func(t *testing.T, st *store.Store) {
			putPR(t, st, prID(1), "closed", "feat-a", "main", false)
			putPR(t, st, prID(2), "open", "feat-b", "feat-a", false)
		}},
		{"base branch on another repository", func(t *testing.T, st *store.Store) {
			putPR(t, st, "acme/other#1", "open", "feat-a", "main", false)
			putPR(t, st, prID(2), "open", "feat-b", "feat-a", false)
		}},
		{"same branch name, base is the default branch", func(t *testing.T, st *store.Store) {
			putPR(t, st, prID(1), "open", "main", "main", false)
			putPR(t, st, prID(2), "open", "feat-b", "main", false)
		}},
		{"PR whose base is its own head", func(t *testing.T, st *store.Store) {
			putPR(t, st, prID(2), "open", "feat-b", "feat-b", false)
		}},
		{"empty base and branch", func(t *testing.T, st *store.Store) {
			putPR(t, st, prID(1), "open", "", "", false)
			putPR(t, st, prID(2), "open", "", "", false)
		}},
		{"undecodable facts", func(t *testing.T, st *store.Store) {
			putPR(t, st, prID(2), "open", "feat-b", "feat-a", false)
			if err := st.UpsertEntity(store.Entity{Repo: testRepo, EntityType: "pr", EntityID: prID(1), Facts: "not json", AsOf: "2026-10-01T00:00:00Z"}); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st := store.OpenNewSchemaForTest(t)
			c.seed(t, st)
			r := NewResolver(st, testRepo)
			if got := deps(t, r, prID(2)); len(got) != 0 {
				t.Errorf("deps of #2 = %+v; want none", got)
			}
		})
	}
}

// Two open PRs from the same head branch are both dependencies.
func TestStackSource_TwoOpenPRsOnOneBranch(t *testing.T) {
	st := store.OpenNewSchemaForTest(t)
	putPR(t, st, prID(1), "open", "feat-a", "main", false)
	putPR(t, st, prID(5), "open", "feat-a", "release", false)
	putPR(t, st, prID(2), "open", "feat-b", "feat-a", false)
	if got := ids(deps(t, NewResolver(st, testRepo), prID(2))); !reflect.DeepEqual(got, []string{prID(1), prID(5)}) {
		t.Errorf("deps = %v; want both open PRs, ordered by id", got)
	}
}

func TestExternalSource(t *testing.T) {
	st := store.OpenNewSchemaForTest(t)
	putPR(t, st, prID(1), "open", "one", "main", false)
	putPR(t, st, prID(2), "open", "two", "main", false)
	putPR(t, st, prID(3), "closed", "three", "main", true)
	addDependsOn(t, st, prID(1), prID(2), "operator")
	addDependsOn(t, st, prID(1), prID(3), "operator")
	addDependsOn(t, st, prID(1), prID(2), "decider") // second actor, same edge
	r := NewResolver(st, testRepo)

	got := deps(t, r, prID(1))
	want := []Edge{
		{Type: "pr", ID: prID(2), State: StateOpen, Sources: []string{SourceExternal}, Origins: []string{"external:decider", "external:operator"}},
		{Type: "pr", ID: prID(3), State: StateMerged, Sources: []string{SourceExternal}, Origins: []string{"external:operator"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("deps of #1 =\n%+v\nwant\n%+v", got, want)
	}
	open, err := r.OpenDependenciesOf("pr", prID(1))
	if err != nil || !reflect.DeepEqual(ids(open), []string{prID(2)}) {
		t.Errorf("open deps = %v, %v; want only the open #2", ids(open), err)
	}
	if got := ids(dependents(t, r, prID(2))); !reflect.DeepEqual(got, []string{prID(1)}) {
		t.Errorf("dependents of #2 = %v; want [#1]", got)
	}
	// The link is directional: #2 does not depend on #1.
	if got := deps(t, r, prID(2)); len(got) != 0 {
		t.Errorf("deps of #2 = %v; want none", ids(got))
	}
}

// Only external depends_on rows between PRs count: another relation, a derived
// row with the same relation, and a non-PR end are ignored.
func TestExternalSource_IgnoresOtherLinks(t *testing.T) {
	st := store.OpenNewSchemaForTest(t)
	putPR(t, st, prID(1), "open", "one", "main", false)
	putPR(t, st, prID(2), "open", "two", "main", false)
	if err := st.AddExternalXref(store.XrefLink{
		Repo: testRepo, FromType: "pr", FromID: prID(1), ToType: "pr", ToID: prID(2),
		Relation: "references", Actor: "operator", ActedAt: "2026-10-01T12:00:00Z",
		FirstSeen: "2026-10-01T12:00:00Z", LastConfirmed: "2026-10-01T12:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.AddExternalXref(store.XrefLink{
		Repo: testRepo, FromType: "pr", FromID: prID(1), ToType: "issue", ToID: "ABC-1",
		Relation: RelationDependsOn, Actor: "operator", ActedAt: "2026-10-01T12:00:00Z",
		FirstSeen: "2026-10-01T12:00:00Z", LastConfirmed: "2026-10-01T12:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceDerivedXrefs(testRepo, "pr", prID(1), []store.XrefLink{{
		Repo: testRepo, FromType: "pr", FromID: prID(1), ToType: "pr", ToID: prID(2),
		Relation: RelationDependsOn, Origin: "derived:other", Evidence: "test",
		FirstSeen: "2026-10-01T00:00:00Z", LastConfirmed: "2026-10-01T00:00:00Z",
	}}); err != nil {
		t.Fatal(err)
	}
	if got := deps(t, NewResolver(st, testRepo), prID(1)); len(got) != 0 {
		t.Errorf("deps = %+v; want none", got)
	}
}

// A dependency with no stored row is not open: it cannot hold anything back.
func TestExternalSource_UnstoredDependencyIsNotOpen(t *testing.T) {
	st := store.OpenNewSchemaForTest(t)
	putPR(t, st, prID(1), "open", "one", "main", false)
	addDependsOn(t, st, prID(1), prID(9), "operator")
	r := NewResolver(st, testRepo)
	got := deps(t, r, prID(1))
	if len(got) != 1 || got[0].ID != prID(9) || got[0].State != "" || got[0].Open() {
		t.Fatalf("deps = %+v; want one unknown-state, not-open edge to #9", got)
	}
	if open, _ := r.OpenDependenciesOf("pr", prID(1)); len(open) != 0 {
		t.Errorf("open deps = %v; want none", ids(open))
	}
}

// The same edge claimed by both sources is one Edge naming both.
func TestSourcesMerge(t *testing.T) {
	st := store.OpenNewSchemaForTest(t)
	seedStack(t, st)
	addDependsOn(t, st, prID(2), prID(1), "operator")
	got := deps(t, NewResolver(st, testRepo), prID(2))
	want := []Edge{{
		Type: "pr", ID: prID(1), State: StateOpen,
		Sources: []string{SourceExternal, SourceStack}, Origins: []string{"derived:stack", "external:operator"},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("deps = %+v; want %+v", got, want)
	}
}

// On the unmigrated store (version 1) the stack source works and the external
// source quietly has nothing.
func TestUnmigratedStore(t *testing.T) {
	st := store.OpenForTest(t)
	seedStack(t, st)
	r := NewResolver(st, testRepo)
	if got := ids(deps(t, r, prID(3))); !reflect.DeepEqual(got, []string{prID(2)}) {
		t.Errorf("deps of #3 on a version 1 store = %v; want [#2]", got)
	}
	if got := ids(dependents(t, r, prID(1))); !reflect.DeepEqual(got, []string{prID(2)}) {
		t.Errorf("dependents of #1 on a version 1 store = %v; want [#2]", got)
	}
}

func TestNonPREntityHasNoDependencies(t *testing.T) {
	st := store.OpenNewSchemaForTest(t)
	seedStack(t, st)
	r := NewResolver(st, testRepo)
	for _, typ := range []string{"issue", "thread", ""} {
		if e, err := r.DependenciesOf(typ, prID(2)); err != nil || len(e) != 0 {
			t.Errorf("DependenciesOf(%q) = %v, %v; want none", typ, e, err)
		}
		if e, err := r.DependentsOf(typ, prID(1)); err != nil || len(e) != 0 {
			t.Errorf("DependentsOf(%q) = %v, %v; want none", typ, e, err)
		}
	}
}

// A shared Jira issue is a group, never a dependency.
func TestSharedIssueIsNotADependency(t *testing.T) {
	st := store.OpenNewSchemaForTest(t)
	putPR(t, st, prID(1), "open", "one", "main", false)
	putPR(t, st, prID(2), "open", "two", "main", false)
	for _, id := range []string{prID(1), prID(2)} {
		if err := st.ReplaceDerivedXrefs(testRepo, "pr", id, []store.XrefLink{{
			Repo: testRepo, FromType: "pr", FromID: id, ToType: "issue", ToID: "ABC-1",
			Relation: "jira", Origin: "derived:jira-key", Evidence: "test",
			FirstSeen: "2026-10-01T00:00:00Z", LastConfirmed: "2026-10-01T00:00:00Z",
		}}); err != nil {
			t.Fatal(err)
		}
	}
	r := NewResolver(st, testRepo)
	if got := deps(t, r, prID(1)); len(got) != 0 {
		t.Errorf("deps = %v; want none", ids(got))
	}
	if got := dependents(t, r, prID(1)); len(got) != 0 {
		t.Errorf("dependents = %v; want none", ids(got))
	}
}

// An unreadable store is an error, never "no dependencies".
type failingReader struct{ Reader }

func (failingReader) ListEntities() ([]store.Entity, error) { return nil, fmt.Errorf("boom") }

func TestStoreErrorIsAnError(t *testing.T) {
	st := store.OpenNewSchemaForTest(t)
	r := NewResolver(failingReader{st}, testRepo)
	if _, err := r.DependenciesOf("pr", prID(1)); err == nil {
		t.Fatal("DependenciesOf on an unreadable store returned no error")
	}
	if _, err := r.DependentsOf("pr", prID(1)); err == nil {
		t.Fatal("DependentsOf on an unreadable store returned no error")
	}
}

func TestRegistry(t *testing.T) {
	if got := NewRegistry().Names(); !reflect.DeepEqual(got, []string{SourceStack, SourceExternal}) {
		t.Errorf("Names = %v", got)
	}
	defer func() {
		if recover() == nil {
			t.Error("registering a duplicate source name did not panic")
		}
	}()
	NewRegistry().Register(stackSource{})
}

// The package is read-only and offline: no os/exec, no net, no wall clock.
func TestPackageImportsNoExecOrNetwork(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	scanned := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		scanned++
		af, err := parser.ParseFile(token.NewFileSet(), f, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", f, err)
		}
		for _, imp := range af.Imports {
			path, _ := strconv.Unquote(imp.Path.Value)
			if path == "os/exec" || path == "net" || strings.HasPrefix(path, "net/") {
				t.Errorf("%s imports %q: dependency reads the store only", f, path)
			}
		}
	}
	if scanned == 0 {
		t.Fatal("scanned no source files; the guard is vacuous")
	}
}
