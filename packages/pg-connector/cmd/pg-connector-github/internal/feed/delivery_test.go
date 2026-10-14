package feed

import (
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/classify"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

func rowOf(t *testing.T, kinds []string, fields ...classify.Field) store.ChangeRow {
	t.Helper()
	raw, err := classify.EncodeFields(fields)
	if err != nil {
		t.Fatal(err)
	}
	return store.ChangeRow{Kind: "pr", ID: refA, Kinds: kinds, Fields: raw}
}

func TestDeliveryForPRKey(t *testing.T) {
	k := func(kinds ...string) []string { return kinds }
	for _, tc := range []struct {
		name    string
		row     store.ChangeRow
		member  bool
		deliver bool
		change  string
	}{
		{"member plain change", rowOf(t, k(classify.KindHeadChanged)), true, true, "changed"},
		{"member empty kinds", rowOf(t, nil), true, true, "changed"},
		{"non-member plain change", rowOf(t, k(classify.KindHeadChanged)), false, false, ""},
		{"entered naming the query", rowOf(t, k(classify.KindEnteredQuery), enteredField("q")), true, true, "added"},
		{"entered naming another query", rowOf(t, k(classify.KindEnteredQuery), enteredField("r")), true, true, "changed"},
		{"entered naming another query, non-member", rowOf(t, k(classify.KindEnteredQuery), enteredField("r")), false, false, ""},
		{"left naming the query", rowOf(t, k(classify.KindLeftQuery), leftField("q")), false, true, "removed"},
		{"left naming the query, still a member", rowOf(t, k(classify.KindLeftQuery), leftField("q")), true, true, "removed"},
		{"left naming another query", rowOf(t, k(classify.KindLeftQuery), leftField("r")), false, false, ""},
		{"removed naming the query", rowOf(t, k(classify.KindRemoved), leftField("r"), leftField("q")), false, true, "removed"},
		{"removed naming another query", rowOf(t, k(classify.KindRemoved), leftField("r")), false, false, ""},
		{"entered then left in one window: no query field", rowOf(t, k(classify.KindEnteredQuery, classify.KindLeftQuery)), false, false, ""},
		{"entered then left in one window, member", rowOf(t, k(classify.KindEnteredQuery, classify.KindLeftQuery)), true, true, "changed"},
		{"entered with a head change", rowOf(t, k(classify.KindHeadChanged, classify.KindEnteredQuery), enteredField("q")), true, true, "added"},
		{"the field is on the wrong side", rowOf(t, k(classify.KindLeftQuery), enteredField("q")), false, false, ""},
		{"a membership kind without fields", rowOf(t, k(classify.KindRemoved)), false, false, ""},
		{"undecodable fields", store.ChangeRow{Kinds: k(classify.KindRemoved), Fields: []byte("{")}, false, false, ""},
	} {
		deliver, change := DeliveryFor(tc.row, "q", tc.member)
		if deliver != tc.deliver || change != tc.change {
			t.Errorf("%s: got (%v, %q), want (%v, %q)", tc.name, deliver, change, tc.deliver, tc.change)
		}
	}
}

func TestDeliveryForCIKey(t *testing.T) {
	k := func(kinds ...string) []string { return kinds }
	for _, tc := range []struct {
		name    string
		row     store.ChangeRow
		member  bool
		deliver bool
		change  string
	}{
		{"member ci change", rowOf(t, k(classify.KindCIChanged)), true, true, "changed"},
		{"member head change only", rowOf(t, k(classify.KindHeadChanged)), true, false, ""},
		{"non-member ci change", rowOf(t, k(classify.KindCIChanged)), false, false, ""},
		{"member head and ci change", rowOf(t, k(classify.KindHeadChanged, classify.KindCIChanged)), true, true, "changed"},
		{"entered naming the query", rowOf(t, k(classify.KindEnteredQuery), enteredField("q")), true, true, "added"},
		{"left naming the query", rowOf(t, k(classify.KindLeftQuery), leftField("q")), false, true, "removed"},
		{"removed naming the query", rowOf(t, k(classify.KindRemoved), leftField("q")), false, true, "removed"},
		{"entered naming another query", rowOf(t, k(classify.KindEnteredQuery), enteredField("r")), true, false, ""},
		{"removed naming another query", rowOf(t, k(classify.KindRemoved), leftField("r")), false, false, ""},
	} {
		deliver, change := DeliveryForKey(KindCI, tc.row, "q", tc.member)
		if deliver != tc.deliver || change != tc.change {
			t.Errorf("%s: got (%v, %q), want (%v, %q)", tc.name, deliver, change, tc.deliver, tc.change)
		}
	}
}

func TestRefFoldIgnoresTheCaseOfOwnerAndRepoOnly(t *testing.T) {
	m := newMemberSet([]string{"Acme-Org/Widget#3"})
	if !m.has("acme-org/widget#3") {
		t.Error("owner/repo case must not matter")
	}
	if m.has("acme-org/widget#4") || m.has("acme-org/widget") {
		t.Error("the number is exact")
	}
}
