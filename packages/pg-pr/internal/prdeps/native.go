package prdeps

import (
	"sort"
	"strings"
)

// DeriveWithNativeStack computes the same kind of whole-set dependency graph
// as Derive, but layers the code host's NATIVE stacked-PR relation
// (PR.NativeUpstreamHead) on top of the base-branch chain as a fallback, and
// resolves the two product questions the pure base-chain relation
// deliberately left undecided (see the package doc's "Deliberately out of
// scope"), per the operator rulings recorded against bead pg2-4dz88.3.6:
//
//   - Detection precedence: NativeUpstreamHead wins whenever it is non-empty;
//     Base is consulted only when it is empty. PR-body text is never a
//     detection source at all — PR carries no field a body's text could
//     occupy, so there is no code path through which "Depends on #5"-shaped
//     prose could ever reach a Resolution (see TestPRHasNoBodyField).
//   - Merged-middle: when the winning target names an upstream PR that HAS
//     merged, this PR reads as ResolutionUnblocked — no live blocking
//     dependency, and it is NOT re-pointed to that PR's own upstream.
//   - Out-of-set upstream: when the winning target names no PR this input set
//     can turn into a live edge (absent entirely, or present but neither
//     open/draft nor merged), this PR reads as ResolutionUpstreamOutOfSet — a
//     defined, reported marker. No special fetch is ever made to pull a
//     missing upstream into the set.
//
// Like Derive, it is a pure function — values in, values out, no I/O,
// deterministic regardless of input order — and it is a SEPARATE algorithm
// from Derive rather than a wrapper around it or a modification of it, so
// that Derive's own pinned base-chain-only tests never have to change to
// accommodate these two policy calls.
func DeriveWithNativeStack(in Input) Graph {
	prs := make([]PR, len(in.PRs))
	copy(prs, in.PRs)
	sort.Slice(prs, func(i, j int) bool { return refLess(prs[i].Ref(), prs[j].Ref()) })

	trunk := make(map[string]struct{}, len(in.TrunkRefs))
	for _, r := range in.TrunkRefs {
		trunk[r] = struct{}{}
	}

	// byHead indexes EVERY PR (any state), unlike Derive's open-only `sharing`
	// index: a merged PR must be findable (to detect merged-middle) and a
	// closed-without-merge PR must be findable too (so "matched but not a live
	// candidate" can be told apart from "absent from the set entirely" — both
	// still resolve to ResolutionUpstreamOutOfSet, but for the RIGHT reason,
	// and a future caller inspecting Diagnostics sees the correct RefName
	// either way). An EMPTY head ref is skipped, for the same reason Derive
	// skips one: it identifies nothing, and indexing it would invite a
	// phantom match.
	byHead := make(map[headKey][]PR, len(prs))
	for _, p := range prs {
		if p.Head == "" {
			continue
		}
		k := headKey{repo: p.Repo, ref: p.Head}
		byHead[k] = append(byHead[k], p)
	}

	// Ambiguity is checked across ALL states here, wider than Derive's
	// open-only check: because a merged PR is now itself a valid resolution
	// target (for the merged-middle ruling), two PRs — of any state — sharing
	// a head ref is a data problem worth reporting regardless of which of them
	// happens to be open.
	var diags []Diagnostic
	for k, owners := range byHead {
		if len(owners) > 1 {
			refs := make([]Ref, len(owners))
			for i, o := range owners {
				refs[i] = o.Ref()
			}
			diags = append(diags, Diagnostic{Kind: DiagnosticAmbiguousHead, Refs: refs, RefName: k.ref})
		}
	}

	nodes := make([]Node, 0, len(prs))
	// walkUpstream feeds the chain walk (Depth/Cyclic) and holds ONLY
	// ResolutionUpstream edges: a ResolutionUnblocked node is deliberately
	// absent from it, which is what makes resolveChains treat it as a chain
	// bottom instead of walking through the merged PR to ITS upstream.
	walkUpstream := make(map[Ref]Ref, len(prs))
	// matchedDownstream holds both ResolutionUpstream and ResolutionUnblocked
	// edges: Downstream is a structural "what points at me" report, and a
	// merged PR still structurally had something stacked on it even though
	// that edge no longer blocks anything.
	matchedDownstream := make(map[Ref][]Ref, len(prs))
	for _, p := range prs {
		id := p.Ref()
		n := Node{Ref: id, Open: IsOpen(p.State)}

		// Detection precedence: native wins whenever present.
		usingNative := p.NativeUpstreamHead != ""
		target := p.NativeUpstreamHead
		if !usingNative {
			target = p.Base
		}

		switch {
		case target == "":
			// No native signal and an empty base: there is no name to blame at
			// all, which is a different (and strictly less informative) case
			// than "a named target we could not resolve" below — kept as its
			// own outcome so the two stay distinguishable.
			n.Resolution = ResolutionUnresolvable
			diags = append(diags, Diagnostic{Kind: DiagnosticUnresolvableBase, Refs: []Ref{id}, RefName: target})
		case !usingNative && inSet(trunk, target):
			// Trunk is a BASE-branch concept only: GitHub's native upstream
			// head ref never names the integration branch (a bottommost native
			// stack entry simply carries no NativeUpstreamHead at all), so this
			// arm never fires for a native-sourced target.
			n.Resolution = ResolutionTrunk
		case strings.Contains(target, ":"):
			n.Resolution = ResolutionForeign
			diags = append(diags, Diagnostic{Kind: DiagnosticForeignBase, Refs: []Ref{id}, RefName: target})
		case target == p.Head:
			n.Resolution = ResolutionSelf
			diags = append(diags, Diagnostic{Kind: DiagnosticSelfBase, Refs: []Ref{id}, RefName: target})
		default:
			owners := byHead[headKey{repo: p.Repo, ref: target}]
			switch {
			case len(owners) == 0:
				// Named, but no PR anywhere in the set heads it: the classic
				// out-of-set-upstream case. Per the ruling, marker only — no
				// special fetch is made to go get it.
				n.Resolution = ResolutionUpstreamOutOfSet
				diags = append(diags, Diagnostic{Kind: DiagnosticUpstreamOutOfSet, Refs: []Ref{id}, RefName: target})
			case IsOpen(owners[0].State):
				// owners is built by iterating the already (repo, number)-sorted
				// prs, so owners[0] is the lowest-numbered sharer — the same
				// tie-break Derive uses, now applied across every state rather
				// than open-only.
				winner := owners[0].Ref()
				n.Resolution = ResolutionUpstream
				n.Upstream = winner
				walkUpstream[id] = winner
				matchedDownstream[winner] = append(matchedDownstream[winner], id)
			case isMerged(owners[0].State):
				winner := owners[0].Ref()
				n.Resolution = ResolutionUnblocked
				n.MergedUpstream = winner
				matchedDownstream[winner] = append(matchedDownstream[winner], id)
			default:
				// Matched a real PR in the set, but one that is neither a live
				// candidate (open/draft) nor merged — e.g. closed without
				// merging. No ruling authorizes treating this as unblocked, so
				// it shares the same marker-only outcome as a genuinely absent
				// upstream.
				n.Resolution = ResolutionUpstreamOutOfSet
				diags = append(diags, Diagnostic{Kind: DiagnosticUpstreamOutOfSet, Refs: []Ref{id}, RefName: target})
			}
		}
		nodes = append(nodes, n)
	}

	order := make([]Ref, len(nodes))
	for i, n := range nodes {
		order[i] = n.Ref
	}
	chains, cycles := resolveChains(order, walkUpstream)
	for i := range nodes {
		c := chains[nodes[i].Ref]
		nodes[i].Depth = c.depth
		nodes[i].Cyclic = c.cyclic
		nodes[i].Downstream = matchedDownstream[nodes[i].Ref]
	}
	for _, members := range cycles {
		diags = append(diags, Diagnostic{Kind: DiagnosticCycle, Refs: members})
	}

	// Same total order as Derive's own final sort: (Kind, first PR named).
	sort.Slice(diags, func(i, j int) bool {
		a, b := diags[i], diags[j]
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return refLess(a.Refs[0], b.Refs[0])
	})
	return Graph{Nodes: nodes, Diagnostics: diags}
}
