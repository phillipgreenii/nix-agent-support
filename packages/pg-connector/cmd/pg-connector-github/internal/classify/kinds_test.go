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
