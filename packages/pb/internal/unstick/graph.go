package unstick

import "sort"

// Graph is the dependency structure of an export, built once from []Row with
// adjacency maps so every query is O(degree) (no per-target full scans).
//
// Only DepBlocks and DepParentChild edges are indexed; other edge types are
// ignored. Every slice returned by a Graph method is sorted by id, freshly
// allocated (callers may keep or mutate it) and free of duplicates. Edges that
// reference an id absent from the export are kept in the adjacency lists (the
// id is simply not Has) and are reported by Unknown.
type Graph struct {
	rows       []Row
	byID       map[string]int
	blockers   map[string][]string // id -> ids it is blocked by
	blockerDep map[string][]string // id -> ids it blocks
	parent     map[string]string   // child -> parent
	children   map[string][]string // parent -> children
	unknown    map[string][]string // id -> referenced ids absent from export
}

// NewGraph indexes rows. A duplicate row id keeps the first occurrence.
// Duplicate edges are collapsed. A bead with several parent-child edges has the
// lexicographically smallest parent as its Parent.
func NewGraph(rows []Row) *Graph {
	g := &Graph{
		rows:       rows,
		byID:       Index(rows),
		blockers:   map[string][]string{},
		blockerDep: map[string][]string{},
		parent:     map[string]string{},
		children:   map[string][]string{},
		unknown:    map[string][]string{},
	}
	seen := map[[3]string]bool{}
	unk := map[string]map[string]bool{}
	note := func(owner, ref string) {
		if _, ok := g.byID[ref]; ok {
			return
		}
		if unk[owner] == nil {
			unk[owner] = map[string]bool{}
		}
		unk[owner][ref] = true
	}
	for i, r := range rows {
		if g.byID[r.ID] != i {
			continue
		}
		for _, d := range r.Dependencies {
			if d.Type != DepBlocks && d.Type != DepParentChild {
				continue
			}
			k := [3]string{d.IssueID, d.DependsOnID, d.Type}
			if seen[k] {
				continue
			}
			seen[k] = true
			note(d.IssueID, d.DependsOnID)
			note(d.DependsOnID, d.IssueID)
			if d.Type == DepBlocks {
				g.blockers[d.IssueID] = append(g.blockers[d.IssueID], d.DependsOnID)
				g.blockerDep[d.DependsOnID] = append(g.blockerDep[d.DependsOnID], d.IssueID)
			} else {
				if p, ok := g.parent[d.IssueID]; !ok || d.DependsOnID < p {
					g.parent[d.IssueID] = d.DependsOnID
				}
				g.children[d.DependsOnID] = append(g.children[d.DependsOnID], d.IssueID)
			}
		}
	}
	for _, m := range []map[string][]string{g.blockers, g.blockerDep, g.children} {
		for k := range m {
			sort.Strings(m[k])
		}
	}
	for id, set := range unk {
		for ref := range set {
			g.unknown[id] = append(g.unknown[id], ref)
		}
		sort.Strings(g.unknown[id])
	}
	return g
}

// Has reports whether id is a row of the export.
func (g *Graph) Has(id string) bool { _, ok := g.byID[id]; return ok }

// Row returns the row for id.
func (g *Graph) Row(id string) (Row, bool) {
	i, ok := g.byID[id]
	if !ok {
		return Row{}, false
	}
	return g.rows[i], true
}

// IDs returns every row id, sorted.
func (g *Graph) IDs() []string {
	out := make([]string, 0, len(g.byID))
	for id := range g.byID {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func clone(s []string) []string { return append([]string(nil), s...) }

// Blockers returns the ids that id depends on via "blocks" edges (including
// ids absent from the export).
func (g *Graph) Blockers(id string) []string { return clone(g.blockers[id]) }

// BlockerDependents returns the ids that depend on id via "blocks" edges.
func (g *Graph) BlockerDependents(id string) []string { return clone(g.blockerDep[id]) }

// Parent returns id's parent via a parent-child edge (the child is the edge's
// issue_id). The parent may be absent from the export.
func (g *Graph) Parent(id string) (string, bool) { p, ok := g.parent[id]; return p, ok }

// Children returns the direct children of id.
func (g *Graph) Children(id string) []string { return clone(g.children[id]) }

// Ancestors returns every transitive parent of id (excluding id itself, even
// in a parent cycle), sorted.
func (g *Graph) Ancestors(id string) []string {
	seen := map[string]bool{id: true}
	var out []string
	for p, ok := g.parent[id]; ok && !seen[p]; p, ok = g.parent[p] {
		seen[p] = true
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// Descendants returns every transitive child of id (excluding id itself, even
// in a parent cycle), sorted.
func (g *Graph) Descendants(id string) []string {
	seen := map[string]bool{id: true}
	queue := []string{id}
	var out []string
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, c := range g.children[cur] {
			if !seen[c] {
				seen[c] = true
				out = append(out, c)
				queue = append(queue, c)
			}
		}
	}
	sort.Strings(out)
	return out
}

// Unknown returns the ids that blocks/parent-child edges touching id (in either
// direction) reference but that are absent from the export, sorted. A bead with
// any unknown id cannot be judged and must go to REVIEW.
func (g *Graph) Unknown(id string) []string { return clone(g.unknown[id]) }

// HasUnknown reports whether Unknown(id) is non-empty.
func (g *Graph) HasUnknown(id string) bool { return len(g.unknown[id]) > 0 }
