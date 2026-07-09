package sync

import "testing"

// TestReviewRequestedOfSelf: the sync layer derives ReviewRequestedOfMe from the
// provider's raw RequestedReviewers against the configured self login.
func TestReviewRequestedOfSelf(t *testing.T) {
	if !reviewRequestedOfSelf("me", []string{"you", "me"}) {
		t.Error("self among requested reviewers should be true")
	}
	if reviewRequestedOfSelf("me", []string{"you"}) {
		t.Error("self not requested should be false")
	}
	if reviewRequestedOfSelf("", []string{"me"}) {
		t.Error("empty self should be false")
	}
	if reviewRequestedOfSelf("me", nil) {
		t.Error("no requested reviewers should be false")
	}
}
