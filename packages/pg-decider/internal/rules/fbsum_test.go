package rules

import (
	"reflect"
	"testing"
)

// The expected digests were computed once by running pg-desk's
// packages/pg-desk/internal/sync fbsumDigest over the same id sets.
func TestFbsumDigestMatchesPortedPgDeskFunction(t *testing.T) {
	for _, tc := range []struct {
		ids  []string
		want string
	}{
		{[]string{"c-100"}, "f68d0c1f2f64"},
		{[]string{"c-101", "c-102"}, "5ff7c41ebe23"},
		{[]string{"c-101", "c-102", "c-103"}, "15b98a689116"},
		{[]string{"c-101", "c-103"}, "234aa3d824fd"},
		{[]string{"c-102"}, "de7ef7ee219f"},
		{[]string{"c-102", "c-103"}, "7ef2615a1894"},
		{[]string{"c-881", "c-902"}, "d1a29da50087"},
	} {
		got := fbsumDigest(tc.ids)
		if got != tc.want || len(got) != 12 {
			t.Errorf("fbsumDigest(%v) = %q, want %q", tc.ids, got, tc.want)
		}
	}
}

func TestFbsumDigestEmptyForNoIDs(t *testing.T) {
	if got := fbsumDigest(nil); got != "" {
		t.Errorf("nil: %q", got)
	}
	if got := fbsumDigest([]string{}); got != "" {
		t.Errorf("empty: %q", got)
	}
}

func TestFbsumStaleLabelsKeepsOnlyOtherDigests(t *testing.T) {
	got := fbsumStaleLabels([]string{"mine", "fbsum:old1", "fbsum:cur", "fbsum:old2", "human"}, "cur")
	want := []string{"fbsum:old1", "fbsum:old2"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if got := fbsumStaleLabels([]string{"mine"}, "cur"); len(got) != 0 {
		t.Errorf("no fbsum labels: %v", got)
	}
}

func TestFbsumCycleDescriptionRendersUnaddressedItems(t *testing.T) {
	got := fbsumCycleDescription("acme/widgets", 42, []string{"c-101", "c-102"})
	want := "Unaddressed reviewer feedback on acme/widgets#42.\n\n2 unaddressed item(s): c-101, c-102."
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
