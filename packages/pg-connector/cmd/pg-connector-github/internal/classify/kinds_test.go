package classify

import (
	"slices"
	"testing"
)

func TestKindsAreTheSpecsFourteenInCanonicalOrder(t *testing.T) {
	want := []string{
		"reopened", "closed", "merged", "draft_changed", "head_changed", "base_changed",
		"ci_changed", "mergeability_changed", "review_changed", "feedback_changed",
		"renamed", "removed", "entered_query", "left_query",
	}
	if got := Kinds(); !slices.Equal(got, want) {
		t.Fatalf("Kinds() = %v, want %v", got, want)
	}
	consts := []string{
		KindReopened, KindClosed, KindMerged, KindDraftChanged, KindHeadChanged, KindBaseChanged,
		KindCIChanged, KindMergeabilityChanged, KindReviewChanged, KindFeedbackChanged,
		KindRenamed, KindRemoved, KindEnteredQuery, KindLeftQuery,
	}
	if !slices.Equal(consts, want) {
		t.Fatalf("constants = %v, want %v", consts, want)
	}
	// The caller owns the slice.
	k := Kinds()
	k[0] = "mutated"
	if Kinds()[0] != KindReopened {
		t.Fatal("Kinds() returned a shared slice")
	}
}

func TestResultEmpty(t *testing.T) {
	if !(Result{}).Empty() {
		t.Error("zero Result is not Empty")
	}
	if (Result{Kinds: []string{KindClosed}}).Empty() {
		t.Error("a Result with a kind is Empty")
	}
	if (Result{Fields: []Field{{Name: "x"}}}).Empty() {
		t.Error("a Result with a field is Empty")
	}
}

func TestOrderKinds(t *testing.T) {
	got := orderKinds([]string{KindLeftQuery, KindClosed, "zzz", KindClosed, KindReopened, "aaa"})
	want := []string{KindReopened, KindClosed, KindLeftQuery, "aaa", "zzz"}
	if !slices.Equal(got, want) {
		t.Fatalf("orderKinds = %v, want %v", got, want)
	}
	if orderKinds(nil) != nil {
		t.Error("orderKinds(nil) is not nil")
	}
}
