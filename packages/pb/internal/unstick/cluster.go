package unstick

import (
	"fmt"
	"sort"
	"strings"
)

// Batch size bounds for packing REVIEW beads (plan v2, "Packing").
const (
	// SingleBatchBelow: a REVIEW set smaller than this is one batch.
	SingleBatchBelow = 10
	// MinBatch and MaxBatch bound a batch when the REVIEW set is large enough
	// (>= 20 beads) for both to be satisfiable.
	MinBatch = 10
	MaxBatch = 15
)

// BatchName returns the batch file name for the 0-based index i: B01, B02, ...
func BatchName(i int) string { return fmt.Sprintf("B%02d", i+1) }

// clusterLinks is the undirected link graph over REVIEW beads.
type clusterLinks struct {
	ids []string // sorted REVIEW ids (deduplicated)
	adj map[string]map[string]bool
}

func (l *clusterLinks) link(a, b string) {
	if a == b {
		return
	}
	l.adj[a][b] = true
	l.adj[b][a] = true
}

func (l *clusterLinks) neighbours(id string) []string {
	out := make([]string, 0, len(l.adj[id]))
	for n := range l.adj[id] {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// idChar reports whether b can be part of a bead id (letters, digits, hyphen,
// underscore). Dots are handled separately because ids carry dotted suffixes.
func idChar(b byte) bool {
	return b == '-' || b == '_' || (b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// MentionsID reports whether text contains id as a whole token. The match is a
// substring search for a KNOWN id (not a regex), bounded so that "tc-1" does not
// match inside "tc-10", "xtc-1" or the dotted child "tc-1.2", while a sentence
// full stop ("see tc-1.") still matches.
func MentionsID(text, id string) bool {
	if id == "" {
		return false
	}
	for from := 0; from < len(text); {
		i := strings.Index(text[from:], id)
		if i < 0 {
			return false
		}
		i += from
		end := i + len(id)
		okBefore := i == 0 || !idChar(text[i-1])
		okAfter := end == len(text) || (!idChar(text[end]) && !(text[end] == '.' && end+1 < len(text) && idChar(text[end+1])))
		if okBefore && okAfter {
			return true
		}
		from = i + 1
	}
	return false
}

// buildLinks computes the undirected link graph over the REVIEW ids:
//   - blocks / parent-child edges between two REVIEW beads;
//   - one hop through an OUTSIDE bead that is not closed: all REVIEW beads
//     touching it by blocks/parent-child in either direction are linked (a
//     closed outside bead is never a hub);
//   - a bead id mentioned in another REVIEW bead's title/description/notes,
//     matched against the known id set;
//   - identical non-empty defer_until.
//
// Labels never link.
func buildLinks(review []string, g *Graph) *clusterLinks {
	set := map[string]bool{}
	for _, id := range review {
		set[id] = true
	}
	l := &clusterLinks{adj: map[string]map[string]bool{}}
	for id := range set {
		l.ids = append(l.ids, id)
		l.adj[id] = map[string]bool{}
	}
	sort.Strings(l.ids)

	touching := func(id string) []string {
		out := g.Blockers(id)
		out = append(out, g.BlockerDependents(id)...)
		if p, ok := g.Parent(id); ok {
			out = append(out, p)
		}
		return append(out, g.Children(id)...)
	}

	hubs := map[string][]string{} // outside non-closed id -> REVIEW ids touching it
	for _, id := range l.ids {
		for _, o := range touching(id) {
			if set[o] {
				l.link(id, o)
				continue
			}
			if r, ok := g.Row(o); ok && r.Status != StatusClosed {
				hubs[o] = append(hubs[o], id)
			}
		}
	}
	for _, members := range hubs {
		for _, m := range members[1:] {
			l.link(members[0], m)
		}
	}

	byDefer := map[string]string{}
	for _, id := range l.ids {
		r, ok := g.Row(id)
		if !ok {
			continue
		}
		if r.DeferUntil != "" {
			if first, seen := byDefer[r.DeferUntil]; seen {
				l.link(first, id)
			} else {
				byDefer[r.DeferUntil] = id
			}
		}
		text := r.Title + "\n" + r.Description + "\n" + r.Notes
		for _, other := range l.ids {
			if other != id && MentionsID(text, other) {
				l.link(id, other)
			}
		}
	}
	return l
}

// components returns the connected components, each sorted by id, ordered by
// lowest id.
func (l *clusterLinks) components() [][]string {
	seen := map[string]bool{}
	var out [][]string
	for _, start := range l.ids {
		if seen[start] {
			continue
		}
		var comp []string
		stack := []string{start}
		seen[start] = true
		for len(stack) > 0 {
			cur := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			comp = append(comp, cur)
			for n := range l.adj[cur] {
				if !seen[n] {
					seen[n] = true
					stack = append(stack, n)
				}
			}
		}
		sort.Strings(comp)
		out = append(out, comp)
	}
	return out
}

// bfsOrder lists comp in breadth-first order from its lowest id, visiting
// neighbours in id order.
func (l *clusterLinks) bfsOrder(comp []string) []string {
	in := map[string]bool{}
	for _, id := range comp {
		in[id] = true
	}
	seen := map[string]bool{comp[0]: true}
	order := []string{comp[0]}
	for i := 0; i < len(order); i++ {
		for _, n := range l.neighbours(order[i]) {
			if in[n] && !seen[n] {
				seen[n] = true
				order = append(order, n)
			}
		}
	}
	return order
}

// Cluster groups the REVIEW ids into batches. See package doc and the plan's
// "Cluster links" and "Packing" rules. Every REVIEW id (duplicates collapsed)
// appears in exactly one batch; batches are sorted internally and by lowest id.
// A total below SingleBatchBelow is one batch. Otherwise connected components
// are placed whole (largest first); a component over MaxBatch is split in BFS
// order from its lowest id; singletons (sorted by first label of a sorted copy,
// then title, then id; byte comparison) fill the smallest batches. Batches are
// MinBatch..MaxBatch except that totals of 16..19 cannot satisfy both bounds
// and are split evenly. The input slice is not modified.
func Cluster(review []string, g *Graph) [][]string {
	l := buildLinks(review, g)
	n := len(l.ids)
	if n == 0 {
		return nil
	}
	if n < SingleBatchBelow {
		return [][]string{append([]string(nil), l.ids...)}
	}

	var units [][]string // multi-member units (components, or BFS chunks of oversize ones)
	var singles []string
	for _, comp := range l.components() {
		switch {
		case len(comp) == 1:
			singles = append(singles, comp[0])
		case len(comp) <= MaxBatch:
			units = append(units, comp)
		default:
			order := l.bfsOrder(comp)
			for len(order) > 0 {
				k := min(MaxBatch, len(order))
				units = append(units, order[:k])
				order = order[k:]
			}
		}
	}
	sort.SliceStable(units, func(i, j int) bool {
		if len(units[i]) != len(units[j]) {
			return len(units[i]) > len(units[j])
		}
		return minID(units[i]) < minID(units[j])
	})
	sortSingletons(singles, g)

	k := (n + MaxBatch - 1) / MaxBatch
	batches := make([][]string, k)
	// Components whole, largest first, into the batch with the most room.
	for _, u := range units {
		best := -1
		for i, b := range batches {
			if MaxBatch-len(b) >= len(u) && (best < 0 || len(b) < len(batches[best])) {
				best = i
			}
		}
		if best < 0 {
			batches = append(batches, append([]string(nil), u...))
			continue
		}
		batches[best] = append(batches[best], u...)
	}
	// Singletons fill the smallest batch that still has room.
	for _, s := range singles {
		best := -1
		for i, b := range batches {
			if len(b) < MaxBatch && (best < 0 || len(b) < len(batches[best])) {
				best = i
			}
		}
		if best < 0 {
			batches = append(batches, nil)
			best = len(batches) - 1
		}
		batches[best] = append(batches[best], s)
	}
	batches = repairShort(batches)

	for _, b := range batches {
		sort.Strings(b)
	}
	sort.Slice(batches, func(i, j int) bool { return batches[i][0] < batches[j][0] })
	return batches
}

func minID(ids []string) string {
	m := ids[0]
	for _, id := range ids[1:] {
		if id < m {
			m = id
		}
	}
	return m
}

// sortSingletons orders ids by (first label of a sorted COPY of the labels,
// title, id) using byte comparison. Row data is never mutated.
func sortSingletons(ids []string, g *Graph) {
	type key struct{ label, title string }
	keys := map[string]key{}
	for _, id := range ids {
		r, _ := g.Row(id)
		labels := append([]string(nil), r.Labels...)
		sort.Strings(labels)
		k := key{title: r.Title}
		if len(labels) > 0 {
			k.label = labels[0]
		}
		keys[id] = k
	}
	sort.Slice(ids, func(i, j int) bool {
		a, b := keys[ids[i]], keys[ids[j]]
		if a.label != b.label {
			return a.label < b.label
		}
		if a.title != b.title {
			return a.title < b.title
		}
		return ids[i] < ids[j]
	})
}

// repairShort fixes batches below MinBatch: first by merging (dissolving the
// smallest batch into others that have room, which keeps components whole),
// otherwise by rebalancing (moving the most recently added members of the
// largest batch to the smallest until it reaches MinBatch, or until the two
// differ by at most one).
func repairShort(batches [][]string) [][]string {
	for len(batches) > 1 {
		si := smallest(batches)
		if len(batches[si]) >= MinBatch {
			break
		}
		// Merge: only if everything fits elsewhere.
		room := 0
		for i, b := range batches {
			if i != si {
				room += MaxBatch - len(b)
			}
		}
		if room >= len(batches[si]) {
			rest := batches[si]
			batches = append(batches[:si:si], batches[si+1:]...)
			for _, id := range rest {
				best := -1
				for i, b := range batches {
					if len(b) < MaxBatch && (best < 0 || len(b) < len(batches[best])) {
						best = i
					}
				}
				batches[best] = append(batches[best], id)
			}
			continue
		}
		// Rebalance from the largest batch.
		li := largest(batches)
		if len(batches[li])-len(batches[si]) < 2 {
			break
		}
		for len(batches[si]) < MinBatch && len(batches[li])-len(batches[si]) >= 2 {
			last := len(batches[li]) - 1
			batches[si] = append(batches[si], batches[li][last])
			batches[li] = batches[li][:last]
		}
	}
	return batches
}

func smallest(b [][]string) int {
	best := 0
	for i := range b {
		if len(b[i]) < len(b[best]) {
			best = i
		}
	}
	return best
}

func largest(b [][]string) int {
	best := 0
	for i := range b {
		if len(b[i]) > len(b[best]) {
			best = i
		}
	}
	return best
}
