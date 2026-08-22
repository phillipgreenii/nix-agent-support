package snapshot

import (
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/agentregistry"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
)

// buildTeamRow MUST populate NeedsAttention / AttentionReason from STORE facts
// via the shared NeedsAttention predicate (NOT live reviews /
// classifyApprovals). A team PR nobody approved that I have never reviewed needs
// attention — and, per pg2-kh1ar, needs NO bead artifact to say so.
func TestBuildTeamRow_NeedsAttentionFromStore(t *testing.T) {
	reg, _ := agentregistry.New(nil)
	in := BuilderInput{
		GeneratedAt: time.Now(),
		Self:        "alice",
		TeamMembers: []string{"bob"},
		Registry:    reg,
		PRs: []PRInput{
			{
				PR:        api.PR{Repo: "o/r", Number: 2, Author: "bob", URL: "u2"},
				Revisions: []store.Revision{{Seq: 1, HeadSHA: "h1"}}, // nobody approved, I haven't reviewed
			},
		},
	}
	snap := Build(in)
	if len(snap.Team) != 1 {
		t.Fatalf("want 1 team row, got %d", len(snap.Team))
	}
	if !snap.Team[0].NeedsAttention {
		t.Errorf("NeedsAttention should be true")
	}
	if snap.Team[0].AttentionReason != AttentionReasonUnreviewed {
		t.Errorf("AttentionReason = %q, want %q", snap.Team[0].AttentionReason, AttentionReasonUnreviewed)
	}
}

// A team PR carrying a STANDING teammate approval of the current head is off the
// hook — NeedsAttention false. The approval comes from the PER-APPROVER rows
// (pg2-4dz88.1.9), so the row names WHO approved rather than setting a collapsed
// boolean.
func TestBuildTeamRow_TeammateApprovedNoAttention(t *testing.T) {
	reg, _ := agentregistry.New(nil)
	in := BuilderInput{
		GeneratedAt: time.Now(),
		Self:        "alice",
		TeamMembers: []string{"bob"},
		Registry:    reg,
		PRs: []PRInput{
			{
				PR:        api.PR{Repo: "o/r", Number: 2, Author: "bob", URL: "u2", HeadSHA: "h1"},
				Revisions: []store.Revision{{Seq: 1, HeadSHA: "h1"}},
				Approvals: []store.Approval{{Approver: "carol", State: "approved", HeadSHA: "h1"}},
			},
		},
	}
	snap := Build(in)
	if snap.Team[0].NeedsAttention {
		t.Errorf("NeedsAttention should be false when a teammate approved")
	}
	if snap.Team[0].AttentionReason != "" {
		t.Errorf("AttentionReason should be empty, got %q", snap.Team[0].AttentionReason)
	}
}

// The DISMISSED-only case, end-to-end through Build (pg2-4dz88.1.9): the ONLY
// approval on the PR is one the code host dismissed, so it is STALE, and the
// team row MUST still need attention — reading as unapproved, not as freshly
// approved. It also MUST NOT report an approver: a dismissed approval no longer
// stands, so it contributes to neither approver count.
func TestBuildTeamRow_DismissedApprovalStillNeedsAttention(t *testing.T) {
	reg, _ := agentregistry.New(nil)
	in := BuilderInput{
		GeneratedAt: time.Now(),
		Self:        "alice",
		TeamMembers: []string{"bob"},
		Registry:    reg,
		PRs: []PRInput{
			{
				PR:        api.PR{Repo: "o/r", Number: 3, Author: "bob", URL: "u3", HeadSHA: "h1"},
				Revisions: []store.Revision{{Seq: 1, HeadSHA: "h1"}},
				Approvals: []store.Approval{
					{Approver: "carol", State: "approved", HeadSHA: "h1", Dismissed: true},
				},
			},
		},
	}
	snap := Build(in)
	if len(snap.Team) != 1 {
		t.Fatalf("want 1 team row, got %d", len(snap.Team))
	}
	row := snap.Team[0]
	if !row.NeedsAttention {
		t.Error("a dismissed-only approval MUST NOT close the attention edge")
	}
	if row.AttentionReason != AttentionReasonUnreviewed {
		t.Errorf("AttentionReason = %q, want %q", row.AttentionReason, AttentionReasonUnreviewed)
	}
	if row.HumanApprovers != 0 || row.AgentApprovers != 0 || row.HumanApproved {
		t.Errorf("dismissed approval must count as no approver: human=%d agent=%d bool=%v",
			row.HumanApprovers, row.AgentApprovers, row.HumanApproved)
	}
}
