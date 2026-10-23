package feed

import (
	"encoding/json"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/engine"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/registry"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

// The change words a delivered row carries on the wire; the roles of a
// registry.Spec map a connector's kinds onto them.
const (
	ChangeAdded   = "added"
	ChangeRemoved = "removed"
	ChangeChanged = "changed"
)

// naming says which of a row's membership kinds name a given scope.
type naming struct{ entered, left, removed bool }

func (n naming) any() bool { return n.entered || n.left || n.removed }

// hasKind reports whether the row carries kind; an empty kind (a role the
// connector does not use) is never carried.
func hasKind(row store.ChangeRow, kind string) bool {
	if kind == "" {
		return false
	}
	for _, k := range row.Kinds {
		if k == kind {
			return true
		}
	}
	return false
}

// namesScope reports which membership kinds of row name scope: the entered role
// names it when its query field has an After that is the scope, the left and
// removed roles when the field has a Before that is. A merged row whose entered
// and left fields cancelled names no scope.
func namesScope(spec registry.Spec, row store.ChangeRow, scope string) naming {
	has := naming{
		entered: hasKind(row, spec.Roles.EnteredQuery),
		left:    hasKind(row, spec.Roles.LeftQuery),
		removed: hasKind(row, spec.Roles.Removed),
	}
	if !has.any() {
		return naming{}
	}
	fields, err := engine.DecodeFields(row.Fields)
	if err != nil {
		return naming{}
	}
	var n naming
	for _, f := range fields {
		if f.Name != engine.QueryFieldPrefix+scope {
			continue
		}
		n.entered = has.entered && isString(f.After, scope)
		n.left = has.left && isString(f.Before, scope)
		n.removed = has.removed && isString(f.Before, scope)
	}
	return n
}

func isString(raw json.RawMessage, want string) bool {
	if len(raw) == 0 {
		return false
	}
	var s string
	return json.Unmarshal(raw, &s) == nil && s == want
}

// Deliver reports whether a log row reaches a consumer bound to (view, scope),
// and the wire word. member says whether the row's entity is a member of scope
// now (false when scope is empty). Spec.Delivery selects the rule.
//
// DeliverByMembership (the legacy rule): the rows of a member are delivered. The
// rows of a non-member are delivered only when a membership kind names the scope
// (an entered, a left or a removed row), so a consumer sees a departure and the
// arrival that preceded a departure it has not yet been told of. A view with
// AnyOf delivers a member's row only when its kinds include one of them.
// Membership kinds that name ANOTHER scope are not this cursor's added or
// removed.
//
// DeliverByWatched: a row whose watched snapshot is not set is dropped. With a
// scope, a row whose entity is not a member and whose kinds do not name the scope
// through a membership kind is dropped. A view with AnyOf drops a row whose kinds
// miss AnyOf unless it carries the Removed role or names the scope. Everything
// else is delivered, member or not: an unscoped consumer receives the closed row
// of an entity that already left its query, while a scoped consumer asked for a
// membership filter and gets it.
//
// The word, with a scope: an entered kind that names the scope is added, a left
// or removed kind that names it is removed, the added role counts as added for a
// row whose entity is a member, anything else is changed. Without a scope no row
// names one, so the word follows the kinds: entered or added is added, left or
// removed is removed, anything else is changed.
func Deliver(spec registry.Spec, view registry.View, row store.ChangeRow, scope string, member bool) (ok bool, word string) {
	var n naming
	if scope != "" {
		n = namesScope(spec, row, scope)
	}
	if spec.Delivery == registry.DeliverByWatched {
		if !deliverByWatched(spec, view, row, scope, n, member) {
			return false, ""
		}
		return true, wireWord(spec, row, scope, n, member)
	}
	word = wireWord(spec, row, scope, n, member)
	if n.any() {
		return true, word
	}
	if !member {
		return false, ""
	}
	if len(view.AnyOf) > 0 && !hasAny(row, view.AnyOf) {
		return false, ""
	}
	return true, word
}

// deliverByWatched is the selection rule of DeliverByWatched; n says which
// membership kinds of the row name scope (none without a scope).
func deliverByWatched(spec registry.Spec, view registry.View, row store.ChangeRow, scope string, n naming, member bool) bool {
	switch {
	case !row.Watched:
		return false
	case scope != "" && !member && !n.any():
		return false
	case len(view.AnyOf) > 0 && !hasAny(row, view.AnyOf) && !hasKind(row, spec.Roles.Removed) && !n.any():
		return false
	}
	return true
}

func hasAny(row store.ChangeRow, kinds []string) bool {
	for _, k := range kinds {
		if hasKind(row, k) {
			return true
		}
	}
	return false
}

// wireWord is the word rule of Deliver.
func wireWord(spec registry.Spec, row store.ChangeRow, scope string, n naming, member bool) string {
	if scope != "" {
		switch {
		case n.entered:
			return ChangeAdded
		case n.left, n.removed:
			return ChangeRemoved
		case member && hasKind(row, spec.Roles.Added):
			return ChangeAdded
		}
		return ChangeChanged
	}
	switch {
	case hasKind(row, spec.Roles.EnteredQuery), hasKind(row, spec.Roles.Added):
		return ChangeAdded
	case hasKind(row, spec.Roles.LeftQuery), hasKind(row, spec.Roles.Removed):
		return ChangeRemoved
	}
	return ChangeChanged
}

// memberSet is the set of a scope's members by folded reference.
type memberSet struct {
	fold func(string) string
	m    map[string]struct{}
}

func newMemberSet(refs []string, fold func(string) string) memberSet {
	if fold == nil {
		fold = func(s string) string { return s }
	}
	m := make(map[string]struct{}, len(refs))
	for _, r := range refs {
		m[fold(r)] = struct{}{}
	}
	return memberSet{fold: fold, m: m}
}

func (s memberSet) has(ref string) bool {
	if s.m == nil {
		return false
	}
	_, ok := s.m[s.fold(ref)]
	return ok
}
