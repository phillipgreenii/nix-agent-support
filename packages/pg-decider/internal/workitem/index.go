package workitem

import (
	"fmt"
	"strings"

	"github.com/phillipgreenii/pg-decider/internal/view"
)

// Item is one work item linked from the view.
type Item struct {
	ID, Title, State, Assignee, DedupKey string // State is the link's state ("open", "closed", ...)
	Kind                                 Kind
	Labels                               []string
	Metadata                             map[string]string
}

// Open reports whether the item is not closed. A claimed in_progress item is
// open. How an item came to be closed is not recorded and never asked.
func (i Item) Open() bool { return i.State != "closed" }

// HasLabel reports whether the item carries label l.
func (i Item) HasLabel(l string) bool {
	for _, x := range i.Labels {
		if x == l {
			return true
		}
	}
	return false
}

// RelationSource is the link relation of a work item that names its source
// entity in its own metadata (source_type, source_id): a focus bead is linked
// to its source by it, never by the PR's own "work" relation.
const RelationSource = "source"

const fbsumPrefix = "fbsum:"

// Digest is the digest in the item's fbsum:<digest> label, "" when absent.
func (i Item) Digest() string {
	for _, l := range i.Labels {
		if d, ok := strings.CutPrefix(l, fbsumPrefix); ok {
			return d
		}
	}
	return ""
}

// Adoptable is an item without a dedup_key that belongs to the viewed entity.
// MatchedBy is "title", "anchor-prefix" or "node_id". This package only
// classifies; the rule that writes the dedup_key is a separate packet.
type Adoptable struct {
	Item      Item
	Kind      Kind
	MatchedBy string
}

// Index answers questions about the work items linked from one view. Adoptable
// items count as existing work: Anchor, ByKind and Find include them, so a
// rule never creates a duplicate while an adoption is still pending.
type Index struct {
	items     []Item // every recognized work item, in link order
	adoptable []Adoptable
}

// titlePrefixKinds are the title shapes of the non-anchor kinds.
var titlePrefixKinds = []Kind{KindProcessFeedback, KindReviewPR, KindFixCI, KindResolveConflict}

// kindFromTitle classifies a title by shape alone, for any entity: a kind
// prefix, or a "<repo>#<n>: " prefix for the anchor.
func kindFromTitle(title string) (Kind, bool) {
	for _, k := range titlePrefixKinds {
		if strings.HasPrefix(title, string(k)+": ") {
			return k, true
		}
	}
	if i := strings.Index(title, "#"); i > 0 {
		rest := title[i+1:]
		j := 0
		for j < len(rest) && rest[j] >= '0' && rest[j] <= '9' {
			j++
		}
		if j > 0 && strings.HasPrefix(rest[j:], ": ") {
			return KindAnchor, true
		}
	}
	return "", false
}

// entityTitleRef is the <repo>#<n> reference titles use for this entity.
func entityTitleRef(v *view.View) string {
	if s := v.Snapshot; s.Repo != "" && s.Number > 0 {
		return fmt.Sprintf("%s#%d", s.Repo, s.Number)
	}
	return v.ID
}

// BuildIndex classifies the view's links. A link is a work item when its type
// is "issue"; its kind is the kind segment of its dedup_key, or else (an
// adoptable item) comes from its title. A link matching neither is ignored,
// and so is a keyless link that does not belong to this entity. Adoption
// precedence: existing dedup_key (not adoptable), then node_id (the item's
// node_id metadata equals the snapshot's), then exact title, then the anchor
// "<repo>#<n>: " prefix. A malformed dedup_key counts as absent. BuildIndex
// never fails: bad metadata is treated as empty.
//
// A focus bead (relation "source", dedup_key naming the focus-item kind) is a
// keyed work item and is indexed like any other. A keyless link of relation
// "source" is never adoptable, whatever its title looks like: a focus bead is
// not the PR's own work, so it is never adopted as an anchor or a child.
func BuildIndex(v *view.View) *Index {
	ix := &Index{}
	if v == nil {
		return ix
	}
	ref := entityTitleRef(v)
	nodeID := v.Snapshot.NodeID
	for _, l := range v.Links {
		if l.Type != "issue" {
			continue
		}
		it := Item{
			ID: l.ID, Title: l.Title, State: l.State, Assignee: l.Assignee,
			Labels: cloneStrings(l.Labels), Metadata: cloneMap(l.Metadata),
		}
		if _, _, k, _, ok := ParseKey(l.Metadata["dedup_key"]); ok {
			it.DedupKey, it.Kind = l.Metadata["dedup_key"], k
			ix.items = append(ix.items, it)
			continue
		}
		if l.Relation == RelationSource {
			continue
		}
		k, by, ok := adoptionMatch(it, ref, nodeID)
		if !ok {
			continue
		}
		it.Kind = k
		ix.items = append(ix.items, it)
		ix.adoptable = append(ix.adoptable, Adoptable{Item: it, Kind: k, MatchedBy: by})
	}
	return ix
}

func adoptionMatch(it Item, ref, nodeID string) (Kind, string, bool) {
	itemNode := it.Metadata["node_id"]
	if nodeID != "" && itemNode != "" {
		if itemNode != nodeID {
			return "", "", false // another entity's item
		}
		if k, ok := kindFromTitle(it.Title); ok {
			return k, "node_id", true
		}
		return "", "", false
	}
	for _, k := range titlePrefixKinds {
		if it.Title == string(k)+": "+ref {
			return k, "title", true
		}
	}
	if strings.HasPrefix(it.Title, ref+": ") {
		return KindAnchor, "anchor-prefix", true
	}
	return "", "", false
}

func cloneMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// Anchor returns the anchor, preferring an open one, then link order.
func (ix *Index) Anchor() (Item, bool) {
	best, ok := Item{}, false
	for _, it := range ix.ByKind(KindAnchor) {
		if !ok || (it.Open() && !best.Open()) {
			best, ok = it, true
		}
	}
	return best, ok
}

// ByKind lists the items of kind k (keyed and adoptable) in link order.
func (ix *Index) ByKind(k Kind) []Item {
	if ix == nil {
		return nil
	}
	var out []Item
	for _, it := range ix.items {
		if it.Kind == k {
			out = append(out, it)
		}
	}
	return out
}

// Find returns the existing item of kind k for context c, open or closed,
// ignoring how it was closed. A keyed item matches when its key names kind k
// with exactly ContextSuffix(k, c), in either key form (id or node_id; links
// scope an item to this entity, so a pre-rename id form still counts). A
// keyless adoptable item matches on its metadata: fix-ci by head_sha,
// resolve-conflict by branch, head_sha, base and base_sha, process-feedback
// by its fbsum label digest, review-pr and anchor always. An empty context
// value never matches. With several matches an open item wins, then a keyed
// one, then link order.
func (ix *Index) Find(k Kind, c Context) (Item, bool) {
	best, bestRank, found := Item{}, 0, false
	for _, it := range ix.ByKind(k) {
		if !matchesContext(it, k, c) {
			continue
		}
		rank := 0
		if !it.Open() {
			rank += 2
		}
		if it.DedupKey == "" {
			rank++
		}
		if !found || rank < bestRank {
			best, bestRank, found = it, rank, true
		}
	}
	return best, found
}

func matchesContext(it Item, k Kind, c Context) bool {
	if it.DedupKey != "" {
		_, _, _, suffix, _ := ParseKey(it.DedupKey)
		return suffix == ContextSuffix(k, c)
	}
	eq := func(a, b string) bool { return a != "" && a == b }
	md := it.Metadata
	switch k {
	case KindFixCI:
		return eq(md["head_sha"], c.HeadSHA)
	case KindResolveConflict:
		return eq(md["branch"], c.Branch) && eq(md["head_sha"], c.HeadSHA) &&
			eq(md["base"], c.Base) && eq(md["base_sha"], c.BaseSHA)
	case KindProcessFeedback:
		return eq(it.Digest(), c.Digest)
	}
	return true // review-pr and anchor: one item per PR
}

// OpenHumanParked reports whether an open item labeled human exists for k;
// parked work counts as existing and is never re-created or re-labeled.
func (ix *Index) OpenHumanParked(k Kind) bool {
	for _, it := range ix.ByKind(k) {
		if it.Open() && it.HasLabel("human") {
			return true
		}
	}
	return false
}

// CoveredComments is the union of covered_comments ids over every
// process-feedback item, open or closed (what an earlier cycle covers).
// Pre-cutover cycles carry no covered_comments and add nothing.
func (ix *Index) CoveredComments() map[string]bool {
	out := map[string]bool{}
	for _, it := range ix.ByKind(KindProcessFeedback) {
		for _, id := range strings.Split(it.Metadata["covered_comments"], ",") {
			if id = strings.TrimSpace(id); id != "" {
				out[id] = true
			}
		}
	}
	return out
}

// Adoptable lists the items lacking a dedup_key that belong to this entity, in
// link order.
func (ix *Index) Adoptable() []Adoptable {
	if ix == nil {
		return nil
	}
	return append([]Adoptable(nil), ix.adoptable...)
}
