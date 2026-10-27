package executor

import (
	"context"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router-handlers/internal/beads"
)

// INV-CCH-22: a MOOT precheck outcome is accepted and completed as skipped; every
// other outcome stays a decline. This table drives the real precheck of each kind
// (review, ready, triage) into every reason the handler can report and pins the
// disposition: Skip.Moot is what dispatch.go reads to choose between the skipped
// completion and the busy decline. A new reason MUST be added here, and with it a
// decision, never a silent default (bead pg2-6t91v7).
func TestPrecheck_EveryReasonIsPinnedMootOrDecline(t *testing.T) {
	cfg := fastCfg()
	clk := time.Unix(0, 0)
	future := clk.Add(time.Hour).UTC().Format(time.RFC3339)
	deps := func(pr *fakePR) Deps { return Deps{Cfg: cfg, PR: pr, Now: func() time.Time { return clk }} }
	ready := func(f func(*beads.Issue)) *beads.Issue { i := groomed(); f(&i); return &i }
	triage := func(f func(*beads.Issue)) *beads.Issue { i := escalated(); f(&i); return &i }

	type run func(t *testing.T) (Skip, bool)
	review := func(bead *beads.Issue, pr *fakePR) run {
		return func(t *testing.T) (Skip, bool) {
			return PrecheckReview(context.Background(), DispatchContext{Role: pinnedHeadReviewRole(cfg), Item: reviewItem()}, deps(pr), bead)
		}
	}
	readyRun := func(bead *beads.Issue) run {
		return func(t *testing.T) (Skip, bool) {
			return PrecheckReady(DispatchContext{Role: readyRole(cfg), Item: reviewItem()}, deps(&fakePR{}), bead)
		}
	}
	triageRun := func(bead *beads.Issue) run {
		return func(t *testing.T) (Skip, bool) {
			return PrecheckTriage(DispatchContext{Role: triageRole(cfg), Item: reviewItem()}, deps(&fakePR{}), bead)
		}
	}

	tests := []struct {
		name       string
		run        run
		wantReason string
		wantMoot   bool
		retryable  bool
	}{
		// MOOT: completed as skipped.
		{"review: bead closed", review(closedBead(), &fakePR{}), SkipBeadClosed, true, false},
		{"review: PR merged", review(openBead(), &fakePR{merged: true}), SkipPRMerged, true, false},
		{"ready: bead closed", readyRun(ready(func(i *beads.Issue) { i.Status = "closed" })), SkipBeadClosed, true, false},
		{"triage: bead closed (the head-of-line jam of 2026-10-15)", triageRun(triage(func(i *beads.Issue) { i.Status = "closed" })), SkipBeadClosed, true, false},
		{"triage: bead no longer escalated", triageRun(triage(func(i *beads.Issue) { i.Labels = []string{"agent-support"} })), SkipBeadNotEscalated, true, false},

		// NOT moot: still declines, the work still exists.
		{"review: pending review holds the head", review(openBead(), &fakePR{pending: PendingReview{Pending: true}}), SkipPendingReview, false, false},
		{"review: connector unavailable", review(openBead(), &fakePR{mergedErr: ErrConnectorUnavailable}), ReasonConnectorUnavailable, false, true},
		{"ready: human", readyRun(ready(func(i *beads.Issue) { i.Labels = append(i.Labels, "human") })), SkipBeadHuman, false, false},
		{"ready: not groomed", readyRun(ready(func(i *beads.Issue) { i.Labels = []string{"x"} })), SkipBeadNotGroomed, false, false},
		{"ready: blocked", readyRun(ready(func(i *beads.Issue) { i.Status = "blocked" })), SkipBeadBlocked, false, false},
		{"ready: deferred", readyRun(ready(func(i *beads.Issue) { i.DeferUntil = future })), SkipBeadDeferred, false, false},
		{"ready: claimed by a peer", readyRun(ready(func(i *beads.Issue) { i.Assignee = "someone-else" })), SkipBeadClaimed, false, false},
		{"ready: not open (in_progress by a peer)", readyRun(ready(func(i *beads.Issue) { i.Status = "in_progress"; i.Assignee = "someone-else" })), SkipBeadNotOpen, false, false},
		{"triage: human", triageRun(triage(func(i *beads.Issue) { i.Labels = append(i.Labels, "human") })), SkipBeadHuman, false, false},
		{"triage: blocked", triageRun(triage(func(i *beads.Issue) { i.Status = "blocked" })), SkipBeadBlocked, false, false},
		{"triage: deferred", triageRun(triage(func(i *beads.Issue) { i.DeferUntil = future })), SkipBeadDeferred, false, false},
		{"triage: not open (in_progress)", triageRun(triage(func(i *beads.Issue) { i.Status = "in_progress" })), SkipBeadNotOpen, false, false},
	}
	seen := map[string]bool{}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, skipped := tc.run(t)
			if !skipped || got.Reason != tc.wantReason {
				t.Fatalf("precheck = (%+v, %v), want a skip with reason %q", got, skipped, tc.wantReason)
			}
			if got.Moot != tc.wantMoot || Moot(got.Reason) != tc.wantMoot {
				t.Errorf("reason %q: Skip.Moot = %v, Moot() = %v, want %v (moot = complete as skipped, else decline)", got.Reason, got.Moot, Moot(got.Reason), tc.wantMoot)
			}
			if got.Retryable != tc.retryable {
				t.Errorf("reason %q: Retryable = %v, want %v", got.Reason, got.Retryable, tc.retryable)
			}
			if got.Moot && got.Retryable {
				t.Errorf("a moot outcome must never be retryable: %+v", got)
			}
			seen[got.Reason] = true
		})
	}
	// Every reason constant is covered above and its disposition is the pinned one.
	for _, reason := range []string{
		SkipBeadClosed, SkipPRMerged, SkipBeadNotEscalated,
		SkipPendingReview, SkipBeadHuman, SkipBeadNotGroomed, SkipBeadBlocked,
		SkipBeadDeferred, SkipBeadClaimed, SkipBeadNotOpen, ReasonConnectorUnavailable,
	} {
		if !seen[reason] {
			t.Errorf("reason %q has no case in the table", reason)
		}
	}
	for reason, want := range map[string]bool{
		SkipBeadClosed: true, SkipPRMerged: true, SkipBeadNotEscalated: true,
		SkipPendingReview: false, SkipBeadHuman: false, SkipBeadNotGroomed: false, SkipBeadBlocked: false,
		SkipBeadDeferred: false, SkipBeadClaimed: false, SkipBeadNotOpen: false, ReasonConnectorUnavailable: false,
		"some-future-reason": false,
	} {
		if Moot(reason) != want {
			t.Errorf("Moot(%q) = %v, want %v", reason, Moot(reason), want)
		}
	}
}
