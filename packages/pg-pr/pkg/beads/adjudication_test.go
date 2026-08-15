package beads

import (
	"reflect"
	"testing"
)

// supersedes builds one adjudication edge, spelled the way the reconcile records
// it: the retired bead carries the edge and names its canonical.
func supersedes(from, to string) Dependency {
	return Dependency{From: from, To: to, Type: adjudicationEdgeType}
}

// TestAdjudicatedIdentitiesRequiresBothEndpointsInTheGroup is the guard that
// keeps the marker verifiable (pg2-peyf0): an audit that honoured ANY supersedes
// edge could be made to drop a genuine duplicate by an unrelated annotation
// elsewhere in the workspace. Only an edge naming another member of the SAME
// identity counts.
func TestAdjudicatedIdentitiesRequiresBothEndpointsInTheGroup(t *testing.T) {
	ids := []string{"a", "b"}
	cases := []struct {
		name  string
		edges []Dependency
		want  [][]string
	}{
		{name: "inside the group", edges: []Dependency{supersedes("a", "b")}, want: [][]string{{"a", "b"}}},
		{name: "target outside the group", edges: []Dependency{supersedes("a", "elsewhere")}},
		{name: "source outside the group", edges: []Dependency{supersedes("elsewhere", "b")}},
		{name: "self edge", edges: []Dependency{supersedes("a", "a")}},
		{name: "wrong edge type", edges: []Dependency{{From: "a", To: "b", Type: "related"}}},
		{name: "blocks is not an adjudication", edges: []Dependency{{From: "a", To: "b", Type: "blocks"}}},
		{name: "no edges at all"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := adjudicatedIdentities(ids, tc.edges)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("adjudicatedIdentities = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestAdjudicatedIdentitiesIsTransitive pins that three beads adjudicated into
// one canonical collapse to ONE identity rather than two overlapping pairs —
// otherwise a 3-member group would still report an excess after being fully
// reconciled.
func TestAdjudicatedIdentitiesIsTransitive(t *testing.T) {
	ids := []string{"keep", "dup1", "dup2", "untouched"}
	edges := []Dependency{supersedes("dup1", "keep"), supersedes("dup2", "dup1")}
	got := adjudicatedIdentities(ids, edges)
	want := [][]string{{"dup1", "dup2", "keep"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("adjudicatedIdentities = %v, want %v", got, want)
	}
}

// TestDropAdjudicatedKeepsTheCanonicalNotTheEdgeSource pins the property that
// makes the direction-agnostic detection safe: which member survives is decided
// by the arm's canonical rule (here: OPEN over closed), never by which end of the
// edge was written first. A reversed edge must not make the audit retire the LIVE
// bead and advise closing it.
func TestDropAdjudicatedKeepsTheCanonicalNotTheEdgeSource(t *testing.T) {
	for _, tc := range []struct {
		name string
		edge Dependency
	}{
		{name: "excess names its canonical", edge: supersedes("cyc-closed", "cyc-open")},
		{name: "edge written the other way round", edge: supersedes("cyc-open", "cyc-closed")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			group := []ProcessingCycle{
				{ID: "cyc-closed", Status: "closed", Dependencies: []Dependency{tc.edge}},
				{ID: "cyc-open", Status: "open"},
			}
			got := dropAdjudicated(group,
				func(pc ProcessingCycle) string { return pc.ID },
				func(pc ProcessingCycle) []Dependency { return pc.Dependencies },
				pickAuditCanonicalCycle)
			if len(got) != 1 || got[0].ID != "cyc-open" {
				t.Fatalf("survivors = %+v, want just cyc-open (the live cycle)", got)
			}
		})
	}
}

// TestDropAdjudicatedLeavesAnUnadjudicatedGroupIdentical pins that the exclusion
// is inert when nothing has been adjudicated — the common case, and the one
// pg2-0z8fw's status-agnostic counts depend on.
func TestDropAdjudicatedLeavesAnUnadjudicatedGroupIdentical(t *testing.T) {
	group := []ProcessingCycle{{ID: "cyc-a", Status: "closed"}, {ID: "cyc-b", Status: "closed"}}
	got := dropAdjudicated(group,
		func(pc ProcessingCycle) string { return pc.ID },
		func(pc ProcessingCycle) []Dependency { return pc.Dependencies },
		pickAuditCanonicalCycle)
	if !reflect.DeepEqual(got, group) {
		t.Fatalf("survivors = %+v, want the group unchanged %+v", got, group)
	}
}
