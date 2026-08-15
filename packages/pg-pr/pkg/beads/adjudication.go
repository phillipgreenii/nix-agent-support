// Package beads — duplicate ADJUDICATION (pg2-peyf0).
//
// Both arms of `pg-pr sync duplicates` count the beads sharing one identity in
// EVERY status, open and closed (pg2-0z8fw: closing a duplicate does not resolve
// it, so a closed pair must not silently leave the report). The consequence,
// measured on 2026-08-14, is that the pg2-xqwy6 reconcile closed all 201 excess
// beads with reasons naming their canonicals and the audit reported exactly the
// same counts afterwards. A number no remediation can move is not a regression
// signal, which is what pg2-0waxt needs it to be.
//
// The missing distinction is ADJUDICATION: a duplicate that has been resolved
// DELIBERATELY against a NAMED canonical, as opposed to one that merely happens
// to be closed. An adjudicated bead is retired from its group before the count;
// a bead that is only closed is still counted, so pg2-0z8fw's status-agnostic
// population is untouched.
//
// THE MARKER IS A `supersedes` bd DEPENDENCY EDGE between two beads that share
// one identity — never a match on the close reason. That is the same structural
// discriminator the operator chose on pg2-0waxt (ruling of 2026-08-14) for
// excluding process-feedback SUCCESSIONS, and it is chosen here for the same
// reason: an audit whose input is prose silently reclassifies its population
// when someone rewords a sentence. `bd dep add <a> <b> -t supersedes` is a
// first-class bd edge type and does NOT gate readiness, so the marker is a pure
// annotation with no effect on `bd ready` or any queue.
package beads

import "sort"

// adjudicationEdgeType is the bd dependency type that records an adjudication.
const adjudicationEdgeType = "supersedes"

// Dependency is one bd dependency edge as embedded in a `bd list --json` row.
// From is the bead CARRYING the edge (bd's `issue_id`); To is the bead it points
// AT (bd's `depends_on_id`).
type Dependency struct {
	From string
	To   string
	Type string
}

// dependenciesFromBD converts bd's wire shape into the view shape. Returns nil
// for an empty input so a bead with no edges carries no allocation.
func dependenciesFromBD(in []bdDependency) []Dependency {
	if len(in) == 0 {
		return nil
	}
	out := make([]Dependency, 0, len(in))
	for _, d := range in {
		out = append(out, Dependency{From: d.IssueID, To: d.DependsOnID, Type: d.Type})
	}
	return out
}

// adjudicatedIdentities partitions the ids of ONE duplicate group into the sets
// that recorded `supersedes` edges have tied together into a single adjudicated
// identity. Only components of two or more ids are returned; an id no edge
// touches is not in the result.
//
// BOTH ENDPOINTS MUST BE IN ids. An edge to a bead outside the group is ignored,
// so an unrelated `supersedes` annotation elsewhere in the workspace cannot make
// a genuine duplicate disappear from the report. The audit therefore honours an
// adjudication only when it names a bead the audit itself considers part of the
// same identity — which is the property a plain marker label would not have.
//
// DETECTION IS DIRECTION-AGNOSTIC, deliberately. bd's dependency-type names do
// not read consistently in one direction — `bd dep add <blocked> <blocker>
// -t blocks` reads object-to-subject while `bd dep add <child> <origin>
// -t discovered-from` reads subject-to-object — and the `supersedes` edges
// already present in these workspaces disagree with each other about which end
// is the survivor. An audit that keyed on one direction would silently keep
// counting a pair that HAD been adjudicated, the exact failure this exists to
// remove. Which member survives is therefore decided by the arm's own canonical
// rule (see dropAdjudicated), not by the edge's orientation, so a
// wrongly-oriented edge cannot make the audit advise closing the live bead.
func adjudicatedIdentities(ids []string, edges []Dependency) [][]string {
	if len(ids) < 2 || len(edges) == 0 {
		return nil
	}
	member := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		member[id] = struct{}{}
	}
	adj := map[string][]string{}
	for _, e := range edges {
		if e.Type != adjudicationEdgeType || e.From == e.To {
			continue
		}
		if _, ok := member[e.From]; !ok {
			continue
		}
		if _, ok := member[e.To]; !ok {
			continue
		}
		adj[e.From] = append(adj[e.From], e.To)
		adj[e.To] = append(adj[e.To], e.From)
	}
	if len(adj) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(adj))
	var out [][]string
	// Walk ids in the caller's order so the result is stable across runs.
	for _, id := range ids {
		if seen[id] || len(adj[id]) == 0 {
			continue
		}
		seen[id] = true
		component := []string{}
		queue := []string{id}
		for len(queue) > 0 {
			cur := queue[0]
			queue = queue[1:]
			component = append(component, cur)
			for _, next := range adj[cur] {
				if seen[next] {
					continue
				}
				seen[next] = true
				queue = append(queue, next)
			}
		}
		if len(component) < 2 {
			continue
		}
		sort.Strings(component)
		out = append(out, component)
	}
	return out
}

// dropAdjudicated returns the members of one duplicate group that are still
// OUTSTANDING: every adjudicated identity in the group is collapsed to the one
// member the arm's own canonical rule (pick) names as the survivor, and the rest
// are retired. Callers then run their unchanged canonical/excess computation on
// the result, so a group that collapses to fewer than two members reports no
// excess at all.
//
// Collapsing rather than dropping the whole group is what keeps a MIXED group
// honest: with members {C, E1, E2} where only E1 is adjudicated against C, E1 is
// retired and C, E2 survive — so the report still names one excess bead and
// still keeps C. Dropping any group that contained an edge would hide E2, which
// nobody has resolved.
//
// The group's canonical pick is unaffected by the collapse: the highest-ranked
// member of the group is by definition also the highest-ranked member of its own
// identity, so it is never the one retired.
//
// id, deps and pick adapt the two bead views (MergeRequest, ProcessingCycle);
// pick is the arm's existing canonical chooser, reused verbatim so "keep" cannot
// drift between the two code paths.
func dropAdjudicated[T any](group []T, id func(T) string, deps func(T) []Dependency, pick func([]T) *T) []T {
	if len(group) < 2 {
		return group
	}
	ids := make([]string, 0, len(group))
	byID := make(map[string]T, len(group))
	var edges []Dependency
	for _, m := range group {
		ids = append(ids, id(m))
		byID[id(m)] = m
		edges = append(edges, deps(m)...)
	}
	identities := adjudicatedIdentities(ids, edges)
	if len(identities) == 0 {
		return group
	}
	retired := map[string]bool{}
	for _, identity := range identities {
		members := make([]T, 0, len(identity))
		for _, mid := range identity {
			members = append(members, byID[mid])
		}
		keep := pick(members)
		for _, mid := range identity {
			if keep == nil || mid != id(*keep) {
				retired[mid] = true
			}
		}
	}
	out := make([]T, 0, len(group))
	for _, m := range group {
		if retired[id(m)] {
			continue
		}
		out = append(out, m)
	}
	return out
}
