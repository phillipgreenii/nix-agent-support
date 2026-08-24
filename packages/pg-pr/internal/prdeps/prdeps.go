// Package prdeps derives the PR-to-PR dependency graph implied by the
// BASE-BRANCH CHAIN: a PR whose base ref names another OPEN PR's head ref is
// stacked on top of that PR (bead pg2-4dz88.3.5).
//
// It is the fallback detector for stacks the code host does not model natively,
// and it needs no schema addition — `branch` (the head ref) and `base` are
// already columns on `pull_request` (internal/store's prColumns/scanPR), so
// every fact it consumes is already fetched on the GraphQL path AND on the REST
// fallback, and already persisted.
//
// # Why this is a whole-set pass of its own
//
// snapshot.Build (internal/snapshot/builder.go) is a SINGLE per-PR loop with no
// cross-PR phase, so a PR-to-PR relation cannot be computed inside it. Derive
// takes the WHOLE PR set instead and returns relations as a pure function:
// values in, values out. There is no store handle, no provider handle, no
// clock and no I/O anywhere in scope, which is what makes the purity
// STRUCTURAL rather than a test convention — there is nothing to reach a side
// effect through. That is the code-level expression of INV-READ-1 and
// INV-SYNC-1 in docs/behavior/invariants.md.
//
// # Deliberately out of scope (for Derive)
//
// Derive answers only the mechanically-decidable structural questions of the
// PURE base-branch chain, with no notion of the code host's native stack
// relation at all. Anything it cannot decide mechanically it REPORTS as a
// Diagnostic and resolves to ResolutionUnresolvable; it never invents a
// relation, and it never returns an error (every input set is describable).
//
// Incorporating the code host's NATIVE stack fields, the precedence between
// those and the base chain, what a MERGED middle PR does to the PRs stacked
// above it, and whether an unresolvable upstream is pulled into the retrieval
// set or left as a marker were left to the dependent leaf that merges the two
// relations (bead pg2-4dz88.3.6) — that leaf is DeriveWithNativeStack
// (native.go), a separate pure function rather than a change to Derive itself,
// specifically so Derive's own pinned base-chain semantics never have to move
// to accommodate those rulings.
package prdeps

import (
	"fmt"
	"sort"
	"strings"
)

// Ref identifies one pull request. (Repo, Number) is the store's primary key —
// `pull_request` is UNIQUE(repo, number) and Tx.UpsertPR conflicts on that pair
// — so the input set holds at most one PR per Ref.
type Ref struct {
	Repo   string
	Number int
}

// String renders a Ref in the `<repo>#<number>` form the store's own error
// strings use, so a diagnostic or a test failure names a PR the way the rest of
// pg-pr does.
func (r Ref) String() string { return fmt.Sprintf("%s#%d", r.Repo, r.Number) }

// PR is the minimal fact set the base-branch chain is computed from.
//
// It is deliberately a package-local input struct rather than store.PullRequest
// or api.PR: both carry dozens of fields this pass has no use for, and taking
// either would couple a pure graph derivation to a persistence or wire shape.
// Callers project into it (Head from store.PullRequest.Branch / api.PR.Branch,
// Base from .Base).
type PR struct {
	Repo   string
	Number int
	// Head is the PR's head ref name, UNQUALIFIED — GitHub's headRefName,
	// persisted as pull_request.branch.
	Head string
	// Base is the PR's base ref name — GitHub's baseRefName, persisted as
	// pull_request.base. A value carrying a ':' is repo-qualified and therefore
	// cross-repo; see ResolutionForeign.
	Base string
	// State is the PR's lifecycle state spelled the way internal/sync's
	// stateForPR spells it: "open" | "draft" | "closed" | "merged". Compared
	// case-insensitively, mirroring that function's own strings.ToLower.
	State string
	// NativeUpstreamHead is the head ref of the PR this one is stacked on
	// according to the code host's NATIVE stacked-PR relation (GitHub's
	// StackUpstreamHeadRefName, pkg/api.PR — bead pg2-4dz88.3.6). Empty means
	// this PR carries no native upstream signal: it isn't part of a native
	// stack, or it is the bottommost entry of one. DeriveWithNativeStack falls
	// back to the base-branch chain (Base) exactly when this is empty; Derive
	// itself never reads this field at all, which is what keeps every one of
	// its existing tests passing unchanged with the field left at its zero
	// value.
	NativeUpstreamHead string
}

// Ref returns the PR's identity.
func (p PR) Ref() Ref { return Ref{Repo: p.Repo, Number: p.Number} }

// openStates are the states in which a PR is still open, and therefore in which
// its head ref may serve as another PR's upstream. It is exactly the set
// store.ListOpenPRs selects on (`state IN ('open','draft')`): a DRAFT PR is
// open, so a stack can legitimately be built on top of one.
var openStates = map[string]struct{}{"open": {}, "draft": {}}

// IsOpen reports whether a state string names an open PR.
func IsOpen(state string) bool {
	_, ok := openStates[strings.ToLower(state)]
	return ok
}

// isMerged reports whether a state string names a merged PR, compared
// case-insensitively like IsOpen. Used only by DeriveWithNativeStack (native.go)
// to apply the merged-middle ruling — Derive itself has no notion of "merged"
// beyond "not open".
func isMerged(state string) bool {
	return strings.ToLower(state) == "merged"
}

// Resolution says what a PR's base ref resolved to. Every Node carries exactly
// one, so "this PR has no upstream" is a VALUE rather than a nil a caller might
// dereference into a phantom relation.
type Resolution int

const (
	// ResolutionTrunk: the base ref is one of the configured trunk refs, so the
	// PR sits at the bottom of a chain (or is standalone).
	ResolutionTrunk Resolution = iota
	// ResolutionUpstream: the base ref is the head ref of another OPEN PR in the
	// same repo and in the input set. Node.Upstream names it.
	ResolutionUpstream
	// ResolutionUnresolvable: the base ref is neither trunk nor any in-set open
	// PR's head. A deleted branch, an upstream outside the retrieval set and a
	// merged-away upstream are indistinguishable from the PR set alone, so they
	// share this one outcome; deciding what to DO about it (pull the upstream in,
	// or mark and move on) is the dependent leaf's ruling.
	ResolutionUnresolvable
	// ResolutionForeign: the base ref is repo-qualified, so it names a ref in a
	// different repository or fork. It is never matched against this repo's
	// heads, so no cross-repo edge can be invented.
	ResolutionForeign
	// ResolutionSelf: the base ref equals the PR's own head ref. Rejected — a PR
	// cannot be stacked on itself, so no self-edge is created.
	ResolutionSelf
	// ResolutionUpstreamOutOfSet: produced only by DeriveWithNativeStack (never
	// by Derive). The winning detection method (native, or the base-branch
	// chain when no native signal is present) names an upstream this input set
	// cannot make into a live edge — either no PR anywhere in the set heads
	// that ref at all, or one does but is neither open/draft nor merged (e.g.
	// closed without merging). Per the out-of-set-upstream ruling (bead
	// pg2-4dz88.3.6): no special fetch is made to pull the missing PR into the
	// set, but this is ALWAYS a defined, reported marker — never a silent
	// drop. Like ResolutionUnresolvable, several mechanically-indistinguishable
	// causes deliberately share this one value; RefName on the accompanying
	// DiagnosticUpstreamOutOfSet names the target ref that could not be
	// resolved.
	ResolutionUpstreamOutOfSet
	// ResolutionUnblocked: produced only by DeriveWithNativeStack. The winning
	// detection method names an upstream that IS in the input set and has
	// MERGED. Per the merged-middle ruling (bead pg2-4dz88.3.6), a merged
	// upstream no longer blocks anything stacked on it: this node reads as
	// having no live blocking dependency, and — deliberately — is NOT
	// re-pointed to the merged PR's own upstream. Node.MergedUpstream names the
	// merged PR for traceability; Node.Upstream stays the zero Ref (Upstream's
	// contract restricts it to ResolutionUpstream), and the chain walk
	// (Depth/Cyclic) treats this node as a bottom.
	ResolutionUnblocked
)

// String names the Resolution, for diagnostics and test failures.
func (r Resolution) String() string {
	switch r {
	case ResolutionTrunk:
		return "trunk"
	case ResolutionUpstream:
		return "upstream"
	case ResolutionUnresolvable:
		return "unresolvable"
	case ResolutionForeign:
		return "foreign"
	case ResolutionSelf:
		return "self"
	case ResolutionUpstreamOutOfSet:
		return "upstream-out-of-set"
	case ResolutionUnblocked:
		return "unblocked"
	default:
		return fmt.Sprintf("Resolution(%d)", int(r))
	}
}

// Node is one PR's place in the graph.
type Node struct {
	Ref Ref
	// Open mirrors IsOpen(PR.State). It is carried so the output is
	// self-contained: Downstream is the exact inverse of the resolved upstream
	// edges and therefore may name a closed or merged PR, and a caller filtering
	// on that should not have to re-join to the input.
	Open bool
	// Resolution is what this PR's base ref resolved to.
	Resolution Resolution
	// Upstream is the PR this one is stacked on. Meaningful ONLY when Resolution
	// is ResolutionUpstream; the zero Ref otherwise.
	Upstream Ref
	// MergedUpstream names the PR this node was natively- or base-chain-
	// stacked on, which has since MERGED. Meaningful ONLY when Resolution is
	// ResolutionUnblocked (set only by DeriveWithNativeStack); the zero Ref
	// otherwise. It is informational, not a live edge: Upstream stays the zero
	// Ref, and Depth/Cyclic/the chain walk all treat this node as a bottom —
	// the merged-middle ruling's "no longer blocked, not re-pointed further
	// up".
	MergedUpstream Ref
	// Downstream names the PRs stacked DIRECTLY on this one, sorted by repo then
	// number. Nil for the top of a chain. Each PR contributes at most one
	// upstream edge, so a PR can never appear twice here.
	Downstream []Ref
	// Depth is the number of upstream hops from this PR down to the bottom of
	// its chain: 0 for a PR that is itself at the bottom. A cyclic chain has no
	// bottom, so Depth is 0 whenever Cyclic is true.
	Depth int
	// Cyclic is true when this PR's upstream chain never reaches a bottom —
	// either the PR is a member of a cycle, or it is stacked (transitively) on
	// one. The members of each cycle are named by a DiagnosticCycle entry; a PR
	// that merely feeds INTO a cycle is Cyclic but is not one of them.
	Cyclic bool
}

// DiagnosticKind classifies a finding the derivation reports rather than
// silently absorbing.
type DiagnosticKind int

const (
	// DiagnosticCycle: a set of PRs whose base refs form an upstream cycle. A
	// cycle in real data means the upstream reading is wrong, so it is surfaced
	// instead of being flattened away.
	DiagnosticCycle DiagnosticKind = iota
	// DiagnosticSelfBase: a PR whose base ref is its own head ref.
	DiagnosticSelfBase
	// DiagnosticForeignBase: a PR whose base ref is repo-qualified.
	DiagnosticForeignBase
	// DiagnosticUnresolvableBase: a PR whose base ref matched neither trunk nor
	// any in-set open PR's head.
	DiagnosticUnresolvableBase
	// DiagnosticAmbiguousHead: several open PRs in one repo share a head ref, so
	// "the PR that owns this ref" needed a tie-break. The tie-break is applied
	// (lowest number wins) AND reported, because the ambiguity is itself a data
	// problem.
	DiagnosticAmbiguousHead
	// DiagnosticUpstreamOutOfSet: produced only by DeriveWithNativeStack. A PR
	// whose native-or-base-chain-resolved upstream target could not be turned
	// into a live edge — absent from the input set entirely, or present but
	// not open/draft/merged.
	DiagnosticUpstreamOutOfSet
)

// String names the DiagnosticKind, for logs and test failures.
func (k DiagnosticKind) String() string {
	switch k {
	case DiagnosticCycle:
		return "cycle"
	case DiagnosticSelfBase:
		return "self-base"
	case DiagnosticForeignBase:
		return "foreign-base"
	case DiagnosticUnresolvableBase:
		return "unresolvable-base"
	case DiagnosticAmbiguousHead:
		return "ambiguous-head"
	case DiagnosticUpstreamOutOfSet:
		return "upstream-out-of-set"
	default:
		return fmt.Sprintf("DiagnosticKind(%d)", int(k))
	}
}

// Diagnostic is one reported finding. Every Diagnostic names at least one PR.
type Diagnostic struct {
	Kind DiagnosticKind
	// Refs are the PRs involved: every member of the cycle for DiagnosticCycle,
	// every PR sharing the ref for DiagnosticAmbiguousHead (the winner first),
	// and the single offending PR otherwise.
	Refs []Ref
	// RefName is the ref NAME at issue — the unresolvable, foreign or
	// self-referential base ref, or the head ref several PRs share. Empty for
	// DiagnosticCycle, whose members are themselves the finding.
	RefName string
}

// Input is the whole-set input to Derive.
type Input struct {
	PRs []PR
	// TrunkRefs are the ref names that mean "bottom of a chain": a PR based on
	// one is not stacked on anything. This is CONFIG and never a literal — the
	// integration branch is a per-repo convention, so hardcoding one repo's
	// would bake it in for all of them. The set is a UNION across repos, the
	// same shape snapshot.BuilderInput.WatchLabels uses. An empty set is valid
	// and simply means no ref is recognised as trunk, so a base ref no open PR
	// heads reads as ResolutionUnresolvable.
	TrunkRefs []string
}

// Graph is the derived whole-set result.
type Graph struct {
	// Nodes holds one entry per input PR, sorted by repo then number. That pair
	// is unique (see Ref), so the order is TOTAL, stable, and independent of the
	// input order — the same ordering habit as
	// beads.FindDuplicateMergeRequests' report.
	Nodes []Node
	// Diagnostics are the derivation's findings, sorted by kind then by the
	// first PR involved then by ref name. Empty for a clean set.
	Diagnostics []Diagnostic
}

// Lookup returns the node for ref. The bool is false when ref is not in the
// set, so a caller never mistakes a zero Node for a real one. It binary-searches
// Nodes, which Derive guarantees is sorted.
func (g Graph) Lookup(ref Ref) (Node, bool) {
	i := sort.Search(len(g.Nodes), func(i int) bool { return !refLess(g.Nodes[i].Ref, ref) })
	if i < len(g.Nodes) && g.Nodes[i].Ref == ref {
		return g.Nodes[i], true
	}
	return Node{}, false
}

// headKey is a head ref scoped to its repository. Scoping the index by repo is
// what makes a cross-repo edge structurally impossible: two PRs in different
// repos can carry identical ref names and still never match.
type headKey struct {
	repo string
	ref  string
}

// Derive computes the base-branch-chain dependency graph over the whole PR set.
// It never mutates in, never errors, and is deterministic: the same input set
// yields the same Graph regardless of the order the PRs arrive in.
func Derive(in Input) Graph {
	prs := make([]PR, len(in.PRs))
	copy(prs, in.PRs)
	sort.Slice(prs, func(i, j int) bool { return refLess(prs[i].Ref(), prs[j].Ref()) })

	trunk := make(map[string]struct{}, len(in.TrunkRefs))
	for _, r := range in.TrunkRefs {
		trunk[r] = struct{}{}
	}

	// sharing maps a repo-scoped head ref to every OPEN PR that heads it, in
	// (repo, number) order. Only open PRs are indexed, because only an open PR
	// can be an upstream. An EMPTY head ref is skipped: it identifies nothing, so
	// indexing it would both invite a phantom match and report two headless PRs
	// as an ambiguous head ref.
	sharing := make(map[headKey][]Ref, len(prs))
	for _, p := range prs {
		if p.Head == "" || !IsOpen(p.State) {
			continue
		}
		k := headKey{repo: p.Repo, ref: p.Head}
		sharing[k] = append(sharing[k], p.Ref())
	}

	var diags []Diagnostic
	for k, refs := range sharing {
		if len(refs) > 1 {
			diags = append(diags, Diagnostic{Kind: DiagnosticAmbiguousHead, Refs: refs, RefName: k.ref})
		}
	}

	nodes := make([]Node, 0, len(prs))
	upstream := make(map[Ref]Ref, len(prs))
	downstream := make(map[Ref][]Ref, len(prs))
	for _, p := range prs {
		id := p.Ref()
		n := Node{Ref: id, Open: IsOpen(p.State)}
		// The order of these arms is the resolution PRECEDENCE, and each arm
		// short-circuits the ones below it:
		//
		//  1. an EMPTY base ref names nothing, so it can never resolve — checked
		//     first so it cannot be read as trunk or as a self-reference;
		//  2. TRUNK wins over the head index: a ref that is the integration
		//     branch is the bottom of a chain by definition, so an open PR
		//     happening to head a trunk-named ref is a data anomaly, not an
		//     upstream;
		//  3. a repo-QUALIFIED base is foreign and is never looked up;
		//  4. a base equal to the PR's own head is a SELF reference. Because
		//     this arm rejects it, the lookup below can only ever return a
		//     DIFFERENT PR — the index maps a ref to the PR that heads it, so a
		//     hit on p itself would require p.Base == p.Head.
		switch {
		case p.Base == "":
			n.Resolution = ResolutionUnresolvable
			diags = append(diags, Diagnostic{Kind: DiagnosticUnresolvableBase, Refs: []Ref{id}, RefName: p.Base})
		case inSet(trunk, p.Base):
			n.Resolution = ResolutionTrunk
		case strings.Contains(p.Base, ":"):
			// A repo-qualified base ref (`owner:branch`, `owner/repo:branch`) can
			// only have come from a cross-repo or fork source: GitHub's
			// baseRefName is bare for a same-repo base. So the qualifier is NOT
			// stripped and re-matched even when it names this PR's own repo —
			// that would be inventing a normalization for a form the provider
			// never emits, and the whole point of this arm is that no cross-repo
			// edge is ever invented.
			n.Resolution = ResolutionForeign
			diags = append(diags, Diagnostic{Kind: DiagnosticForeignBase, Refs: []Ref{id}, RefName: p.Base})
		case p.Base == p.Head:
			n.Resolution = ResolutionSelf
			diags = append(diags, Diagnostic{Kind: DiagnosticSelfBase, Refs: []Ref{id}, RefName: p.Base})
		default:
			// The winner among PRs sharing a head ref is the FIRST entry, which
			// is the LOWEST number because prs is sorted — the explicit
			// lowest-id tie-break pkg/beads/mergerequest.go's
			// pickCanonicalMergeRequest ends on, so the pick never depends on
			// the order the PRs arrived in.
			owners := sharing[headKey{repo: p.Repo, ref: p.Base}]
			if len(owners) == 0 {
				n.Resolution = ResolutionUnresolvable
				diags = append(diags, Diagnostic{Kind: DiagnosticUnresolvableBase, Refs: []Ref{id}, RefName: p.Base})
				break
			}
			up := owners[0]
			n.Resolution = ResolutionUpstream
			n.Upstream = up
			upstream[id] = up
			// prs is iterated in (repo, number) order, so each downstream list is
			// built already sorted and needs no re-sort.
			downstream[up] = append(downstream[up], id)
		}
		nodes = append(nodes, n)
	}

	order := make([]Ref, len(nodes))
	for i, n := range nodes {
		order[i] = n.Ref
	}
	chains, cycles := resolveChains(order, upstream)
	for i := range nodes {
		c := chains[nodes[i].Ref]
		nodes[i].Depth = c.depth
		nodes[i].Cyclic = c.cyclic
		nodes[i].Downstream = downstream[nodes[i].Ref]
	}
	for _, members := range cycles {
		diags = append(diags, Diagnostic{Kind: DiagnosticCycle, Refs: members})
	}

	// Diagnostics are emitted across three phases (head indexing, base
	// resolution, chain walking), and the first of those iterates a map, so this
	// final sort is what makes the slice deterministic. (Kind, first PR named) is
	// already a TOTAL key: a PR has one base ref, heads one ref and belongs to at
	// most one cycle, so no two diagnostics of the same kind can name the same PR
	// first — hence no further tie-break exists to write.
	sort.Slice(diags, func(i, j int) bool {
		a, b := diags[i], diags[j]
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return refLess(a.Refs[0], b.Refs[0])
	})
	return Graph{Nodes: nodes, Diagnostics: diags}
}

func inSet(set map[string]struct{}, s string) bool {
	_, ok := set[s]
	return ok
}

// refLess is the total order over Refs: repo, then number. Same shape as
// beads.FindDuplicateMergeRequests' repo-then-PR-number report ordering.
func refLess(a, b Ref) bool {
	if a.Repo != b.Repo {
		return a.Repo < b.Repo
	}
	return a.Number < b.Number
}

// chainInfo is one PR's resolved position in its chain.
type chainInfo struct {
	depth  int
	cyclic bool
}

// resolveChains computes every PR's chain depth and cyclicity, and reports each
// upstream cycle exactly once.
//
// The upstream relation is FUNCTIONAL — a PR has one base ref, hence at most one
// upstream — so each walk follows a single path and every cycle is a simple
// loop. Termination rests on TWO explicit visited sets, not on a recursion bound
// or a hop limit: `pos` holds the PRs of the walk currently in progress, so
// meeting one again IS the cycle and its position in the path is known exactly;
// `info` holds every PR an earlier walk already settled, so no PR is walked
// twice. The walk is iterative and nothing here recurses, so a cycle can neither
// hang it nor blow a stack.
func resolveChains(order []Ref, upstream map[Ref]Ref) (map[Ref]chainInfo, [][]Ref) {
	info := make(map[Ref]chainInfo, len(order))
	var cycles [][]Ref
	for _, start := range order {
		if _, done := info[start]; done {
			continue
		}
		var path []Ref
		pos := make(map[Ref]int)
		// carry is the resolved info of the PR just BEYOND the path's tail; the
		// unwind below turns it into each path member's own info.
		var carry chainInfo
		cur := start
		for {
			if idx, onPath := pos[cur]; onPath {
				// cur is already on the path being walked: path[idx:] is the
				// cycle, and path[:idx] feeds into it, so EVERY PR on this path
				// has a chain with no bottom.
				cycles = append(cycles, rotateCycle(path[idx:]))
				for _, r := range path {
					info[r] = chainInfo{cyclic: true}
				}
				path = nil // fully settled here; nothing left to unwind
				break
			}
			if c, done := info[cur]; done {
				carry = c
				break
			}
			up, ok := upstream[cur]
			if !ok {
				// cur is the bottom of the chain: depth 0, not cyclic.
				carry = chainInfo{}
				info[cur] = carry
				break
			}
			pos[cur] = len(path)
			path = append(path, cur)
			cur = up
		}
		for i := len(path) - 1; i >= 0; i-- {
			if carry.cyclic {
				carry = chainInfo{cyclic: true}
			} else {
				carry = chainInfo{depth: carry.depth + 1}
			}
			info[path[i]] = carry
		}
	}
	return info, cycles
}

// rotateCycle rotates a cycle's member list to start at its lowest Ref, keeping
// the upstream order from there. The walk discovers a cycle from whichever
// member it happened to reach first — which, for a cycle entered from OUTSIDE,
// is not the lowest member — so rotating makes the reported list depend only on
// the cycle itself.
func rotateCycle(members []Ref) []Ref {
	lowest := 0
	for i := 1; i < len(members); i++ {
		if refLess(members[i], members[lowest]) {
			lowest = i
		}
	}
	out := make([]Ref, 0, len(members))
	out = append(out, members[lowest:]...)
	out = append(out, members[:lowest]...)
	return out
}
