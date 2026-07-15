package sync

import "testing"

// TestReviewHookEnabled_GatedByLiveConfig documents the review kill switch as
// re-worked by bead pg2-bw30: the hook is gated by the LIVE review.enabled value
// (read per poll from the engine's config), not by whether SetReviewHook was
// called once at startup. The deps are wired unconditionally; the config gate
// decides — and re-decides each poll — whether the hook runs.
func TestReviewHookEnabled_GatedByLiveConfig(t *testing.T) {
	// A bare Engine (no config, no deps) reports disabled.
	if (&Engine{}).reviewHookEnabled() {
		t.Fatal("a bare Engine must report reviewHookEnabled()==false")
	}

	// Deps wired but review.enabled=false: still off.
	e := &Engine{}
	e.ReplaceCfg(cfgWithReview(false))
	e.SetReviewHook(ReviewHookDeps{Beads: &fakeReviewBeads{}, Spawner: &fakeSpawner{}})
	if e.reviewHookEnabled() {
		t.Fatal("deps wired but review.enabled=false must report false")
	}

	// Flip review.enabled=true on the live config (no re-wiring, no restart).
	e.ReplaceCfg(cfgWithReview(true))
	if !e.reviewHookEnabled() {
		t.Fatal("review.enabled=true with deps wired must report true")
	}

	// Flip it back off: the next poll observes the change and disables the hook.
	e.ReplaceCfg(cfgWithReview(false))
	if e.reviewHookEnabled() {
		t.Fatal("review.enabled=false must disable the hook even after it was enabled")
	}
}
