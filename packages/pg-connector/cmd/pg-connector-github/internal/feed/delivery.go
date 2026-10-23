package feed

import (
	"encoding/json"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/classify"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/engine"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

// The change words a delivered row carries for a key.
const (
	ChangeAdded   = "added"
	ChangeRemoved = "removed"
	ChangeChanged = "changed"
)

// naming says which of a row's membership kinds name a given query.
type naming struct{ entered, left, removed bool }

func (n naming) any() bool { return n.entered || n.left || n.removed }

// namesQuery reports which membership kinds of row name query: entered_query
// names it when its query:<name> field has an After that is the query name,
// left_query and removed when the field has a Before that is. A merged row whose
// entered and left fields cancelled names no query.
func namesQuery(row store.ChangeRow, query string) naming {
	var has struct{ entered, left, removed bool }
	for _, k := range row.Kinds {
		switch k {
		case classify.KindEnteredQuery:
			has.entered = true
		case classify.KindLeftQuery:
			has.left = true
		case classify.KindRemoved:
			has.removed = true
		}
	}
	if !has.entered && !has.left && !has.removed {
		return naming{}
	}
	fields, err := classify.DecodeFields(row.Fields)
	if err != nil {
		return naming{}
	}
	var n naming
	for _, f := range fields {
		if f.Name != engine.QueryFieldPrefix+query {
			continue
		}
		n.entered = has.entered && isString(f.After, query)
		n.left = has.left && isString(f.Before, query)
		n.removed = has.removed && isString(f.Before, query)
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

func hasKind(row store.ChangeRow, kind string) bool {
	for _, k := range row.Kinds {
		if k == kind {
			return true
		}
	}
	return false
}

// DeliveryFor reports whether a log row reaches a PR-feed key of query, and which
// of "added", "removed" or "changed" it is for that key. member says whether the
// row's entity is a member of the query now.
//
// The rows of a member are delivered. The rows of a non-member are delivered only
// when a membership kind names the query (an entered_query, a left_query or a
// removed row), so a consumer sees a departure, and sees the arrival that
// preceded a departure it has not yet been told of. An entered_query that names
// the query is "added"; a left_query or removed row that names it is "removed";
// anything else is "changed". Membership kinds that name ANOTHER query are not
// this key's added or removed.
func DeliveryFor(row store.ChangeRow, query string, member bool) (deliver bool, change string) {
	return DeliveryForKey(KindPR, row, query, member)
}

// DeliveryForKey is DeliveryFor for a key of kind: for the "ci" key only rows
// whose kinds include ci_changed (of a member), or a membership or removed kind
// naming the query, are delivered.
func DeliveryForKey(kind string, row store.ChangeRow, query string, member bool) (deliver bool, change string) {
	n := namesQuery(row, query)
	switch {
	case n.entered:
		change = ChangeAdded
	case n.left, n.removed:
		change = ChangeRemoved
	default:
		change = ChangeChanged
	}
	if n.any() {
		return true, change
	}
	if !member {
		return false, ""
	}
	if kind == KindCI && !hasKind(row, classify.KindCIChanged) {
		return false, ""
	}
	return true, change
}

// refFold is a wire reference with owner/repo folded to lower case and the
// number exact, the form two references of one PR compare equal in.
func refFold(ref string) string {
	if i := strings.LastIndex(ref, "#"); i >= 0 {
		return strings.ToLower(ref[:i]) + ref[i:]
	}
	return strings.ToLower(ref)
}

// memberSet is the set of a query's members by folded reference.
type memberSet map[string]struct{}

func newMemberSet(refs []string) memberSet {
	m := make(memberSet, len(refs))
	for _, r := range refs {
		m[refFold(r)] = struct{}{}
	}
	return m
}

func (m memberSet) has(ref string) bool { _, ok := m[refFold(ref)]; return ok }
