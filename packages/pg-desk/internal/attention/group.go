package attention

import (
	"context"
	"sort"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/links"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// This file is stage 4 of the evaluator, grouping and ordering
// (docs/behavior/pg-desk/attention.md, "Grouping and order").
//
// Group key, the first level that applies to an entity wins:
//
//  1. the Jira issue it cross-references (xref relation "jira"; with several,
//     the lexicographically smallest key);
//  2. otherwise its PR stack, named by the stack's root PR (StackSource);
//  3. otherwise the bead that tracks it (xref relation "work"; with several,
//     the lexicographically smallest id);
//  4. otherwise a singleton group keyed by the entity.
//
// Every key but the singleton's is anchored on an entity, so the key is a
// valid "<type>:<id>" ref for `pg-desk links`.
//
// Order, computed once here and emitted in list_attention order: inside a
// group, severity descending then entity id; between groups, the most urgent
// item's severity descending, then group size descending, then that item's
// entity id (the documented attention.ordering.ties default), then the group
// key so the result is total. Result.Items is Result.Groups flattened.

// Grouping-level xref vocabulary, shared with internal/links.
const (
	relationJira = "jira"
	relationWork = "work"
	typeIssue    = "issue"
	typePR       = "pr"
)

// StackSource names the PR stack an entity belongs to. It is the seam the
// PR-to-PR dependency source plugs into: Evaluate defaults it to
// dependency.StackSource over the store being read (Inputs.Stacks).
type StackSource interface {
	// StackRoot returns the entity id of the root PR of the stack the PR
	// belongs to. ok is false when the PR is in no stack (a lone PR is not a
	// stack). The same root MUST be returned for every member of one stack.
	StackRoot(ctx context.Context, entityType, id string) (rootID string, ok bool, err error)
}

// groupAnchor is where an entity's group key came from.
type groupAnchor struct {
	Type  string
	ID    string
	Label string
}

func (a groupAnchor) key() string { return Ref(a.Type, a.ID) }

// groupOf returns the group an entity belongs to. Cross-references and the
// stack are read for pull requests only: that is the entity the work-context
// edges leave from or arrive at; any other entity type is its own singleton.
// A store read error is an error (INV-ATTNEVAL-6).
func groupOf(ctx context.Context, in Inputs, v *View) (groupAnchor, error) {
	own := groupAnchor{Type: v.Type, ID: v.ID, Label: v.ID}
	if v.Type != typePR {
		return own, nil
	}
	linked, err := links.ReadLinked(ctx, in.linkReader(), v.Repo, v.Type, v.ID)
	if err != nil {
		return groupAnchor{}, err
	}
	if id, ok := smallestLinked(linked, links.DirOut, relationJira); ok {
		return groupAnchor{Type: typeIssue, ID: id, Label: id}, nil
	}
	if in.Stacks != nil {
		root, ok, serr := in.Stacks.StackRoot(ctx, v.Type, v.ID)
		if serr != nil {
			return groupAnchor{}, serr
		}
		if ok && root != "" {
			return groupAnchor{Type: typePR, ID: root, Label: root}, nil
		}
	}
	// A work item points at the PR it tracks (work item -> PR), so for a PR
	// the edge arrives.
	if id, ok := smallestLinked(linked, links.DirIn, relationWork); ok {
		return groupAnchor{Type: typeIssue, ID: id, Label: id}, nil
	}
	return own, nil
}

// smallestLinked returns the lexicographically smallest id of the linked
// issues reached in direction dir through relation rel.
func smallestLinked(linked []links.Linked, dir links.Direction, rel string) (string, bool) {
	best, found := "", false
	for _, l := range linked {
		if l.Direction != dir || l.Relation != rel || l.Type != typeIssue {
			continue
		}
		if !found || l.ID < best {
			best, found = l.ID, true
		}
	}
	return best, found
}

// byItemOrder is the in-group order and the final tie-break between groups:
// severity descending, then entity type and id.
func byItemOrder(a, b Item) bool {
	if a.Severity.rank() != b.Severity.rank() {
		return a.Severity.rank() > b.Severity.rank()
	}
	if a.Type != b.Type {
		return a.Type < b.Type
	}
	return a.ID < b.ID
}

// buildGroups assembles items into groups and puts both the groups and the
// items inside them in canonical order. labels maps a group key to its
// display label; an item whose key has no label uses the key.
func buildGroups(items []Item, labels map[string]string) []Group {
	byKey := map[string]*Group{}
	var keys []string
	for _, it := range items {
		g := byKey[it.Group]
		if g == nil {
			label := labels[it.Group]
			if label == "" {
				label = it.Group
			}
			g = &Group{Key: it.Group, Label: label}
			byKey[it.Group] = g
			keys = append(keys, it.Group)
		}
		g.Items = append(g.Items, it)
	}
	groups := make([]Group, 0, len(keys))
	for _, k := range keys {
		g := byKey[k]
		sort.SliceStable(g.Items, func(i, j int) bool { return byItemOrder(g.Items[i], g.Items[j]) })
		groups = append(groups, *g)
	}
	sort.SliceStable(groups, func(i, j int) bool {
		a, b := groups[i], groups[j]
		if a.Items[0].Severity.rank() != b.Items[0].Severity.rank() {
			return a.Items[0].Severity.rank() > b.Items[0].Severity.rank()
		}
		if len(a.Items) != len(b.Items) {
			return len(a.Items) > len(b.Items)
		}
		if byItemOrder(a.Items[0], b.Items[0]) != byItemOrder(b.Items[0], a.Items[0]) {
			return byItemOrder(a.Items[0], b.Items[0])
		}
		return a.Key < b.Key
	})
	return groups
}

// flatten returns the groups' items in canonical order.
func flatten(groups []Group) []Item {
	out := []Item{}
	for _, g := range groups {
		out = append(out, g.Items...)
	}
	return out
}

// repoLinkLister is the optional batched xref read: a store that has it is
// read once per repo instead of twice per grouped entity.
type repoLinkLister interface {
	ListXrefLinksForRepo(ctx context.Context, repo string) ([]store.XrefLink, error)
}

// linkReader returns the LinkedReader grouping reads through: the store
// itself, or, when the store can list a repo's links in one query, a
// per-evaluation snapshot of them. Evaluate reads, never writes, so the
// snapshot answers exactly what the per-entity queries would.
func (in Inputs) linkReader() links.LinkedReader {
	if in.snapshot != nil {
		return in.snapshot
	}
	return in.Store
}

// linkSnapshot serves ListXrefLinksFrom/To from one ListXrefLinksForRepo read
// per repo; every other LinkedReader method goes to the store.
type linkSnapshot struct {
	links.LinkedReader
	lister repoLinkLister
	byRepo map[string][]store.XrefLink
}

func (s *linkSnapshot) repoLinks(ctx context.Context, repo string) ([]store.XrefLink, error) {
	if ls, ok := s.byRepo[repo]; ok {
		return ls, nil
	}
	ls, err := s.lister.ListXrefLinksForRepo(ctx, repo)
	if err != nil {
		return nil, err
	}
	if s.byRepo == nil {
		s.byRepo = map[string][]store.XrefLink{}
	}
	s.byRepo[repo] = ls
	return ls, nil
}

func (s *linkSnapshot) filter(ctx context.Context, repo string, keep func(store.XrefLink) bool) ([]store.XrefLink, error) {
	if err := s.RequireNewSchema(ctx); err != nil {
		return nil, err
	}
	all, err := s.repoLinks(ctx, repo)
	if err != nil {
		return nil, err
	}
	var out []store.XrefLink
	for _, l := range all {
		if keep(l) {
			out = append(out, l)
		}
	}
	return out, nil
}

func (s *linkSnapshot) ListXrefLinksFrom(ctx context.Context, repo, fromType, fromID string) ([]store.XrefLink, error) {
	return s.filter(ctx, repo, func(l store.XrefLink) bool { return l.FromType == fromType && l.FromID == fromID })
}

func (s *linkSnapshot) ListXrefLinksTo(ctx context.Context, repo, toType, toID string) ([]store.XrefLink, error) {
	return s.filter(ctx, repo, func(l store.XrefLink) bool { return l.ToType == toType && l.ToID == toID })
}
