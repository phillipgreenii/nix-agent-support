package parity

import (
	"fmt"
	"sort"
	"strings"

	"github.com/phillipgreenii/pg-decider/internal/action"
)

// Entry is one normalized write, the unit the two sides are compared in.
//
// GRANULARITY (stated once, here): an entry is (entity, kind, op-class). That is
// the finest level the OLD side exposes. `pg-desk show --json` lists, per
// entity, only the planned ledger rows that still lack a bead id: a kind and a
// content hash, nothing else. The content hash is therefore NOT compared: the
// new plan carries no equivalent hash, and the two sides write different field
// shapes (a dedup_key the old bead never had, for one). Counts are kept, so two
// entries with the same key are two writes.
type Entry struct{ Entity, Kind, Op string }

// Op classes. Create and annotate are written for a thing that does not exist
// yet; the rest act on an existing work item.
const (
	OpCreate     = "create"      // a new work item
	OpAnnotate   = "annotate"    // a pg-desk annotation (new side only)
	OpAdopt      = "adopt"       // an existing keyless bead matched to the entity and linked
	OpKeyWrite   = "key-write"   // the stable dedup_key written onto an adopted bead (S19)
	OpKeyRewrite = "key-rewrite" // an adopted key rewritten to the node_id form (S19)
	OpClose      = "close"
	OpReopen     = "reopen"
	OpUpdate     = "update" // any other write to an existing work item
)

// Kinds the normalized vocabulary holds. An annotation entry's kind is
// "annotation:" plus the annotation key.
const (
	KindAnchor          = "anchor"
	KindProcessFeedback = "process-feedback" // old: feedback-cycle
	KindReviewPR        = "review-pr"        // old: review-request
	KindFixCI           = "fix-ci"
	KindResolveConflict = "resolve-conflict"
	KindFocusItem       = "focus-item"
	kindAnnotationPref  = "annotation:"
)

var oldKinds = map[string]string{
	"anchor":         KindAnchor,
	"feedback-cycle": KindProcessFeedback,
	"review-request": KindReviewPR,
}

var newKinds = map[string]bool{
	KindAnchor: true, KindProcessFeedback: true, KindReviewPR: true, KindFixCI: true, KindResolveConflict: true,
	KindFocusItem: true,
}

// NormalizeOld maps the old side to entries. Two sources feed it, and they are
// kept apart on purpose:
//
//   - OBSERVED: each planned ledger row is one create entry (anchor,
//     feedback-cycle -> process-feedback, review-request -> review-pr).
//   - MODELLED, adoption only: the old sync adopts, before creating anything,
//     every work bead of the entity it recognises by shape (docs/behavior/
//     pg-desk/sync.md "Adoption": `<repo>#<n>: ` title prefix is the anchor, the
//     exact titles `review-pr: <entity>` and `process-feedback: <entity>` are
//     the children). Adoption makes no ledger row with an empty bead id, so the
//     old runner cannot see it; it is derived from the fixture's beads by that
//     one documented classifier, so a bead the new side fails to adopt shows as
//     a difference instead of vanishing.
//
// The old side's closes, reopens and field refreshes of existing beads are NOT
// modelled: they are unobservable here, and the new side's matching entries are
// listed in the expected diff (id UNOBS) rather than guessed at.
func NormalizeOld(fx *Fixture, old OldResult) ([]Entry, error) {
	var out []Entry
	for entity, rows := range old.Rows {
		for _, r := range rows {
			kind, ok := oldKinds[r.Kind]
			if !ok {
				return nil, fmt.Errorf("parity: old row kind %q for %s is not one of anchor, feedback-cycle, review-request", r.Kind, entity)
			}
			out = append(out, Entry{entity, kind, OpCreate})
		}
	}
	for _, entity := range fx.Entities {
		for _, b := range fx.Beads {
			if kind, ok := oldAdoptionKind(b.Title, entity); ok {
				out = append(out, Entry{entity, kind, OpAdopt})
			}
		}
	}
	sortEntries(out)
	return out, nil
}

// oldAdoptionKind classifies a bead title the way the old sync adoption does.
func oldAdoptionKind(title, entity string) (string, bool) {
	switch {
	case title == "review-pr: "+entity:
		return KindReviewPR, true
	case title == "process-feedback: "+entity:
		return KindProcessFeedback, true
	case strings.HasPrefix(title, entity+": "):
		return KindAnchor, true
	}
	return "", false
}

// NormalizeNew maps the raw pg-decider plan of each entity to entries. An
// adoption update is two entries, the link (adopt) and the dedup_key it writes
// (key-write), because only the first has an old counterpart; the node_id
// rewrite is one key-rewrite entry. Every other action maps by its op.
func NormalizeNew(nr NewResult) ([]Entry, error) {
	var out []Entry
	for entity, plan := range nr.Plans {
		for _, a := range plan.Actions {
			kind, err := newKind(a)
			if err != nil {
				return nil, fmt.Errorf("parity: new plan of %s: %w", entity, err)
			}
			switch {
			case a.Op == action.OpUpdate && a.Rule == "adoption":
				out = append(out, Entry{entity, kind, OpAdopt}, Entry{entity, kind, OpKeyWrite})
			case a.Op == action.OpUpdate && a.Rule == "adoption.node-id":
				out = append(out, Entry{entity, kind, OpKeyRewrite})
			default:
				op, err := newOp(a.Op)
				if err != nil {
					return nil, fmt.Errorf("parity: new plan of %s: %w", entity, err)
				}
				out = append(out, Entry{entity, kind, op})
			}
		}
	}
	sortEntries(out)
	return out, nil
}

func newKind(a action.Action) (string, error) {
	if a.Op == action.OpAnnotate {
		if a.Target == nil || *a.Target == "" {
			return "", fmt.Errorf("annotate action of rule %q has no key", a.Rule)
		}
		return kindAnnotationPref + *a.Target, nil
	}
	if !newKinds[a.Kind] {
		return "", fmt.Errorf("action kind %q (rule %q) is not a known work-item kind", a.Kind, a.Rule)
	}
	return a.Kind, nil
}

func newOp(op action.Op) (string, error) {
	switch op {
	case action.OpCreate:
		return OpCreate, nil
	case action.OpUpdate:
		return OpUpdate, nil
	case action.OpReopen:
		return OpReopen, nil
	case action.OpClose:
		return OpClose, nil
	case action.OpAnnotate:
		return OpAnnotate, nil
	}
	return "", fmt.Errorf("action op %q is not known", op)
}

func sortEntries(es []Entry) {
	sort.Slice(es, func(i, j int) bool {
		a, b := es[i], es[j]
		if a.Entity != b.Entity {
			return a.Entity < b.Entity
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return a.Op < b.Op
	})
}
