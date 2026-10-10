package unstick

import (
	"reflect"
	"testing"
)

func dep(issue, on, typ string) Dep { return Dep{IssueID: issue, DependsOnID: on, Type: typ} }

func testGraph() *Graph {
	return NewGraph([]Row{
		{ID: "a", Dependencies: []Dep{dep("a", "b", DepBlocks), dep("a", "b", DepBlocks), dep("a", "ghost", DepBlocks), dep("a", "x", "related")}},
		{ID: "b"},
		{ID: "p1"},
		{ID: "p2", Dependencies: []Dep{dep("p2", "p1", DepParentChild)}},
		{ID: "p3", Dependencies: []Dep{dep("p3", "p2", DepParentChild)}},
		{ID: "p4", Dependencies: []Dep{dep("p4", "p2", DepParentChild)}},
		{ID: "c1", Dependencies: []Dep{dep("c1", "c2", DepParentChild)}},
		{ID: "c2", Dependencies: []Dep{dep("c2", "c1", DepParentChild)}},
	})
}

func TestGraphBlockers(t *testing.T) {
	g := testGraph()
	if got, want := g.Blockers("a"), []string{"b", "ghost"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Blockers = %v want %v", got, want)
	}
	if got, want := g.BlockerDependents("b"), []string{"a"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("BlockerDependents = %v want %v", got, want)
	}
	if got := g.Blockers("b"); len(got) != 0 {
		t.Fatalf("Blockers(b) = %v", got)
	}
}

func TestGraphUnknown(t *testing.T) {
	g := testGraph()
	if !g.HasUnknown("a") || !reflect.DeepEqual(g.Unknown("a"), []string{"ghost"}) {
		t.Fatalf("Unknown(a) = %v", g.Unknown("a"))
	}
	if g.HasUnknown("b") {
		t.Fatal("b has no unknown ids (related edge ignored)")
	}
	if g.Has("ghost") || !g.Has("a") {
		t.Fatal("Has wrong")
	}
}

func TestGraphHierarchy(t *testing.T) {
	g := testGraph()
	if p, ok := g.Parent("p3"); !ok || p != "p2" {
		t.Fatalf("Parent(p3) = %q %v", p, ok)
	}
	if _, ok := g.Parent("p1"); ok {
		t.Fatal("p1 has no parent")
	}
	if got, want := g.Ancestors("p3"), []string{"p1", "p2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Ancestors = %v want %v", got, want)
	}
	if got, want := g.Descendants("p1"), []string{"p2", "p3", "p4"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Descendants = %v want %v", got, want)
	}
	if got, want := g.Children("p2"), []string{"p3", "p4"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Children = %v want %v", got, want)
	}
}

func TestGraphParentCycleTerminates(t *testing.T) {
	g := testGraph()
	if got := g.Ancestors("c1"); !reflect.DeepEqual(got, []string{"c2"}) {
		t.Fatalf("Ancestors(c1) = %v", got)
	}
	if got := g.Descendants("c1"); !reflect.DeepEqual(got, []string{"c2"}) {
		t.Fatalf("Descendants(c1) = %v", got)
	}
}

func TestGraphIDsSortedAndCopies(t *testing.T) {
	g := testGraph()
	ids := g.IDs()
	if ids[0] != "a" || len(ids) != 8 {
		t.Fatalf("IDs = %v", ids)
	}
	b := g.Blockers("a")
	b[0] = "zzz"
	if g.Blockers("a")[0] != "b" {
		t.Fatal("Blockers must return a copy")
	}
}
