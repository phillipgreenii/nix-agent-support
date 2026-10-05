package workitem

import (
	"strings"

	"github.com/phillipgreenii/pg-decider/internal/view"
)

// EntityRef identifies the viewed entity. NodeID is the stable backend id
// (spec 5.1, S19), empty when the snapshot has none.
type EntityRef struct{ Type, ID, NodeID string }

// Context carries the values a kind's "same context" is made of (spec 7.3);
// only the fields a kind's context uses are read.
type Context struct{ HeadSHA, Branch, Base, BaseSHA, Digest string }

// EntityRefFrom reads the entity identity out of a view.
func EntityRefFrom(v *view.View) EntityRef {
	return EntityRef{Type: v.Type, ID: v.ID, NodeID: v.Snapshot.NodeID}
}

// ContextSuffix is the dedup-key suffix naming k's context where that context
// is narrower than the PR (spec 9.8): ":<head_sha>" for fix-ci,
// ":<branch>:<head_sha>:<base>:<base_sha>" for resolve-conflict, ":<digest>"
// for process-feedback, and "" for review-pr, anchor and unknown kinds.
func ContextSuffix(k Kind, c Context) string {
	switch k {
	case KindFixCI:
		return ":" + c.HeadSHA
	case KindResolveConflict:
		return ":" + strings.Join([]string{c.Branch, c.HeadSHA, c.Base, c.BaseSHA}, ":")
	case KindProcessFeedback:
		return ":" + c.Digest
	}
	return ""
}

// DedupKey is <type>:<id>:<kind> plus the kind's context suffix.
func DedupKey(e EntityRef, k Kind, c Context) string {
	return e.Type + ":" + e.ID + ":" + string(k) + ContextSuffix(k, c)
}

// DedupKeyNodeID is the node_id form <type>:<node_id>:<kind> plus the same
// suffix; false when the entity carries no node_id.
func DedupKeyNodeID(e EntityRef, k Kind, c Context) (string, bool) {
	if e.NodeID == "" {
		return "", false
	}
	return e.Type + ":" + e.NodeID + ":" + string(k) + ContextSuffix(k, c), true
}

// ParseKey splits <type>:<ident>:<kind>[:suffix]. suffix keeps its leading
// colon (the exact ContextSuffix output) and is "" when absent. ok is false
// for anything that is not that shape with a known kind. Ids and node_ids
// carry no colon, nor do git ref names, so the split is unambiguous.
func ParseKey(key string) (typ, ident string, k Kind, suffix string, ok bool) {
	parts := strings.SplitN(key, ":", 4)
	if len(parts) < 3 || parts[0] == "" || parts[1] == "" {
		return "", "", "", "", false
	}
	k = Kind(parts[2])
	if !knownKind(k) {
		return "", "", "", "", false
	}
	if len(parts) == 4 {
		suffix = ":" + parts[3]
	}
	return parts[0], parts[1], k, suffix, true
}

// Owns reports whether key is a well-formed dedup key naming this entity in
// either form: <type>:<id>:... or, when e carries a node_id,
// <type>:<node_id>:... . It is how both key forms are recognized as the same
// identity.
func (e EntityRef) Owns(key string) bool {
	typ, ident, _, _, ok := ParseKey(key)
	if !ok || typ != e.Type {
		return false
	}
	return ident == e.ID || (e.NodeID != "" && ident == e.NodeID)
}
