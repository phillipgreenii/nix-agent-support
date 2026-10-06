package dependency

import (
	"sort"
)

// StackSource names the PR stack a pull request belongs to, for the attention
// evaluator's "PR stack" grouping level (docs/behavior/pg-desk/attention.md,
// "Grouping and order"). It satisfies attention.StackSource.
//
// The stack of a PR is the connected component of the dependency graph that
// holds it. Edges come from every registered source (Resolver) and are
// followed in both directions, only through open PRs: a merged or closed PR,
// or one with no stored row, is not part of any stack and links nothing. A PR
// with no open dependency and no open dependent is in no stack. The stack is
// named by its root: the member that depends on no other member, smallest id
// first; when none qualifies (a cycle) the smallest id of all members.
//
// Like Resolver it indexes the store once, lazily, and the answer is memoized
// per member, so build one StackSource per evaluation.
type StackSource struct {
	res   *Resolver
	roots map[string]stackAnswer
}

type stackAnswer struct {
	root string
	ok   bool
}

// NewStackSource builds a StackSource over the default registry for one
// repository.
func NewStackSource(r Reader, repo string) *StackSource {
	return NewStackSourceWith(NewResolver(r, repo))
}

// NewStackSourceWith builds a StackSource over a caller-supplied Resolver.
func NewStackSourceWith(res *Resolver) *StackSource {
	return &StackSource{res: res, roots: map[string]stackAnswer{}}
}

// StackRoot returns the id of the root PR of the stack the PR belongs to; ok
// is false when it is in no stack. Every member of one stack gets the same
// root.
func (s *StackSource) StackRoot(entityType, id string) (string, bool, error) {
	if entityType != TypePR || id == "" {
		return "", false, nil
	}
	if a, seen := s.roots[id]; seen {
		return a.root, a.ok, nil
	}
	state, err := s.res.ctx.stateOf(id)
	if err != nil {
		return "", false, err
	}
	if state != StateOpen {
		s.roots[id] = stackAnswer{}
		return "", false, nil
	}
	members, hasDep, err := s.component(id)
	if err != nil {
		return "", false, err
	}
	if len(members) < 2 {
		s.roots[id] = stackAnswer{}
		return "", false, nil
	}
	root := ""
	for _, m := range members { // members is sorted, so the first match is the smallest
		if !hasDep[m] {
			root = m
			break
		}
	}
	if root == "" {
		root = members[0]
	}
	for _, m := range members {
		s.roots[m] = stackAnswer{root: root, ok: true}
	}
	return root, true, nil
}

// component walks the open dependency graph from id and returns the sorted
// member ids and, per member, whether it depends on another member.
func (s *StackSource) component(id string) ([]string, map[string]bool, error) {
	seen := map[string]bool{id: true}
	hasDep := map[string]bool{}
	queue := []string{id}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		deps, err := s.res.OpenDependenciesOf(TypePR, cur)
		if err != nil {
			return nil, nil, err
		}
		dependents, err := s.res.DependentsOf(TypePR, cur)
		if err != nil {
			return nil, nil, err
		}
		if len(deps) > 0 {
			hasDep[cur] = true
		}
		next := make([]Edge, 0, len(deps)+len(dependents))
		next = append(next, deps...)
		for _, e := range dependents {
			if e.Open() {
				next = append(next, e)
			}
		}
		for _, e := range next {
			if !seen[e.ID] {
				seen[e.ID] = true
				queue = append(queue, e.ID)
			}
		}
	}
	members := make([]string, 0, len(seen))
	for m := range seen {
		members = append(members, m)
	}
	sort.Strings(members)
	return members, hasDep, nil
}
