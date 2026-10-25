package classify

import (
	"slices"
	"testing"
)

func TestKindsAreTheCanonicalSixteenInOrder(t *testing.T) {
	want := []string{
		"closed", "reopened", "status_changed", "assignee_changed", "priority_changed",
		"labels_changed", "parent_changed", "title_changed", "description_changed",
		"deps_changed", "comments_changed", "renamed", "removed", "fields_changed",
		"entered_query", "left_query",
	}
	if got := Kinds(); !slices.Equal(got, want) {
		t.Fatalf("Kinds() = %v, want %v", got, want)
	}
	consts := []string{
		KindClosed, KindReopened, KindStatusChanged, KindAssigneeChanged, KindPriorityChanged,
		KindLabelsChanged, KindParentChanged, KindTitleChanged, KindDescriptionChanged,
		KindDepsChanged, KindCommentsChanged, KindRenamed, KindRemoved, KindFieldsChanged,
		KindEnteredQuery, KindLeftQuery,
	}
	if !slices.Equal(consts, want) {
		t.Fatalf("constants = %v, want %v", consts, want)
	}
}

func TestKindAddedIsTheOneFirstObservationKindAndOpenedDoesNotExist(t *testing.T) {
	if KindAdded != "added" {
		t.Fatalf("KindAdded = %q", KindAdded)
	}
	for _, k := range Kinds() {
		if k == KindAdded {
			t.Fatal("Kinds() lists the first-observation kind; the changes packet appends it")
		}
		if k == "opened" {
			t.Fatal("there is no opened kind")
		}
	}
}

func TestKindsReturnsAnOwnedSlice(t *testing.T) {
	k := Kinds()
	k[0] = "mutated"
	if Kinds()[0] != KindClosed {
		t.Fatal("Kinds() returned a shared slice")
	}
}
