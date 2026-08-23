package prdeps

import (
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"strings"
	"testing"
)

// repoName / otherRepo / trunkRef are placeholders on purpose: no
// employer-specific identifier belongs in this repo's code or tests. otherRepo
// sorts AFTER repoName so the cross-repo cases can state the expected node order
// unambiguously.
const (
	repoName  = "owner/repo"
	otherRepo = "zzz/other"
	trunkRef  = "trunk"
)

func ref(n int) Ref { return Ref{Repo: repoName, Number: n} }

// openPR builds an open PR in repoName.
func openPR(n int, head, base string) PR {
	return PR{Repo: repoName, Number: n, Head: head, Base: base, State: "open"}
}

// statePR builds a PR in repoName with an explicit lifecycle state.
func statePR(n int, head, base, state string) PR {
	return PR{Repo: repoName, Number: n, Head: head, Base: base, State: state}
}

// TestDerive is the whole-set table: one row per structural case the base-chain
// detector must decide mechanically. Each row is an input PR set plus the
// configured trunk refs, and the complete expected Graph — every Node field and
// every Diagnostic — so no field is silently unasserted.
func TestDerive(t *testing.T) {
	cases := []struct {
		name string
		in   Input
		want Graph
	}{
		{
			name: "empty set",
			in:   Input{TrunkRefs: []string{trunkRef}},
			want: Graph{Nodes: []Node{}},
		},
		{
			name: "lone PR based on trunk is standalone",
			in: Input{
				PRs:       []PR{openPR(1, "feature-a", trunkRef)},
				TrunkRefs: []string{trunkRef},
			},
			want: Graph{Nodes: []Node{
				{Ref: ref(1), Open: true, Resolution: ResolutionTrunk},
			}},
		},
		{
			name: "two-deep stack",
			in: Input{
				PRs: []PR{
					openPR(1, "feature-a", trunkRef),
					openPR(2, "feature-b", "feature-a"),
				},
				TrunkRefs: []string{trunkRef},
			},
			want: Graph{Nodes: []Node{
				{Ref: ref(1), Open: true, Resolution: ResolutionTrunk, Downstream: []Ref{ref(2)}},
				{Ref: ref(2), Open: true, Resolution: ResolutionUpstream, Upstream: ref(1), Depth: 1},
			}},
		},
		{
			name: "three-deep stack has transitive depth",
			in: Input{
				PRs: []PR{
					openPR(1, "feature-a", trunkRef),
					openPR(2, "feature-b", "feature-a"),
					openPR(3, "feature-c", "feature-b"),
				},
				TrunkRefs: []string{trunkRef},
			},
			want: Graph{Nodes: []Node{
				{Ref: ref(1), Open: true, Resolution: ResolutionTrunk, Downstream: []Ref{ref(2)}},
				{Ref: ref(2), Open: true, Resolution: ResolutionUpstream, Upstream: ref(1), Depth: 1, Downstream: []Ref{ref(3)}},
				{Ref: ref(3), Open: true, Resolution: ResolutionUpstream, Upstream: ref(2), Depth: 2},
			}},
		},
		{
			// Diamond: #2 carries TWO downstreams, each listed exactly once and in
			// number order.
			name: "diamond: one upstream with two downstreams",
			in: Input{
				PRs: []PR{
					openPR(1, "feature-a", trunkRef),
					openPR(2, "feature-b", "feature-a"),
					openPR(3, "feature-c", "feature-b"),
					openPR(4, "feature-d", "feature-b"),
				},
				TrunkRefs: []string{trunkRef},
			},
			want: Graph{Nodes: []Node{
				{Ref: ref(1), Open: true, Resolution: ResolutionTrunk, Downstream: []Ref{ref(2)}},
				{
					Ref: ref(2), Open: true, Resolution: ResolutionUpstream, Upstream: ref(1), Depth: 1,
					Downstream: []Ref{ref(3), ref(4)},
				},
				{Ref: ref(3), Open: true, Resolution: ResolutionUpstream, Upstream: ref(2), Depth: 2},
				{Ref: ref(4), Open: true, Resolution: ResolutionUpstream, Upstream: ref(2), Depth: 2},
			}},
		},
		{
			// A 2-cycle: the pass must TERMINATE, return a defined result, and
			// REPORT the cycle rather than flatten it. Depth is 0 on a cyclic
			// chain because such a chain has no bottom to measure from.
			name: "two-cycle terminates and is reported",
			in: Input{
				PRs: []PR{
					openPR(1, "feature-a", "feature-b"),
					openPR(2, "feature-b", "feature-a"),
				},
				TrunkRefs: []string{trunkRef},
			},
			want: Graph{
				Nodes: []Node{
					{
						Ref: ref(1), Open: true, Resolution: ResolutionUpstream, Upstream: ref(2),
						Downstream: []Ref{ref(2)}, Cyclic: true,
					},
					{
						Ref: ref(2), Open: true, Resolution: ResolutionUpstream, Upstream: ref(1),
						Downstream: []Ref{ref(1)}, Cyclic: true,
					},
				},
				Diagnostics: []Diagnostic{
					{Kind: DiagnosticCycle, Refs: []Ref{ref(1), ref(2)}},
				},
			},
		},
		{
			name: "three-cycle terminates and is reported",
			in: Input{
				PRs: []PR{
					openPR(1, "feature-a", "feature-b"),
					openPR(2, "feature-b", "feature-c"),
					openPR(3, "feature-c", "feature-a"),
				},
				TrunkRefs: []string{trunkRef},
			},
			want: Graph{
				Nodes: []Node{
					{
						Ref: ref(1), Open: true, Resolution: ResolutionUpstream, Upstream: ref(2),
						Downstream: []Ref{ref(3)}, Cyclic: true,
					},
					{
						Ref: ref(2), Open: true, Resolution: ResolutionUpstream, Upstream: ref(3),
						Downstream: []Ref{ref(1)}, Cyclic: true,
					},
					{
						Ref: ref(3), Open: true, Resolution: ResolutionUpstream, Upstream: ref(1),
						Downstream: []Ref{ref(2)}, Cyclic: true,
					},
				},
				Diagnostics: []Diagnostic{
					{Kind: DiagnosticCycle, Refs: []Ref{ref(1), ref(2), ref(3)}},
				},
			},
		},
		{
			// #3 is stacked on a cycle: its own chain never bottoms out, so it is
			// Cyclic — but it is NOT a member of the cycle and must not appear in
			// the diagnostic.
			name: "PR stacked on a cycle is cyclic but not a member",
			in: Input{
				PRs: []PR{
					openPR(1, "feature-a", "feature-b"),
					openPR(2, "feature-b", "feature-a"),
					openPR(3, "feature-c", "feature-a"),
				},
				TrunkRefs: []string{trunkRef},
			},
			want: Graph{
				Nodes: []Node{
					{
						Ref: ref(1), Open: true, Resolution: ResolutionUpstream, Upstream: ref(2),
						Downstream: []Ref{ref(2), ref(3)}, Cyclic: true,
					},
					{
						Ref: ref(2), Open: true, Resolution: ResolutionUpstream, Upstream: ref(1),
						Downstream: []Ref{ref(1)}, Cyclic: true,
					},
					{Ref: ref(3), Open: true, Resolution: ResolutionUpstream, Upstream: ref(1), Cyclic: true},
				},
				Diagnostics: []Diagnostic{
					{Kind: DiagnosticCycle, Refs: []Ref{ref(1), ref(2)}},
				},
			},
		},
		{
			// The walk reaches the cycle from OUTSIDE it (#1 is the lowest number
			// and is not a member), and it enters at #3 rather than at the cycle's
			// lowest member — so the reported list must be exactly the cycle,
			// ROTATED to start at #2. Without the rotation the same cycle would be
			// reported as {#3, #2} here and as {#2, #3} from another entry point.
			name: "cycle entered from outside reports only its members, rotated",
			in: Input{
				PRs: []PR{
					openPR(1, "outside", "feature-c"),
					openPR(2, "feature-b", "feature-c"),
					openPR(3, "feature-c", "feature-b"),
				},
				TrunkRefs: []string{trunkRef},
			},
			want: Graph{
				Nodes: []Node{
					{Ref: ref(1), Open: true, Resolution: ResolutionUpstream, Upstream: ref(3), Cyclic: true},
					{
						Ref: ref(2), Open: true, Resolution: ResolutionUpstream, Upstream: ref(3),
						Downstream: []Ref{ref(3)}, Cyclic: true,
					},
					{
						Ref: ref(3), Open: true, Resolution: ResolutionUpstream, Upstream: ref(2),
						Downstream: []Ref{ref(1), ref(2)}, Cyclic: true,
					},
				},
				Diagnostics: []Diagnostic{
					{Kind: DiagnosticCycle, Refs: []Ref{ref(2), ref(3)}},
				},
			},
		},
		{
			name: "self-referential base is rejected with no self-edge",
			in: Input{
				PRs:       []PR{openPR(1, "feature-a", "feature-a")},
				TrunkRefs: []string{trunkRef},
			},
			want: Graph{
				Nodes: []Node{
					{Ref: ref(1), Open: true, Resolution: ResolutionSelf},
				},
				Diagnostics: []Diagnostic{
					{Kind: DiagnosticSelfBase, Refs: []Ref{ref(1)}, RefName: "feature-a"},
				},
			},
		},
		{
			// Two open PRs share a head ref: the tie-break is the LOWEST number,
			// and the ambiguity is reported as well as resolved.
			name: "two open PRs sharing a head ref: lowest number wins",
			in: Input{
				PRs: []PR{
					openPR(1, "feature-a", trunkRef),
					openPR(2, "feature-a", trunkRef),
					openPR(3, "feature-c", "feature-a"),
				},
				TrunkRefs: []string{trunkRef},
			},
			want: Graph{
				Nodes: []Node{
					{Ref: ref(1), Open: true, Resolution: ResolutionTrunk, Downstream: []Ref{ref(3)}},
					{Ref: ref(2), Open: true, Resolution: ResolutionTrunk},
					{Ref: ref(3), Open: true, Resolution: ResolutionUpstream, Upstream: ref(1), Depth: 1},
				},
				Diagnostics: []Diagnostic{
					{Kind: DiagnosticAmbiguousHead, Refs: []Ref{ref(1), ref(2)}, RefName: "feature-a"},
				},
			},
		},
		{
			name: "deleted base branch is unresolvable, with no phantom relation",
			in: Input{
				PRs: []PR{
					openPR(1, "feature-a", trunkRef),
					openPR(2, "feature-b", "deleted-branch"),
				},
				TrunkRefs: []string{trunkRef},
			},
			want: Graph{
				Nodes: []Node{
					{Ref: ref(1), Open: true, Resolution: ResolutionTrunk},
					{Ref: ref(2), Open: true, Resolution: ResolutionUnresolvable},
				},
				Diagnostics: []Diagnostic{
					{Kind: DiagnosticUnresolvableBase, Refs: []Ref{ref(2)}, RefName: "deleted-branch"},
				},
			},
		},
		{
			// Only an OPEN PR's head can be an upstream, so a merged #1 leaves #2
			// unresolvable. This asserts the RELATION's definition, not the
			// merged-middle policy (whether #2 should be re-pointed at #1's own
			// upstream or read as unblocked) — that ruling is the dependent leaf's.
			name: "a merged PR is not a candidate upstream",
			in: Input{
				PRs: []PR{
					statePR(1, "feature-a", trunkRef, "merged"),
					openPR(2, "feature-b", "feature-a"),
				},
				TrunkRefs: []string{trunkRef},
			},
			want: Graph{
				Nodes: []Node{
					{Ref: ref(1), Resolution: ResolutionTrunk},
					{Ref: ref(2), Open: true, Resolution: ResolutionUnresolvable},
				},
				Diagnostics: []Diagnostic{
					{Kind: DiagnosticUnresolvableBase, Refs: []Ref{ref(2)}, RefName: "feature-a"},
				},
			},
		},
		{
			// A draft PR IS open, so a stack may be built on top of one.
			name: "a draft PR is a candidate upstream",
			in: Input{
				PRs: []PR{
					statePR(1, "feature-a", trunkRef, "draft"),
					openPR(2, "feature-b", "feature-a"),
				},
				TrunkRefs: []string{trunkRef},
			},
			want: Graph{Nodes: []Node{
				{Ref: ref(1), Open: true, Resolution: ResolutionTrunk, Downstream: []Ref{ref(2)}},
				{Ref: ref(2), Open: true, Resolution: ResolutionUpstream, Upstream: ref(1), Depth: 1},
			}},
		},
		{
			// The head index is scoped by repo, so identical ref names in two
			// repositories can never match and no cross-repo edge is invented.
			name: "identical ref names in different repos never match",
			in: Input{
				PRs: []PR{
					openPR(1, "feature-a", trunkRef),
					{Repo: otherRepo, Number: 2, Head: "feature-b", Base: "feature-a", State: "open"},
				},
				TrunkRefs: []string{trunkRef},
			},
			want: Graph{
				Nodes: []Node{
					{Ref: ref(1), Open: true, Resolution: ResolutionTrunk},
					{Ref: Ref{Repo: otherRepo, Number: 2}, Open: true, Resolution: ResolutionUnresolvable},
				},
				Diagnostics: []Diagnostic{
					{Kind: DiagnosticUnresolvableBase, Refs: []Ref{{Repo: otherRepo, Number: 2}}, RefName: "feature-a"},
				},
			},
		},
		{
			// A repo-qualified base ref names a fork: never looked up, so #1 gains
			// no downstream.
			name: "fork-qualified base is foreign and never matched",
			in: Input{
				PRs: []PR{
					openPR(1, "feature-a", trunkRef),
					openPR(2, "feature-b", "fork-owner:feature-a"),
				},
				TrunkRefs: []string{trunkRef},
			},
			want: Graph{
				Nodes: []Node{
					{Ref: ref(1), Open: true, Resolution: ResolutionTrunk},
					{Ref: ref(2), Open: true, Resolution: ResolutionForeign},
				},
				Diagnostics: []Diagnostic{
					{Kind: DiagnosticForeignBase, Refs: []Ref{ref(2)}, RefName: "fork-owner:feature-a"},
				},
			},
		},
		{
			// Even a qualifier naming this PR's OWN repo stays foreign: the
			// provider never emits a qualified same-repo baseRefName, so stripping
			// it would be inventing a normalization.
			name: "own-repo-qualified base is foreign too",
			in: Input{
				PRs: []PR{
					openPR(1, "feature-a", trunkRef),
					openPR(2, "feature-b", repoName+":feature-a"),
				},
				TrunkRefs: []string{trunkRef},
			},
			want: Graph{
				Nodes: []Node{
					{Ref: ref(1), Open: true, Resolution: ResolutionTrunk},
					{Ref: ref(2), Open: true, Resolution: ResolutionForeign},
				},
				Diagnostics: []Diagnostic{
					{Kind: DiagnosticForeignBase, Refs: []Ref{ref(2)}, RefName: repoName + ":feature-a"},
				},
			},
		},
		{
			// An empty ref name identifies nothing: an empty base must not match an
			// empty head and acquire a phantom upstream, two headless PRs are not
			// "two PRs sharing a head ref", and #4 — empty on BOTH sides — is
			// unresolvable rather than self-referential.
			name: "empty base and empty head form no edge, no ambiguity and no self-reference",
			in: Input{
				PRs: []PR{
					openPR(1, "", trunkRef),
					openPR(2, "", trunkRef),
					openPR(3, "feature-c", ""),
					openPR(4, "", ""),
				},
				TrunkRefs: []string{trunkRef},
			},
			want: Graph{
				Nodes: []Node{
					{Ref: ref(1), Open: true, Resolution: ResolutionTrunk},
					{Ref: ref(2), Open: true, Resolution: ResolutionTrunk},
					{Ref: ref(3), Open: true, Resolution: ResolutionUnresolvable},
					{Ref: ref(4), Open: true, Resolution: ResolutionUnresolvable},
				},
				Diagnostics: []Diagnostic{
					{Kind: DiagnosticUnresolvableBase, Refs: []Ref{ref(3)}},
					{Kind: DiagnosticUnresolvableBase, Refs: []Ref{ref(4)}},
				},
			},
		},
		{
			// The upstream carries a HIGHER number than its downstream — a base ref
			// repointed after the fact. Depth must still count hops down the chain,
			// which means the walk has to unwind a path of more than one PR (the
			// low-to-high case bottoms out one PR at a time and never does).
			name: "depth is counted when the upstream outranks its downstream",
			in: Input{
				PRs: []PR{
					openPR(1, "feature-a", "feature-b"),
					openPR(2, "feature-b", "feature-c"),
					openPR(3, "feature-c", trunkRef),
				},
				TrunkRefs: []string{trunkRef},
			},
			want: Graph{Nodes: []Node{
				{Ref: ref(1), Open: true, Resolution: ResolutionUpstream, Upstream: ref(2), Depth: 2},
				{
					Ref: ref(2), Open: true, Resolution: ResolutionUpstream, Upstream: ref(3), Depth: 1,
					Downstream: []Ref{ref(1)},
				},
				{Ref: ref(3), Open: true, Resolution: ResolutionTrunk, Downstream: []Ref{ref(2)}},
			}},
		},
		{
			// Two cycles, each entered from OUTSIDE by a lower-numbered PR, so the
			// walk DISCOVERS {#5,#6} before {#3,#4} — the reverse of the reported
			// order. Sorting the diagnostics is the only thing that fixes that.
			name: "two cycles are reported in sorted order, not discovery order",
			in: Input{
				PRs: []PR{
					openPR(1, "into-b", "cy-b"),
					openPR(2, "into-d", "cy-d"),
					openPR(3, "cy-c", "cy-d"),
					openPR(4, "cy-d", "cy-c"),
					openPR(5, "cy-a", "cy-b"),
					openPR(6, "cy-b", "cy-a"),
				},
				TrunkRefs: []string{trunkRef},
			},
			want: Graph{
				Nodes: []Node{
					{Ref: ref(1), Open: true, Resolution: ResolutionUpstream, Upstream: ref(6), Cyclic: true},
					{Ref: ref(2), Open: true, Resolution: ResolutionUpstream, Upstream: ref(4), Cyclic: true},
					{
						Ref: ref(3), Open: true, Resolution: ResolutionUpstream, Upstream: ref(4),
						Downstream: []Ref{ref(4)}, Cyclic: true,
					},
					{
						Ref: ref(4), Open: true, Resolution: ResolutionUpstream, Upstream: ref(3),
						Downstream: []Ref{ref(2), ref(3)}, Cyclic: true,
					},
					{
						Ref: ref(5), Open: true, Resolution: ResolutionUpstream, Upstream: ref(6),
						Downstream: []Ref{ref(6)}, Cyclic: true,
					},
					{
						Ref: ref(6), Open: true, Resolution: ResolutionUpstream, Upstream: ref(5),
						Downstream: []Ref{ref(1), ref(5)}, Cyclic: true,
					},
				},
				Diagnostics: []Diagnostic{
					{Kind: DiagnosticCycle, Refs: []Ref{ref(3), ref(4)}},
					{Kind: DiagnosticCycle, Refs: []Ref{ref(5), ref(6)}},
				},
			},
		},
		{
			// Trunk is checked BEFORE the head index, so an open PR that happens
			// to head a trunk-named ref is not treated as anyone's upstream.
			name: "trunk wins over an open PR heading a trunk ref",
			in: Input{
				PRs: []PR{
					openPR(1, trunkRef, "release"),
					openPR(2, "feature-b", trunkRef),
				},
				TrunkRefs: []string{trunkRef, "release"},
			},
			want: Graph{Nodes: []Node{
				{Ref: ref(1), Open: true, Resolution: ResolutionTrunk},
				{Ref: ref(2), Open: true, Resolution: ResolutionTrunk},
			}},
		},
		{
			// Downstream is the exact inverse of the resolved upstream edges, so a
			// CLOSED downstream keeps its edge; Open is what flags it.
			name: "a closed downstream keeps its edge and is flagged not open",
			in: Input{
				PRs: []PR{
					openPR(1, "feature-a", trunkRef),
					statePR(2, "feature-b", "feature-a", "closed"),
				},
				TrunkRefs: []string{trunkRef},
			},
			want: Graph{Nodes: []Node{
				{Ref: ref(1), Open: true, Resolution: ResolutionTrunk, Downstream: []Ref{ref(2)}},
				{Ref: ref(2), Resolution: ResolutionUpstream, Upstream: ref(1), Depth: 1},
			}},
		},
		{
			// State is compared case-insensitively, mirroring internal/sync's
			// stateForPR, and an unrecognised state is not open.
			name: "state matching is case-insensitive and unknown states are not open",
			in: Input{
				PRs: []PR{
					statePR(1, "feature-a", trunkRef, "OPEN"),
					openPR(2, "feature-b", "feature-a"),
					statePR(3, "feature-c", trunkRef, "somethingelse"),
					openPR(4, "feature-d", "feature-c"),
				},
				TrunkRefs: []string{trunkRef},
			},
			want: Graph{
				Nodes: []Node{
					{Ref: ref(1), Open: true, Resolution: ResolutionTrunk, Downstream: []Ref{ref(2)}},
					{Ref: ref(2), Open: true, Resolution: ResolutionUpstream, Upstream: ref(1), Depth: 1},
					{Ref: ref(3), Resolution: ResolutionTrunk},
					{Ref: ref(4), Open: true, Resolution: ResolutionUnresolvable},
				},
				Diagnostics: []Diagnostic{
					{Kind: DiagnosticUnresolvableBase, Refs: []Ref{ref(4)}, RefName: "feature-c"},
				},
			},
		},
		{
			// With no trunk configured, nothing is recognised as the bottom of a
			// chain, so a trunk-named base reads as unresolvable rather than
			// silently matching a literal.
			name: "no configured trunk leaves a trunk-named base unresolvable",
			in: Input{
				PRs: []PR{openPR(1, "feature-a", trunkRef)},
			},
			want: Graph{
				Nodes: []Node{
					{Ref: ref(1), Open: true, Resolution: ResolutionUnresolvable},
				},
				Diagnostics: []Diagnostic{
					{Kind: DiagnosticUnresolvableBase, Refs: []Ref{ref(1)}, RefName: trunkRef},
				},
			},
		},
		{
			name: "two repos with independent stacks order by repo then number",
			in: Input{
				PRs: []PR{
					{Repo: otherRepo, Number: 5, Head: "feature-b", Base: "feature-a", State: "open"},
					openPR(2, "feature-b", "feature-a"),
					{Repo: otherRepo, Number: 1, Head: "feature-a", Base: trunkRef, State: "open"},
					openPR(1, "feature-a", trunkRef),
				},
				TrunkRefs: []string{trunkRef},
			},
			want: Graph{Nodes: []Node{
				{Ref: ref(1), Open: true, Resolution: ResolutionTrunk, Downstream: []Ref{ref(2)}},
				{Ref: ref(2), Open: true, Resolution: ResolutionUpstream, Upstream: ref(1), Depth: 1},
				{
					Ref: Ref{Repo: otherRepo, Number: 1}, Open: true, Resolution: ResolutionTrunk,
					Downstream: []Ref{{Repo: otherRepo, Number: 5}},
				},
				{
					Ref: Ref{Repo: otherRepo, Number: 5}, Open: true, Resolution: ResolutionUpstream,
					Upstream: Ref{Repo: otherRepo, Number: 1}, Depth: 1,
				},
			}},
		},
		{
			// Every diagnostic kind at once, with the whole graph asserted around
			// them. The adversarially-ordered, multi-per-kind version of this
			// check is TestDeriveDiagnosticsAreATotalOrder.
			name: "every diagnostic kind appears, ordered by kind",
			in: Input{
				PRs: []PR{
					openPR(1, "dup", "deleted-branch"),
					openPR(2, "dup", trunkRef),
					openPR(3, "selfish", "selfish"),
					openPR(4, "forked", "fork-owner:elsewhere"),
					openPR(5, "loop-p", "loop-q"),
					openPR(6, "loop-q", "loop-p"),
				},
				TrunkRefs: []string{trunkRef},
			},
			want: Graph{
				Nodes: []Node{
					{Ref: ref(1), Open: true, Resolution: ResolutionUnresolvable},
					{Ref: ref(2), Open: true, Resolution: ResolutionTrunk},
					{Ref: ref(3), Open: true, Resolution: ResolutionSelf},
					{Ref: ref(4), Open: true, Resolution: ResolutionForeign},
					{
						Ref: ref(5), Open: true, Resolution: ResolutionUpstream, Upstream: ref(6),
						Downstream: []Ref{ref(6)}, Cyclic: true,
					},
					{
						Ref: ref(6), Open: true, Resolution: ResolutionUpstream, Upstream: ref(5),
						Downstream: []Ref{ref(5)}, Cyclic: true,
					},
				},
				Diagnostics: []Diagnostic{
					{Kind: DiagnosticCycle, Refs: []Ref{ref(5), ref(6)}},
					{Kind: DiagnosticSelfBase, Refs: []Ref{ref(3)}, RefName: "selfish"},
					{Kind: DiagnosticForeignBase, Refs: []Ref{ref(4)}, RefName: "fork-owner:elsewhere"},
					{Kind: DiagnosticUnresolvableBase, Refs: []Ref{ref(1)}, RefName: "deleted-branch"},
					{Kind: DiagnosticAmbiguousHead, Refs: []Ref{ref(1), ref(2)}, RefName: "dup"},
				},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Derive(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Derive() =\n%swant\n%s", dumpGraph(got), dumpGraph(tc.want))
			}
		})
	}
}

// The derived graph must not depend on the order the PRs arrive in: the node
// order, the downstream lists and the diagnostics are all keyed on (repo,
// number), which is a TOTAL order. Reversing the input is the cheapest probe
// that catches an accidental dependence on input order.
func TestDeriveIsInputOrderIndependent(t *testing.T) {
	forward := []PR{
		openPR(1, "feature-a", trunkRef),
		openPR(2, "feature-b", "feature-a"),
		openPR(3, "feature-c", "feature-b"),
		openPR(4, "feature-d", "feature-b"),
		openPR(5, "feature-e", "deleted-branch"),
		openPR(6, "loop-p", "loop-q"),
		openPR(7, "loop-q", "loop-p"),
	}
	reversed := make([]PR, 0, len(forward))
	for i := len(forward) - 1; i >= 0; i-- {
		reversed = append(reversed, forward[i])
	}
	a := Derive(Input{PRs: forward, TrunkRefs: []string{trunkRef}})
	b := Derive(Input{PRs: reversed, TrunkRefs: []string{trunkRef}})
	if !reflect.DeepEqual(a, b) {
		t.Errorf("reversing the input changed the graph:\nforward\n%sreversed\n%s", dumpGraph(a), dumpGraph(b))
	}
	// Sanity: the fixture really does exercise relations and diagnostics, so a
	// trivially-equal pair cannot pass this test vacuously.
	if len(a.Nodes) != len(forward) || len(a.Diagnostics) == 0 {
		t.Fatalf("fixture is not exercising the derivation: %s", dumpGraph(a))
	}
}

// The diagnostic ordering must be a CONSISTENT total order, not one that merely
// happens to agree with the order the derivation emitted things in. This set is
// adversarial in two ways: it carries THREE diagnostics of every kind, which is
// enough of them that sort.Slice takes its partitioning path instead of a plain
// insertion pass, and its PR numbers run OPPOSITE to its kind order — the
// highest-kind findings sit on the lowest-numbered PRs. A comparator that falls
// through to the PR comparison when the KINDS differ is self-contradictory on
// exactly those pairs, and mis-sorts this set.
//
// Only the diagnostics are asserted; the node-level facts for each shape are
// covered by TestDerive.
func TestDeriveDiagnosticsAreATotalOrder(t *testing.T) {
	in := Input{
		PRs: []PR{
			// Ambiguous heads: highest kind, lowest numbers.
			openPR(1, "dup-a", trunkRef),
			openPR(2, "dup-a", trunkRef),
			openPR(3, "dup-b", trunkRef),
			openPR(4, "dup-b", trunkRef),
			openPR(5, "dup-c", trunkRef),
			openPR(6, "dup-c", trunkRef),
			openPR(7, "u1", "gone-1"),
			openPR(8, "u2", "gone-2"),
			openPR(9, "u3", "gone-3"),
			openPR(10, "f1", "fork-owner:1"),
			openPR(11, "f2", "fork-owner:2"),
			openPR(12, "f3", "fork-owner:3"),
			openPR(13, "s1", "s1"),
			openPR(14, "s2", "s2"),
			openPR(15, "s3", "s3"),
			// Cycles: lowest kind, highest numbers.
			openPR(16, "p1", "q1"),
			openPR(17, "q1", "p1"),
			openPR(18, "p2", "q2"),
			openPR(19, "q2", "p2"),
			openPR(20, "p3", "q3"),
			openPR(21, "q3", "p3"),
		},
		TrunkRefs: []string{trunkRef},
	}
	want := []Diagnostic{
		{Kind: DiagnosticCycle, Refs: []Ref{ref(16), ref(17)}},
		{Kind: DiagnosticCycle, Refs: []Ref{ref(18), ref(19)}},
		{Kind: DiagnosticCycle, Refs: []Ref{ref(20), ref(21)}},
		{Kind: DiagnosticSelfBase, Refs: []Ref{ref(13)}, RefName: "s1"},
		{Kind: DiagnosticSelfBase, Refs: []Ref{ref(14)}, RefName: "s2"},
		{Kind: DiagnosticSelfBase, Refs: []Ref{ref(15)}, RefName: "s3"},
		{Kind: DiagnosticForeignBase, Refs: []Ref{ref(10)}, RefName: "fork-owner:1"},
		{Kind: DiagnosticForeignBase, Refs: []Ref{ref(11)}, RefName: "fork-owner:2"},
		{Kind: DiagnosticForeignBase, Refs: []Ref{ref(12)}, RefName: "fork-owner:3"},
		{Kind: DiagnosticUnresolvableBase, Refs: []Ref{ref(7)}, RefName: "gone-1"},
		{Kind: DiagnosticUnresolvableBase, Refs: []Ref{ref(8)}, RefName: "gone-2"},
		{Kind: DiagnosticUnresolvableBase, Refs: []Ref{ref(9)}, RefName: "gone-3"},
		{Kind: DiagnosticAmbiguousHead, Refs: []Ref{ref(1), ref(2)}, RefName: "dup-a"},
		{Kind: DiagnosticAmbiguousHead, Refs: []Ref{ref(3), ref(4)}, RefName: "dup-b"},
		{Kind: DiagnosticAmbiguousHead, Refs: []Ref{ref(5), ref(6)}, RefName: "dup-c"},
	}
	got := Derive(in).Diagnostics
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Derive().Diagnostics =\n%s\nwant\n%s", dumpDiagnostics(got), dumpDiagnostics(want))
	}
}

// The ambiguity diagnostics are emitted from a MAP iteration, so their order is
// defined only by Derive's final sort. Go randomises map iteration on every
// range, so a SINGLE call can produce the right order by luck — repeating the
// derivation is what actually exercises the sort. Four shared head refs give the
// map 24 orders to hand over, so 200 repeats leave no room for a lucky pass.
func TestDeriveDiagnosticOrderIsStableAcrossRepeats(t *testing.T) {
	in := Input{
		PRs: []PR{
			openPR(1, "dup-a", trunkRef),
			openPR(2, "dup-a", trunkRef),
			openPR(3, "dup-b", trunkRef),
			openPR(4, "dup-b", trunkRef),
			openPR(5, "dup-c", trunkRef),
			openPR(6, "dup-c", trunkRef),
			openPR(7, "dup-d", trunkRef),
			openPR(8, "dup-d", trunkRef),
		},
		TrunkRefs: []string{trunkRef},
	}
	want := []Diagnostic{
		{Kind: DiagnosticAmbiguousHead, Refs: []Ref{ref(1), ref(2)}, RefName: "dup-a"},
		{Kind: DiagnosticAmbiguousHead, Refs: []Ref{ref(3), ref(4)}, RefName: "dup-b"},
		{Kind: DiagnosticAmbiguousHead, Refs: []Ref{ref(5), ref(6)}, RefName: "dup-c"},
		{Kind: DiagnosticAmbiguousHead, Refs: []Ref{ref(7), ref(8)}, RefName: "dup-d"},
	}
	for i := 0; i < 200; i++ {
		got := Derive(in).Diagnostics
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("run %d diagnostics = %+v, want %+v", i, got, want)
		}
	}
}

// Derive is pure, so it must not reorder or otherwise touch the caller's slice.
func TestDeriveDoesNotMutateInput(t *testing.T) {
	in := []PR{
		openPR(3, "feature-c", "feature-b"),
		openPR(1, "feature-a", trunkRef),
		openPR(2, "feature-b", "feature-a"),
	}
	before := make([]PR, len(in))
	copy(before, in)
	Derive(Input{PRs: in, TrunkRefs: []string{trunkRef}})
	if !reflect.DeepEqual(in, before) {
		t.Errorf("Derive mutated its input:\ngot  %+v\nwant %+v", in, before)
	}
}

func TestGraphLookup(t *testing.T) {
	g := Derive(Input{
		PRs: []PR{
			openPR(1, "feature-a", trunkRef),
			openPR(4, "feature-b", "feature-a"),
			{Repo: otherRepo, Number: 4, Head: "feature-a", Base: trunkRef, State: "open"},
		},
		TrunkRefs: []string{trunkRef},
	})
	cases := []struct {
		name    string
		ref     Ref
		wantOK  bool
		wantRes Resolution
	}{
		{"first node", ref(1), true, ResolutionTrunk},
		{"last node of first repo", ref(4), true, ResolutionUpstream},
		{"node in another repo with a colliding number", Ref{Repo: otherRepo, Number: 4}, true, ResolutionTrunk},
		{"number not in the set", ref(2), false, ResolutionTrunk},
		{"number above every node in its repo", ref(99), false, ResolutionTrunk},
		{"repo sorting before every node", Ref{Repo: "aaa/absent", Number: 1}, false, ResolutionTrunk},
		// Sorts past the LAST node, so the binary search returns len(Nodes) —
		// the index that must not be read.
		{"repo sorting after every node", Ref{Repo: "zzzz/beyond", Number: 1}, false, ResolutionTrunk},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			n, ok := g.Lookup(tc.ref)
			if ok != tc.wantOK {
				t.Fatalf("Lookup(%s) ok = %v, want %v", tc.ref, ok, tc.wantOK)
			}
			if !ok {
				if !reflect.DeepEqual(n, Node{}) {
					t.Errorf("Lookup(%s) miss returned %+v, want the zero Node", tc.ref, n)
				}
				return
			}
			if n.Ref != tc.ref {
				t.Errorf("Lookup(%s) returned node for %s", tc.ref, n.Ref)
			}
			if n.Resolution != tc.wantRes {
				t.Errorf("Lookup(%s).Resolution = %s, want %s", tc.ref, n.Resolution, tc.wantRes)
			}
		})
	}
}

func TestIsOpen(t *testing.T) {
	cases := []struct {
		state string
		want  bool
	}{
		{"open", true},
		{"draft", true},
		{"OPEN", true},
		{"Draft", true},
		{"closed", false},
		{"merged", false},
		{"", false},
		{"reopened", false},
	}
	for _, tc := range cases {
		t.Run(tc.state, func(t *testing.T) {
			if got := IsOpen(tc.state); got != tc.want {
				t.Errorf("IsOpen(%q) = %v, want %v", tc.state, got, tc.want)
			}
		})
	}
}

func TestRefString(t *testing.T) {
	if got := ref(7).String(); got != "owner/repo#7" {
		t.Errorf("Ref.String() = %q, want %q", got, "owner/repo#7")
	}
}

func TestPRRef(t *testing.T) {
	p := openPR(9, "feature-a", trunkRef)
	if got := p.Ref(); got != ref(9) {
		t.Errorf("PR.Ref() = %s, want %s", got, ref(9))
	}
}

func TestResolutionString(t *testing.T) {
	cases := []struct {
		res  Resolution
		want string
	}{
		{ResolutionTrunk, "trunk"},
		{ResolutionUpstream, "upstream"},
		{ResolutionUnresolvable, "unresolvable"},
		{ResolutionForeign, "foreign"},
		{ResolutionSelf, "self"},
		{Resolution(42), "Resolution(42)"},
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			if got := tc.res.String(); got != tc.want {
				t.Errorf("Resolution.String() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDiagnosticKindString(t *testing.T) {
	cases := []struct {
		kind DiagnosticKind
		want string
	}{
		{DiagnosticCycle, "cycle"},
		{DiagnosticSelfBase, "self-base"},
		{DiagnosticForeignBase, "foreign-base"},
		{DiagnosticUnresolvableBase, "unresolvable-base"},
		{DiagnosticAmbiguousHead, "ambiguous-head"},
		{DiagnosticKind(42), "DiagnosticKind(42)"},
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			if got := tc.kind.String(); got != tc.want {
				t.Errorf("DiagnosticKind.String() = %q, want %q", got, tc.want)
			}
		})
	}
}

// The "pure function" property is meant to hold BY CONSTRUCTION: there is no
// store handle and no provider handle in scope to reach a side effect through.
// That only stays true if the package never imports one, so this guard scans the
// package's own non-test sources — the same source-scanning enforcement style as
// TestGHExecChokePoint in pkg/provider/vcs/github — and fails on any non-stdlib
// import. A stdlib path is one whose first segment carries no dot (no domain).
func TestPackageImportsStdlibOnly(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	scanned := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		scanned++
		for _, imp := range f.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			if strings.Contains(strings.SplitN(path, "/", 2)[0], ".") {
				t.Errorf("%s imports %q: prdeps must stay a pure whole-set pass with no store "+
					"or provider handle in scope, so it may import stdlib only", name, path)
			}
		}
	}
	if scanned == 0 {
		t.Fatal("scanned no non-test source files; the guard proved nothing")
	}
}

// dumpGraph renders a Graph one line per node and per diagnostic, so a table
// failure reads as a diff instead of a wall of struct syntax.
func dumpGraph(g Graph) string {
	var b strings.Builder
	for _, n := range g.Nodes {
		up := "-"
		if n.Resolution == ResolutionUpstream {
			up = n.Upstream.String()
		}
		fmt.Fprintf(&b, "  node %s open=%v res=%s up=%s depth=%d cyclic=%v down=%v\n",
			n.Ref, n.Open, n.Resolution, up, n.Depth, n.Cyclic, n.Downstream)
	}
	b.WriteString(dumpDiagnostics(g.Diagnostics))
	return b.String()
}

// dumpDiagnostics renders one diagnostic per line, for the same reason.
func dumpDiagnostics(diags []Diagnostic) string {
	var b strings.Builder
	for _, d := range diags {
		fmt.Fprintf(&b, "  diag %s refs=%v ref-name=%q\n", d.Kind, d.Refs, d.RefName)
	}
	return b.String()
}
