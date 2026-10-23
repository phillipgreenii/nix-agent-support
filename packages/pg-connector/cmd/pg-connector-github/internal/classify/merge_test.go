package classify

import (
	"slices"
	"testing"
)

func TestMergeUnionsKindsInCanonicalOrder(t *testing.T) {
	earlier := Result{Kinds: []string{KindRenamed, KindClosed}}
	later := Result{Kinds: []string{KindReopened, KindClosed, KindLeftQuery}}
	want := []string{KindReopened, KindClosed, KindRenamed, KindLeftQuery}
	if got := Merge(earlier, later); !slices.Equal(got.Kinds, want) {
		t.Fatalf("kinds = %v, want %v", got.Kinds, want)
	}
	if got := Merge(Result{}, Result{}); !got.Empty() {
		t.Fatalf("Merge of empties = %+v", got)
	}
}

func TestMembershipAndRemovedQueryFieldConvention(t *testing.T) {
	in := Membership(KindEnteredQuery, "mine")
	wantResult(t, in, []string{KindEnteredQuery}, []string{"query:mine"})
	if in.Fields[0].Before != nil || string(in.Fields[0].After) != `"mine"` {
		t.Errorf("entered_query = %+v", in.Fields[0])
	}
	out := Membership(KindLeftQuery, "mine")
	wantResult(t, out, []string{KindLeftQuery}, []string{"query:mine"})
	if string(out.Fields[0].Before) != `"mine"` || out.Fields[0].After != nil {
		t.Errorf("left_query = %+v", out.Fields[0])
	}
	if res := Membership(KindClosed, "mine"); !res.Empty() {
		t.Errorf("a non-membership kind produced %+v", res)
	}

	rm := Removed([]string{"b", "a", "b"})
	wantResult(t, rm, []string{KindRemoved}, []string{"query:a", "query:b"})
	for _, fl := range rm.Fields {
		if fl.After != nil || fl.Before == nil {
			t.Errorf("removed field %+v: Before must be set, After empty", fl)
		}
	}
	wantResult(t, Removed(nil), []string{KindRemoved}, nil)

	// Two queries never collapse in a merge by field name.
	both := Merge(Membership(KindEnteredQuery, "q1"), Membership(KindEnteredQuery, "q2"))
	wantResult(t, both, []string{KindEnteredQuery}, []string{"query:q1", "query:q2"})
	// An entered row and a left row for one query that merge cancel out the field.
	cancel := Merge(Membership(KindEnteredQuery, "q1"), Membership(KindLeftQuery, "q1"))
	wantResult(t, cancel, []string{KindEnteredQuery, KindLeftQuery}, nil)
}
