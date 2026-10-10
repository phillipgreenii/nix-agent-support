package focus

import (
	"sort"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/classify"
)

// issueTypeEpic is the issue_type value of an epic (compared ignoring case).
const issueTypeEpic = "epic"

// depTypeParentChild is the type of a dependency edge that names a child.
const depTypeParentChild = "parent-child"

// EpicEntry is one row of the trailing "epics with children in play" block.
// Total is the number of OPEN descendants of the epic, counted transitively
// through nested epics (a descendant counts when it is active, not hidden and
// in an open status); InPlay is how many of those are keys in
// Inputs.PlanKeys.
type EpicEntry struct {
	Key    Key
	InPlay int
	Total  int
}

// SlotResult is the answer of ApplySlotRule.
//
//   - Slotted are the ranked candidates that keep a slot, in their ranked
//     order: every candidate except an epic that has an open direct child.
//   - Suppressed maps each such epic to the open direct child that covers it:
//     the first one, in (kind, key) order, that is itself a candidate; when
//     no open child is a candidate, the first open child.
//   - EpicsInPlay is the trailing block, sorted by (kind, key) and not
//     counted against the cap.
//   - UnknownChildren maps an epic to the number of children its stored
//     issue_show.deps names with a parent-child edge whose id has no entity
//     row. Such a child is unknown, not absent; the show coverage header
//     prints the count, and the epic is listed as it would be with no known
//     child. Only epics that are ranked or in the block appear, and an edge
//     to the epic's own parent is not a child.
type SlotResult struct {
	Slotted         []Candidate
	Suppressed      map[Key]Key
	EpicsInPlay     []EpicEntry
	UnknownChildren map[Key]int
}

// epicIndex is the parent/child structure of the stored issues, built once
// per call from issue_show.parent (the only parent field consulted).
type epicIndex struct {
	in       Inputs
	a        *analysis
	children map[Key][]Key // parent -> issue entities naming it as parent, in (kind, key) order
}

func newEpicIndex(in Inputs) *epicIndex {
	x := &epicIndex{in: in, a: analyze(in), children: map[Key][]Key{}}
	for _, v := range x.a.sorted {
		if v.key.Type != entityTypeIssue || v.parent == "" {
			continue
		}
		p := Key{entityTypeIssue, v.parent}
		x.children[p] = append(x.children[p], v.key)
	}
	return x
}

// isEpic: an issue is an epic when its issue_type equals epic ignoring case,
// or any other issue names it as parent.
func (x *epicIndex) isEpic(k Key) bool {
	v := x.a.views[k]
	if v == nil || k.Type != entityTypeIssue {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(v.issueType), issueTypeEpic) || len(x.children[k]) > 0
}

// live reports an entity that decisions may read: stored, active, not hidden.
func (x *epicIndex) live(k Key) bool {
	e, ok := x.in.Entities[k]
	return ok && x.a.views[k] != nil && !e.Inactive && !hidden(x.in.Annotations[k])
}

// open reports an open entity under the slot rule: live, and for a bead
// open, in_progress or blocked (the statuses the open-beads bulk query
// lists; deferred and closed are not open), for a Jira issue not in a done
// status (category first, state-name fallback).
func (x *epicIndex) open(k Key) bool {
	if !x.live(k) {
		return false
	}
	v := x.a.views[k]
	switch v.kind {
	case KindBead:
		return beadStatusOpenish(v.state)
	case KindJira:
		kind, _ := classify.IssueStatusKind([]byte(x.in.Entities[k].Facts))
		return kind != classify.IssueStatusDone
	}
	return false
}

func (x *epicIndex) openChildren(k Key) []Key {
	var out []Key
	for _, c := range x.children[k] {
		if x.open(c) {
			out = append(out, c)
		}
	}
	return out
}

// descendants counts the open descendants of an epic and how many of them
// are in the plan, through nested epics. A child that is live but not open
// is traversed (its own children may be open) and not counted.
func (x *epicIndex) descendants(k Key) (inPlay, total int) {
	seen := map[Key]bool{k: true}
	var walk func(Key)
	walk = func(p Key) {
		for _, c := range x.children[p] {
			if seen[c] || !x.live(c) {
				continue
			}
			seen[c] = true
			if x.open(c) {
				total++
				if x.in.PlanKeys[c] {
					inPlay++
				}
			}
			walk(c)
		}
	}
	walk(k)
	return inPlay, total
}

func (x *epicIndex) unknownChildren(k Key) int {
	v := x.a.views[k]
	if v == nil {
		return 0
	}
	n := 0
	for _, d := range v.deps {
		if !strings.EqualFold(strings.TrimSpace(d.Type), depTypeParentChild) || d.ID == "" || d.ID == v.parent {
			continue
		}
		if _, ok := x.in.Entities[Key{entityTypeIssue, d.ID}]; !ok {
			n++
		}
	}
	return n
}

// IsEpic reports whether key is an epic: an issue whose issue_type equals
// epic ignoring case, or that any other stored issue names as its parent.
func IsEpic(in Inputs, key Key) bool { return newEpicIndex(in).isEpic(key) }

// OpenChildren returns the open DIRECT children of an epic (the issues whose
// parent field names it), in (kind, key) order. "Open" is the slot rule's:
// the child is stored, active and not hidden, and is an open, in_progress or
// blocked bead or a Jira issue not in a done status; a deferred or closed
// bead, an inactive entity and a hidden entity are not open children.
func OpenChildren(in Inputs, epicKey Key) []Key {
	return newEpicIndex(in).openChildren(epicKey)
}

// ApplySlotRule applies the epic slot rule to the ranked candidates, BEFORE
// the cap line is counted: an epic and its children never use more than one
// slot between them. A child that is a candidate keeps its own slot; an epic
// is not in the ranked slots while it has any open direct child (an open
// child that is not a candidate counts, a hidden one does not); an epic with
// no open child stays a candidate in its own right. The rule holds for bd
// epics and Jira epics alike (the connector maps a Jira child's parent).
//
// The trailing block lists epics with children in play by ONE membership
// rule: an epic is listed when it has an open direct child that is a
// candidate, or when it is itself a candidate suppressed by an open child,
// and transitively through nested epics (an epic with an open direct child
// that is a listed epic is listed). An epic none of whose open children is a
// candidate and which is not itself a candidate is in neither place.
//
// ApplySlotRule is pure and reads no clock; it emits no OpenTelemetry or
// Prometheus signal and logs nothing.
func ApplySlotRule(in Inputs, ranked []Candidate) SlotResult {
	x := newEpicIndex(in)
	isRanked := map[Key]bool{}
	for _, c := range ranked {
		isRanked[c.Key] = true
	}
	res := SlotResult{Suppressed: map[Key]Key{}, UnknownChildren: map[Key]int{}}

	for _, c := range ranked {
		if !x.isEpic(c.Key) {
			res.Slotted = append(res.Slotted, c)
			continue
		}
		open := x.openChildren(c.Key)
		if len(open) == 0 {
			res.Slotted = append(res.Slotted, c)
			continue
		}
		cover := open[0]
		for _, k := range open {
			if isRanked[k] {
				cover = k
				break
			}
		}
		res.Suppressed[c.Key] = cover
	}

	// Block membership, transitive through nested epics (cycle-safe: a
	// stored parent cycle is corrupt data, and an epic met again while it is
	// being decided is read as not listed).
	state := map[Key]int{} // 0 unvisited, 1 in progress, 2 listed, 3 not listed
	var listed func(Key) bool
	listed = func(e Key) bool {
		switch state[e] {
		case 2:
			return true
		case 1, 3:
			return false
		}
		state[e] = 1
		open := x.openChildren(e)
		ok := len(open) > 0 && isRanked[e]
		for _, c := range open {
			if ok {
				break
			}
			ok = isRanked[c] || (x.isEpic(c) && listed(c))
		}
		if ok {
			state[e] = 2
		} else {
			state[e] = 3
		}
		return ok
	}
	var epics []Key
	for _, v := range x.a.sorted {
		if x.isEpic(v.key) && x.live(v.key) && listed(v.key) {
			epics = append(epics, v.key)
		}
	}
	sort.Slice(epics, func(i, j int) bool { return candLess(x.a.views[epics[i]], x.a.views[epics[j]]) })
	for _, e := range epics {
		inPlay, total := x.descendants(e)
		res.EpicsInPlay = append(res.EpicsInPlay, EpicEntry{Key: e, InPlay: inPlay, Total: total})
	}

	unknown := func(k Key) {
		if n := x.unknownChildren(k); n > 0 && x.isEpic(k) {
			res.UnknownChildren[k] = n
		}
	}
	for _, c := range ranked {
		unknown(c.Key)
	}
	for _, e := range epics {
		unknown(e)
	}
	return res
}
