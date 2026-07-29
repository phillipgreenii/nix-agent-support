package snapshot

import (
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/store"
)

// NeedsAttention is the ONE pure predicate consumed by BOTH the dashboard
// builder and the bead projector. Inputs are STORE-sourced revision facts plus
// the live merge-conflict flag — no bead artifact (pg2-kh1ar). It implements the
// §2.7 state machine, X3-correct (self approval never counts as "others
// approved").
func TestNeedsAttention(t *testing.T) {
	rev := func(seq int, head, myState string, othersApproved bool) store.Revision {
		return store.Revision{Seq: seq, HeadSHA: head, MyReviewState: myState, OthersApproved: othersApproved}
	}

	tests := []struct {
		name        string
		revs        []store.Revision
		hasConflict bool
		wantNeed    bool
		wantReason  string
	}{
		{
			name:     "no revisions → no attention",
			revs:     nil,
			wantNeed: false,
		},
		{
			name:       "nobody approved + I have never reviewed → NEEDS a first review",
			revs:       []store.Revision{rev(1, "h1", "", false)},
			wantNeed:   true,
			wantReason: "unreviewed-by-me",
		},
		{
			name:       "still unreviewed across several heads → NEEDS a first review",
			revs:       []store.Revision{rev(1, "h1", "", false), rev(2, "h2", "", false)},
			wantNeed:   true,
			wantReason: "unreviewed-by-me",
		},
		{
			name:     "teammate approved at head → NO attention (off the hook)",
			revs:     []store.Revision{rev(1, "h1", "", true)},
			wantNeed: false,
		},
		{
			name:     "I reviewed the current head → NO attention",
			revs:     []store.Revision{rev(1, "h1", "approved", false)},
			wantNeed: false,
		},
		{
			name: "new commits after I approved (re-review) → NEEDS",
			revs: []store.Revision{
				rev(1, "h1", "approved", false), // I approved seq 1
				rev(2, "h2", "", false),         // new head, I have not reviewed
			},
			wantNeed:   true,
			wantReason: "re-review-after-my-approval",
		},
		{
			name: "I approved an earlier head but teammate approved latest → NO attention",
			revs: []store.Revision{
				rev(1, "h1", "approved", false),
				rev(2, "h2", "", true), // teammate approved the new head
			},
			wantNeed: false,
		},
		{
			name: "I approved latest head (re-reviewed after advance) → NO attention",
			revs: []store.Revision{
				rev(1, "h1", "approved", false),
				rev(2, "h2", "approved", false),
			},
			wantNeed: false,
		},
		{
			name:     "changes-requested at head counts as I-reviewed-head → NO attention",
			revs:     []store.Revision{rev(1, "h1", "changes-requested", false)},
			wantNeed: false,
		},
		{
			name:        "conflicting → NO attention even though I never reviewed it (dampened)",
			revs:        []store.Revision{rev(1, "h1", "", false)},
			hasConflict: true,
			wantNeed:    false,
		},
		{
			name: "conflicting → NO attention even on the re-review edge (dampened)",
			revs: []store.Revision{
				rev(1, "h1", "approved", false),
				rev(2, "h2", "", false),
			},
			hasConflict: true,
			wantNeed:    false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			need, reason := NeedsAttention(tc.revs, tc.hasConflict)
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

// TestNeedsAttention_FirstReviewEdgeIsReachable is the regression guard for
// pg2-kh1ar: the first-review edge — "a teammate PR I have never reviewed" —
// MUST fire from revision facts ALONE. It previously required a CLOSED pg-pr
// draft-review bead, an artifact only the legacy review path produces, and that
// path ships OFF (config.ReviewEnabled defaults false; ADR 0034) — so the edge
// was dead code. The predicate now takes no such input at all, which is what
// makes the edge reachable at the shipped default.
func TestNeedsAttention_FirstReviewEdgeIsReachable(t *testing.T) {
	// The minimal teammate-PR fact set: one observed head, nobody approved, no
	// review by me, no conflict. Nothing bead-derived is available to feed in.
	revs := []store.Revision{{Seq: 1, HeadSHA: "h1"}}
	need, reason := NeedsAttention(revs, false)
	if !need {
		t.Fatal("a teammate PR with no prior review by me MUST need attention")
	}
	if reason != AttentionReasonUnreviewed {
		t.Errorf("reason = %q, want %q", reason, AttentionReasonUnreviewed)
	}
}

// TestNeedsAttention_ConflictDampens is the dedicated regression guard for the
// pg2-tsgkj dampening rule: a conflicting team PR is not worth reviewing until
// the author rebases, so hasConflict short-circuits the predicate to
// (false, "") regardless of what the rest of the state machine would say.
func TestNeedsAttention_ConflictDampens(t *testing.T) {
	// A revision set that WOULD need attention (unapproved, unreviewed by me).
	revs := []store.Revision{{Seq: 1, HeadSHA: "h"}}
	if need, _ := NeedsAttention(revs, false); !need {
		t.Fatal("precondition: expected need=true without conflict")
	}
	if need, reason := NeedsAttention(revs, true); need || reason != "" {
		t.Errorf("with conflict: need=%v reason=%q, want false/\"\"", need, reason)
	}
}
