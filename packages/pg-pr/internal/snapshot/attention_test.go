package snapshot

import (
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/store"
)

// needsAttention is the ONE pure predicate consumed by BOTH the dashboard
// builder and the bead projector. Inputs are STORE-sourced revision facts plus
// the draft-review-bead-closed signal. It implements the §2.7 state machine,
// X3-correct (self approval never counts as "others approved").
func TestNeedsAttention(t *testing.T) {
	rev := func(seq int, head, myState string, othersApproved bool) store.Revision {
		return store.Revision{Seq: seq, HeadSHA: head, MyReviewState: myState, OthersApproved: othersApproved}
	}

	tests := []struct {
		name              string
		revs              []store.Revision
		draftReviewClosed bool
		hasConflict       bool
		wantNeed          bool
		wantReason        string
	}{
		{
			name:              "no revisions → no attention",
			revs:              nil,
			draftReviewClosed: true,
			wantNeed:          false,
		},
		{
			name:              "draft review ready + nobody approved + I haven't reviewed → NEEDS",
			revs:              []store.Revision{rev(1, "h1", "", false)},
			draftReviewClosed: true,
			wantNeed:          true,
			wantReason:        "draft-review-ready-unapproved",
		},
		{
			name:              "draft review NOT ready + nothing else → no attention",
			revs:              []store.Revision{rev(1, "h1", "", false)},
			draftReviewClosed: false,
			wantNeed:          false,
		},
		{
			name:              "teammate approved at head → NO attention (off the hook)",
			revs:              []store.Revision{rev(1, "h1", "", true)},
			draftReviewClosed: true, // even with review ready, teammate-approved wins
			wantNeed:          false,
		},
		{
			name:              "I reviewed the current head → NO attention",
			revs:              []store.Revision{rev(1, "h1", "approved", false)},
			draftReviewClosed: true,
			wantNeed:          false,
		},
		{
			name: "new commits after I approved (re-review) → NEEDS",
			revs: []store.Revision{
				rev(1, "h1", "approved", false), // I approved seq 1
				rev(2, "h2", "", false),         // new head, I have not reviewed
			},
			draftReviewClosed: false, // even without a fresh draft review, re-review is needed
			wantNeed:          true,
			wantReason:        "re-review-after-my-approval",
		},
		{
			name: "I approved an earlier head but teammate approved latest → NO attention",
			revs: []store.Revision{
				rev(1, "h1", "approved", false),
				rev(2, "h2", "", true), // teammate approved the new head
			},
			draftReviewClosed: true,
			wantNeed:          false,
		},
		{
			name: "I approved latest head (re-reviewed after advance) → NO attention",
			revs: []store.Revision{
				rev(1, "h1", "approved", false),
				rev(2, "h2", "approved", false),
			},
			draftReviewClosed: true,
			wantNeed:          false,
		},
		{
			name:              "changes-requested at head counts as I-reviewed-head → NO attention",
			revs:              []store.Revision{rev(1, "h1", "changes-requested", false)},
			draftReviewClosed: true,
			wantNeed:          false,
		},
		{
			name:              "conflicting → NO attention even though draft review is ready (dampened)",
			revs:              []store.Revision{rev(1, "h1", "", false)},
			draftReviewClosed: true,
			hasConflict:       true,
			wantNeed:          false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			need, reason := NeedsAttention(tc.revs, tc.draftReviewClosed, tc.hasConflict)
			if need != tc.wantNeed {
				t.Errorf("need = %v, want %v (reason=%q)", need, tc.wantNeed, reason)
			}
			if tc.wantNeed && reason != tc.wantReason {
				t.Errorf("reason = %q, want %q", reason, tc.wantReason)
			}
			if !need && reason != "" {
				t.Errorf("no-attention must carry empty reason, got %q", reason)
			}
		})
	}
}

// TestNeedsAttention_ConflictDampens is the dedicated regression guard for the
// pg2-tsgkj dampening rule: a conflicting team PR is not worth reviewing until
// the author rebases, so hasConflict short-circuits the predicate to
// (false, "") regardless of what the rest of the state machine would say.
func TestNeedsAttention_ConflictDampens(t *testing.T) {
	// A revision set that WOULD need attention (draft review ready, unapproved).
	revs := []store.Revision{{Seq: 1, HeadSHA: "h"}}
	if need, _ := NeedsAttention(revs, true, false); !need {
		t.Fatal("precondition: expected need=true without conflict")
	}
	if need, reason := NeedsAttention(revs, true, true); need || reason != "" {
		t.Errorf("with conflict: need=%v reason=%q, want false/\"\"", need, reason)
	}
}
