package feed

import (
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/engine"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/registry"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

func rowOf(t *testing.T, kinds []string, fields ...engine.Field) store.ChangeRow {
	t.Helper()
	raw, err := engine.EncodeFields(fields)
	if err != nil {
		t.Fatal(err)
	}
	return store.ChangeRow{Kind: entityKind, ID: refA, Kinds: kinds, Fields: raw, Watched: true}
}

func viewNamed(spec registry.Spec, name string) registry.View {
	for _, v := range spec.Views {
		if v.Name == name {
			return v
		}
	}
	panic("no view " + name)
}

type deliveryCase struct {
	name    string
	row     store.ChangeRow
	member  bool
	deliver bool
	change  string
}

func runDeliveryCases(t *testing.T, view string, cases []deliveryCase) {
	t.Helper()
	spec := testSpec()
	for _, tc := range cases {
		deliver, change := Deliver(spec, viewNamed(spec, view), tc.row, "q", tc.member)
		if deliver != tc.deliver || change != tc.change {
			t.Errorf("%s: got (%v, %q), want (%v, %q)", tc.name, deliver, change, tc.deliver, tc.change)
		}
	}
}

// The scoped table of the unfiltered view, for rows whose snapshot is set.
func TestDeliverScopedTableOfTheUnfilteredView(t *testing.T) {
	k := func(kinds ...string) []string { return kinds }
	runDeliveryCases(t, viewAll, []deliveryCase{
		{"member plain change", rowOf(t, k(kindContent)), true, true, "changed"},
		{"member empty kinds", rowOf(t, nil), true, true, "changed"},
		{"non-member plain change", rowOf(t, k(kindContent)), false, false, ""},
		{"entered naming the scope", rowOf(t, k(kindEntered), enteredField("q")), true, true, "added"},
		{"entered naming the scope, non-member", rowOf(t, k(kindEntered), enteredField("q")), false, true, "added"},
		{"entered naming another scope", rowOf(t, k(kindEntered), enteredField("r")), true, true, "changed"},
		{"entered naming another scope, non-member", rowOf(t, k(kindEntered), enteredField("r")), false, false, ""},
		{"left naming the scope", rowOf(t, k(kindLeft), leftField("q")), false, true, "removed"},
		{"left naming another scope", rowOf(t, k(kindLeft), leftField("r")), false, false, ""},
		{"removed naming the scope", rowOf(t, k(kindRemoved), leftField("r"), leftField("q")), false, true, "removed"},
		{"removed naming another scope", rowOf(t, k(kindRemoved), leftField("r")), false, false, ""},
		{"entered then left in one window: no scope field", rowOf(t, k(kindEntered, kindLeft)), false, false, ""},
		{"entered then left in one window, member", rowOf(t, k(kindEntered, kindLeft)), true, true, "changed"},
		{"entered with a content change", rowOf(t, k(kindContent, kindEntered), enteredField("q")), true, true, "added"},
		{"the field is on the wrong side", rowOf(t, k(kindLeft), enteredField("q")), false, false, ""},
		{"a membership kind without fields", rowOf(t, k(kindRemoved)), false, false, ""},
		{"undecodable fields", store.ChangeRow{Kinds: k(kindRemoved), Fields: []byte("{")}, false, false, ""},
		{"the added role of a member", rowOf(t, k(kindAdded)), true, true, "added"},
		{"the added role of a non-member", rowOf(t, k(kindAdded)), false, false, ""},
	})
}

// The scoped table of a view that filters by kind, for rows whose snapshot is set.
func TestDeliverScopedTableOfTheFilteredView(t *testing.T) {
	k := func(kinds ...string) []string { return kinds }
	runDeliveryCases(t, viewFlagged, []deliveryCase{
		{"member flagged change", rowOf(t, k(kindFlag)), true, true, "changed"},
		{"member content change only", rowOf(t, k(kindContent)), true, false, ""},
		{"non-member flagged change", rowOf(t, k(kindFlag)), false, false, ""},
		{"member content and flagged change", rowOf(t, k(kindContent, kindFlag)), true, true, "changed"},
		{"entered naming the scope", rowOf(t, k(kindEntered), enteredField("q")), true, true, "added"},
		{"left naming the scope", rowOf(t, k(kindLeft), leftField("q")), false, true, "removed"},
		{"removed naming the scope", rowOf(t, k(kindRemoved), leftField("q")), false, true, "removed"},
		{"entered naming another scope", rowOf(t, k(kindEntered), enteredField("r")), true, false, ""},
		{"removed naming another scope", rowOf(t, k(kindRemoved), leftField("r")), false, false, ""},
	})
}

func TestDeliverWithoutAScopeNamesTheWordFromTheKinds(t *testing.T) {
	spec := testSpec()
	k := func(kinds ...string) []string { return kinds }
	for _, tc := range []struct {
		name string
		row  store.ChangeRow
		word string
	}{
		{"entered", rowOf(t, k(kindEntered)), "added"},
		{"added role", rowOf(t, k(kindAdded)), "added"},
		{"left", rowOf(t, k(kindLeft)), "removed"},
		{"removed", rowOf(t, k(kindRemoved)), "removed"},
		{"anything else", rowOf(t, k(kindContent)), "changed"},
	} {
		ok, word := Deliver(spec, viewNamed(spec, viewAll), tc.row, "", true)
		if !ok || word != tc.word {
			t.Errorf("%s: got (%v, %q), want (true, %q)", tc.name, ok, word, tc.word)
		}
	}
}

func TestAConnectorWithoutMembershipRolesNeverNamesAScope(t *testing.T) {
	spec := testSpec()
	spec.Membership = registry.MembershipPolicy{}
	spec.Roles.EnteredQuery, spec.Roles.LeftQuery = "", ""
	row := rowOf(t, []string{kindContent}, enteredField("q"))
	if ok, word := Deliver(spec, viewNamed(spec, viewAll), row, "q", false); ok || word != "" {
		t.Fatalf("non-member = (%v, %q)", ok, word)
	}
	if ok, word := Deliver(spec, viewNamed(spec, viewAll), row, "q", true); !ok || word != "changed" {
		t.Fatalf("member = (%v, %q)", ok, word)
	}
}

func TestAMemberSetFoldsThroughTheSpecAndKeepsTheNumberExact(t *testing.T) {
	m := newMemberSet([]string{"Example-Org/Widget#3"}, foldRef)
	if !m.has("example-org/widget#3") {
		t.Error("the fold of the spec must apply to both sides")
	}
	if m.has("example-org/widget#4") || m.has("example-org/widget") {
		t.Error("the number is exact")
	}
	id := newMemberSet([]string{"Same#1"}, nil)
	if !id.has("Same#1") || id.has("same#1") {
		t.Error("a nil fold is the identity")
	}
	var zero memberSet
	if zero.has("x") {
		t.Error("the zero set has no member")
	}
}
