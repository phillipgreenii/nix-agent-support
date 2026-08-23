package snapshot

import (
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/store"
)

const (
	attnSelf     = "me"
	attnTeammate = "teammate"
)

// attnRev is one observed head; the predicate reads the timeline only for "was
// anything observed" and "which head is current" (the last element).
func attnRev(seq int, head string) store.Revision {
	return store.Revision{Seq: seq, HeadSHA: head}
}

// attnAppr is one per-approver row: pr_approval is UNIQUE (pr_id, approver), so
// a login appears at most once and the row IS that approver's latest observed
// review.
func attnAppr(login, state, head string) store.Approval {
	return store.Approval{Approver: login, State: state, HeadSHA: head}
}

// attnDismissed is an approval the code host reported as DISMISSED: state stays
// "approved" (the approver DID approve) and the dismissal marks it STALE
// regardless of head (INV-APPROVAL-3, store.Approval.IsStale).
func attnDismissed(login, head string) store.Approval {
	return store.Approval{Approver: login, State: "approved", HeadSHA: head, Dismissed: true}
}

// NeedsAttention is the ONE pure predicate consumed by BOTH the dashboard
// builder and the bead projector. Inputs are STORE-sourced facts plus the live
// merge-conflict flag — no bead artifact (pg2-kh1ar). Since pg2-4dz88.1.9 the
// approval source is the PER-APPROVER pr_approval rows, not the collapsed
// pr_revision.others_approved / my_review_state pair; the §2.7 state machine
// itself is unchanged, and stays X3-correct (my own row is matched by login and
// never counts as "someone else approved").
func TestNeedsAttention(t *testing.T) {
	tests := []struct {
		name        string
		revs        []store.Revision
		approvals   []store.Approval
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
			revs:       []store.Revision{attnRev(1, "h1")},
			wantNeed:   true,
			wantReason: "unreviewed-by-me",
		},
		{
			name:       "still unreviewed across several heads → NEEDS a first review",
			revs:       []store.Revision{attnRev(1, "h1"), attnRev(2, "h2")},
			wantNeed:   true,
			wantReason: "unreviewed-by-me",
		},
		{
			name:      "teammate approved at head → NO attention (off the hook)",
			revs:      []store.Revision{attnRev(1, "h1")},
			approvals: []store.Approval{attnAppr(attnTeammate, "approved", "h1")},
			wantNeed:  false,
		},
		{
			name:      "I reviewed the current head → NO attention",
			revs:      []store.Revision{attnRev(1, "h1")},
			approvals: []store.Approval{attnAppr(attnSelf, "approved", "h1")},
			wantNeed:  false,
		},
		{
			name: "new commits after I approved (re-review) → NEEDS",
			revs: []store.Revision{attnRev(1, "h1"), attnRev(2, "h2")},
			// My row still points at h1; the current head is h2.
			approvals:  []store.Approval{attnAppr(attnSelf, "approved", "h1")},
			wantNeed:   true,
			wantReason: "re-review-after-my-approval",
		},
		{
			name: "I approved an earlier head but teammate approved latest → NO attention",
			revs: []store.Revision{attnRev(1, "h1"), attnRev(2, "h2")},
			approvals: []store.Approval{
				attnAppr(attnSelf, "approved", "h1"),
				attnAppr(attnTeammate, "approved", "h2"),
			},
			wantNeed: false,
		},
		{
			name:      "I approved latest head (re-reviewed after advance) → NO attention",
			revs:      []store.Revision{attnRev(1, "h1"), attnRev(2, "h2")},
			approvals: []store.Approval{attnAppr(attnSelf, "approved", "h2")},
			wantNeed:  false,
		},
		{
			name:      "changes-requested at head counts as I-reviewed-head → NO attention",
			revs:      []store.Revision{attnRev(1, "h1")},
			approvals: []store.Approval{attnAppr(attnSelf, "changes-requested", "h1")},
			wantNeed:  false,
		},
		{
			name:        "conflicting → NO attention even though I never reviewed it (dampened)",
			revs:        []store.Revision{attnRev(1, "h1")},
			hasConflict: true,
			wantNeed:    false,
		},
		{
			name:        "conflicting → NO attention even on the re-review edge (dampened)",
			revs:        []store.Revision{attnRev(1, "h1"), attnRev(2, "h2")},
			approvals:   []store.Approval{attnAppr(attnSelf, "approved", "h1")},
			hasConflict: true,
			wantNeed:    false,
		},

		// --- cases the collapsed booleans could not represent (pg2-4dz88.1.9) ---
		{
			name: "DISMISSED-only teammate approval → NEEDS, exactly as if unapproved",
			revs: []store.Revision{attnRev(1, "h1")},
			// Dismissed AT the current head: nothing about the head changed, so
			// head comparison alone cannot tell this from a standing approval.
			approvals:  []store.Approval{attnDismissed(attnTeammate, "h1")},
			wantNeed:   true,
			wantReason: "unreviewed-by-me",
		},
		{
			name:       "my own approval DISMISSED at the current head → NEEDS a re-review",
			revs:       []store.Revision{attnRev(1, "h1")},
			approvals:  []store.Approval{attnDismissed(attnSelf, "h1")},
			wantNeed:   true,
			wantReason: "re-review-after-my-approval",
		},
		{
			name:      "a re-approval after a dismissal closes the edge again",
			revs:      []store.Revision{attnRev(1, "h1")},
			approvals: []store.Approval{attnAppr(attnTeammate, "approved", "h1")},
			wantNeed:  false,
		},
		{
			name:       "teammate asked for CHANGES at head → NEEDS (not an approval)",
			revs:       []store.Revision{attnRev(1, "h1")},
			approvals:  []store.Approval{attnAppr(attnTeammate, "changes-requested", "h1")},
			wantNeed:   true,
			wantReason: "unreviewed-by-me",
		},
		{
			name:       "teammate approved an EARLIER head → NEEDS (their approval is stale)",
			revs:       []store.Revision{attnRev(1, "h1"), attnRev(2, "h2")},
			approvals:  []store.Approval{attnAppr(attnTeammate, "approved", "h1")},
			wantNeed:   true,
			wantReason: "unreviewed-by-me",
		},
		{
			name: "two teammates, one dismissed and one standing → NO attention",
			revs: []store.Revision{attnRev(1, "h1")},
			approvals: []store.Approval{
				attnDismissed("teammate-a", "h1"),
				attnAppr("teammate-b", "approved", "h1"),
			},
			wantNeed: false,
		},
		{
			name: "two teammates, BOTH dismissed → NEEDS",
			revs: []store.Revision{attnRev(1, "h1")},
			approvals: []store.Approval{
				attnDismissed("teammate-a", "h1"),
				attnDismissed("teammate-b", "h1"),
			},
			wantNeed:   true,
			wantReason: "unreviewed-by-me",
		},
		{
			// Self-matching is EQUALITY on the login, not an ordering. "alice"
			// sorts BEFORE self ("me") where every other teammate fixture here
			// sorts after, so this fixture is the one an ordered comparison gets
			// wrong: read as my own row, a changes-requested review would close
			// the edge.
			name:       "a teammate sorting before self is still a teammate",
			revs:       []store.Revision{attnRev(1, "h1")},
			approvals:  []store.Approval{attnAppr("alice", "changes-requested", "h1")},
			wantNeed:   true,
			wantReason: "unreviewed-by-me",
		},
		{
			// Only the exact state "approved" is an approval. All three states
			// the schema allows sort at or after "approved", so an ordering
			// comparison is indistinguishable from equality over them; an
			// unrecognized state sorting BEFORE it is what separates the two.
			name:       "an unrecognized teammate state is not an approval",
			revs:       []store.Revision{attnRev(1, "h1")},
			approvals:  []store.Approval{attnAppr(attnTeammate, "APPROVED", "h1")},
			wantNeed:   true,
			wantReason: "unreviewed-by-me",
		},
		{
			name: "my own row is never a teammate approval (X3)",
			revs: []store.Revision{attnRev(1, "h1"), attnRev(2, "h2")},
			// Only my own STALE approval exists. If self-matching were broken it
			// would read as a teammate approval of h1 — still stale, so the guard
			// is the REASON: a teammate reading would give unreviewed-by-me.
			approvals:  []store.Approval{attnAppr(attnSelf, "approved", "h1")},
			wantNeed:   true,
			wantReason: "re-review-after-my-approval",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			need, reason := NeedsAttention(tc.revs, tc.approvals, attnSelf, tc.hasConflict)
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
// MUST fire from persisted facts ALONE. It previously required a CLOSED pg-pr
// draft-review bead, an artifact only the legacy review path produces, and that
// path ships OFF (config.ReviewEnabled defaults false; ADR 0034) — so the edge
// was dead code. The predicate now takes no such input at all, which is what
// makes the edge reachable at the shipped default.
func TestNeedsAttention_FirstReviewEdgeIsReachable(t *testing.T) {
	// The minimal teammate-PR fact set: one observed head, no approval rows at
	// all, no conflict. Nothing bead-derived is available to feed in.
	revs := []store.Revision{{Seq: 1, HeadSHA: "h1"}}
	need, reason := NeedsAttention(revs, nil, attnSelf, false)
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
	if need, _ := NeedsAttention(revs, nil, attnSelf, false); !need {
		t.Fatal("precondition: expected need=true without conflict")
	}
	if need, reason := NeedsAttention(revs, nil, attnSelf, true); need || reason != "" {
		t.Errorf("with conflict: need=%v reason=%q, want false/\"\"", need, reason)
	}
}

// TestNeedsAttention_DismissedApprovalDoesNotClose is the dedicated regression
// guard for the case the per-approver rewrite exists to make possible
// (pg2-4dz88.1.9). A two-way boolean could never distinguish it: with the
// dismissal happening at the CURRENT head, an approval that STANDS and one that
// was DISMISSED are the same (head, approved) pair, and the only thing that
// separates them is the per-approval dismissal marker.
//
// The assertion is symmetric on purpose — the SAME row, differing only in
// Dismissed, must flip the verdict — so a regression that ignores the marker
// (reading a dismissed approval as a standing one) fails here, and so does one
// that drops dismissed rows entirely (reading them as absent, which
// INV-APPROVAL-3 forbids) if it ever changed the self case below.
func TestNeedsAttention_DismissedApprovalDoesNotClose(t *testing.T) {
	revs := []store.Revision{{Seq: 1, HeadSHA: "h1"}}

	standing := []store.Approval{{Approver: attnTeammate, State: "approved", HeadSHA: "h1"}}
	if need, _ := NeedsAttention(revs, standing, attnSelf, false); need {
		t.Fatal("precondition: a standing teammate approval of the current head MUST close the edge")
	}

	dismissed := []store.Approval{{Approver: attnTeammate, State: "approved", HeadSHA: "h1", Dismissed: true}}
	need, reason := NeedsAttention(revs, dismissed, attnSelf, false)
	if !need {
		t.Error("a DISMISSED approval MUST NOT close the attention edge — it is stale, not standing")
	}
	if reason != AttentionReasonUnreviewed {
		t.Errorf("reason = %q, want %q: a dismissed teammate approval must read as if unapproved",
			reason, AttentionReasonUnreviewed)
	}
}
