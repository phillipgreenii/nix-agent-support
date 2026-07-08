package sync

import "testing"

// TestReviewHookEnabled_KillSwitchMechanism documents the foundation of the
// review kill switch (bead pg2-ynhr.11): the daemon skips SetReviewHook when
// review.enabled=false. An Engine whose hook was never wired must therefore
// report reviewHookEnabled()==false (so no reviewHookCycle runs); wiring both
// deps (the review.enabled=true path) flips it true.
func TestReviewHookEnabled_KillSwitchMechanism(t *testing.T) {
	if (&Engine{}).reviewHookEnabled() {
		t.Fatal("a fresh Engine (SetReviewHook not called) must report reviewHookEnabled()==false")
	}

	e := &Engine{}
	e.SetReviewHook(ReviewHookDeps{Beads: &fakeReviewBeads{}, Spawner: &fakeSpawner{}})
	if !e.reviewHookEnabled() {
		t.Fatal("with Beads+Spawner wired, reviewHookEnabled() must be true")
	}
}
