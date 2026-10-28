package feed

import (
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/engine"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

// The DeliverByWatched rule (9.4): the snapshot decides first, a scope then asks
// for membership or a membership kind that names it, and a view filter yields to
// the removed role and to naming the scope.

func watchedRowOf(t *testing.T, watched bool, kinds []string, fields ...engine.Field) store.ChangeRow {
	t.Helper()
	row := rowOf(t, kinds, fields...)
	row.Watched = watched
	return row
}

type watchedCase struct {
	name    string
	row     store.ChangeRow
	scope   string
	member  bool
	deliver bool
	change  string
}

func runWatchedCases(t *testing.T, view string, cases []watchedCase) {
	t.Helper()
	spec := testSpec()
	for _, tc := range cases {
		deliver, change := Deliver(spec, viewNamed(spec, view), tc.row, tc.scope, tc.member)
		if deliver != tc.deliver || change != tc.change {
			t.Errorf("%s: got (%v, %q), want (%v, %q)", tc.name, deliver, change, tc.deliver, tc.change)
		}
	}
}

func TestDeliverByWatchedTableOfTheUnfilteredView(t *testing.T) {
	k := func(kinds ...string) []string { return kinds }
	runWatchedCases(t, viewAll, []watchedCase{
		// The snapshot decides first.
		{"a row without the snapshot, unscoped", watchedRowOf(t, false, k(kindContent)), "", false, false, ""},
		{"a row without the snapshot, member of the scope", watchedRowOf(t, false, k(kindContent)), "q", true, false, ""},
		{"a row without the snapshot that names the scope", watchedRowOf(t, false, k(kindEntered), enteredField("q")), "q", true, false, ""},
		// An unscoped consumer gets every watched row, member or not.
		{"unscoped plain change", watchedRowOf(t, true, k(kindContent)), "", false, true, "changed"},
		{"unscoped closed row of an entity that left its query", watchedRowOf(t, true, k(kindRemoved)), "", false, true, "removed"},
		{"unscoped empty kinds", watchedRowOf(t, true, nil), "", false, true, "changed"},
		// A scoped consumer asked for a membership filter.
		{"scoped member plain change", watchedRowOf(t, true, k(kindContent)), "q", true, true, "changed"},
		{"scoped non-member plain change", watchedRowOf(t, true, k(kindContent)), "q", false, false, ""},
		{"scoped non-member closed row", watchedRowOf(t, true, k(kindRemoved)), "q", false, false, ""},
		{"scoped non-member row that names the scope", watchedRowOf(t, true, k(kindLeft), leftField("q")), "q", false, true, "removed"},
		{"scoped non-member closed row that names the scope", watchedRowOf(t, true, k(kindRemoved), leftField("q")), "q", false, true, "removed"},
		{"scoped non-member entered row that names the scope", watchedRowOf(t, true, k(kindEntered), enteredField("q")), "q", false, true, "added"},
		{"scoped non-member row that names another scope", watchedRowOf(t, true, k(kindEntered), enteredField("r")), "q", false, false, ""},
		{"scoped member row that names another scope", watchedRowOf(t, true, k(kindEntered), enteredField("r")), "q", true, true, "changed"},
		{"scoped entered then left in one window, non-member", watchedRowOf(t, true, k(kindEntered, kindLeft)), "q", false, false, ""},
	})
}

func TestDeliverByWatchedTableOfTheFilteredView(t *testing.T) {
	k := func(kinds ...string) []string { return kinds }
	runWatchedCases(t, viewFlagged, []watchedCase{
		{"unscoped flagged change", watchedRowOf(t, true, k(kindFlag)), "", false, true, "changed"},
		{"unscoped content change only", watchedRowOf(t, true, k(kindContent)), "", false, false, ""},
		{"unscoped closed row passes the filter", watchedRowOf(t, true, k(kindRemoved)), "", false, true, "removed"},
		{"scoped member flagged change", watchedRowOf(t, true, k(kindFlag)), "q", true, true, "changed"},
		{"scoped member content change only", watchedRowOf(t, true, k(kindContent)), "q", true, false, ""},
		{"scoped member closed row passes the filter (the word needs the scope named)", watchedRowOf(t, true, k(kindRemoved)), "q", true, true, "changed"},
		{"scoped member entered row naming the scope passes the filter", watchedRowOf(t, true, k(kindEntered), enteredField("q")), "q", true, true, "added"},
		{"scoped non-member entered row naming the scope passes the filter", watchedRowOf(t, true, k(kindEntered), enteredField("q")), "q", false, true, "added"},
		{"scoped non-member flagged change", watchedRowOf(t, true, k(kindFlag)), "q", false, false, ""},
		{"scoped non-member closed row naming no scope", watchedRowOf(t, true, k(kindRemoved)), "q", false, false, ""},
		{"scoped member entered row naming another scope", watchedRowOf(t, true, k(kindEntered), enteredField("r")), "q", true, false, ""},
		{"a row without the snapshot never passes", watchedRowOf(t, false, k(kindFlag)), "", false, false, ""},
	})
}

func TestDeliverByWatchedWireWord(t *testing.T) {
	k := func(kinds ...string) []string { return kinds }
	runWatchedCases(t, viewAll, []watchedCase{
		{"the added role of a member is added", watchedRowOf(t, true, k(kindAdded)), "q", true, true, "added"},
		{"the added role of a non-member is not delivered to a scope", watchedRowOf(t, true, k(kindAdded)), "q", false, false, ""},
		{"unscoped added role", watchedRowOf(t, true, k(kindAdded)), "", false, true, "added"},
		{"unscoped entered kind", watchedRowOf(t, true, k(kindEntered)), "", false, true, "added"},
		{"unscoped left kind", watchedRowOf(t, true, k(kindLeft)), "", false, true, "removed"},
		{"unscoped removed role", watchedRowOf(t, true, k(kindRemoved)), "", false, true, "removed"},
		{"unscoped anything else", watchedRowOf(t, true, k(kindContent, kindFlag)), "", false, true, "changed"},
		{"scoped entered naming the scope", watchedRowOf(t, true, k(kindEntered), enteredField("q")), "q", true, true, "added"},
		{"scoped left naming the scope", watchedRowOf(t, true, k(kindLeft), leftField("q")), "q", false, true, "removed"},
		{"scoped removed naming the scope", watchedRowOf(t, true, k(kindRemoved), leftField("q")), "q", false, true, "removed"},
		{"scoped entered kind names no scope: changed", watchedRowOf(t, true, k(kindEntered)), "q", true, true, "changed"},
	})
}

// The snapshot decides first: a row whose snapshot is not set reaches nobody,
// member or not, scoped or not.
func TestDeliverDropsARowWhoseSnapshotIsNotSet(t *testing.T) {
	spec := testSpec()
	row := watchedRowOf(t, false, []string{kindContent})
	for _, scope := range []string{"", "q"} {
		for _, member := range []bool{false, true} {
			if ok, word := Deliver(spec, viewNamed(spec, viewAll), row, scope, member); ok || word != "" {
				t.Errorf("scope %q member %v: got (%v, %q), want (false, \"\")", scope, member, ok, word)
			}
		}
	}
}
