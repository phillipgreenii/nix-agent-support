package snapshot

import (
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/agentregistry"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
)

// buildTeamRow MUST populate NeedsAttention / AttentionReason from STORE revision
// facts + the draft-review-closed signal via the shared needsAttention predicate
// (NOT live reviews / classifyApprovals). A team PR with a closed draft-review
// bead and no approval needs attention.
func TestBuildTeamRow_NeedsAttentionFromStore(t *testing.T) {
	reg, _ := agentregistry.New(nil)
	in := BuilderInput{
		GeneratedAt: time.Now(),
		Self:        "alice",
		TeamMembers: []string{"bob"},
		Registry:    reg,
		PRs: []PRInput{
			{
				PR:                api.PR{Repo: "o/r", Number: 2, Author: "bob", URL: "u2"},
				Revisions:         []store.Revision{{Seq: 1, HeadSHA: "h1"}}, // nobody approved, I haven't reviewed
				DraftReviewClosed: true,
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
	if snap.Team[0].AttentionReason != AttentionReasonDraftReviewReady {
		t.Errorf("AttentionReason = %q, want %q", snap.Team[0].AttentionReason, AttentionReasonDraftReviewReady)
	}
}

// A team PR whose latest revision carries a teammate approval is off the hook —
// NeedsAttention false — regardless of the draft-review-closed signal.
func TestBuildTeamRow_TeammateApprovedNoAttention(t *testing.T) {
	reg, _ := agentregistry.New(nil)
	in := BuilderInput{
		GeneratedAt: time.Now(),
		Self:        "alice",
		TeamMembers: []string{"bob"},
		Registry:    reg,
		PRs: []PRInput{
			{
				PR:                api.PR{Repo: "o/r", Number: 2, Author: "bob", URL: "u2"},
				Revisions:         []store.Revision{{Seq: 1, HeadSHA: "h1", OthersApproved: true}},
				DraftReviewClosed: true,
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
