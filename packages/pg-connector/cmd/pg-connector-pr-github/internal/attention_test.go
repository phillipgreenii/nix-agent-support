package internal

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-pr-github/internal/api"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

const (
	attnSelf     = "me"
	attnTeammate = "teammate"
)

// --- needsAttentionForPR: pure predicate, mirroring
// packages/pg-pr/internal/snapshot/attention_test.go's own TestNeedsAttention
// fixture style (bead pg2-7wqkr ports the CONCEPT, not the code). ---

func TestNeedsAttentionForPR(t *testing.T) {
	tests := []struct {
		name        string
		reviews     []api.Review
		self        string
		head        string
		hasConflict bool
		wantNeed    bool
		wantReason  string
	}{
		{
			name:       "nobody reviewed + I have never reviewed -> NEEDS a first review",
			head:       "h1",
			self:       attnSelf,
			wantNeed:   true,
			wantReason: attentionReasonUnreviewed,
		},
		{
			name:     "teammate approved at head -> NO attention (off the hook)",
			head:     "h1",
			self:     attnSelf,
			reviews:  []api.Review{{Author: attnTeammate, State: "APPROVED", CommitOID: "h1"}},
			wantNeed: false,
		},
		{
			name:     "I reviewed the current head -> NO attention",
			head:     "h1",
			self:     attnSelf,
			reviews:  []api.Review{{Author: attnSelf, State: "APPROVED", CommitOID: "h1"}},
			wantNeed: false,
		},
		{
			name:       "new commits after I approved (re-review) -> NEEDS",
			head:       "h2",
			self:       attnSelf,
			reviews:    []api.Review{{Author: attnSelf, State: "APPROVED", CommitOID: "h1"}},
			wantNeed:   true,
			wantReason: attentionReasonReReview,
		},
		{
			name: "I approved an earlier head but teammate approved latest -> NO attention",
			head: "h2",
			self: attnSelf,
			reviews: []api.Review{
				{Author: attnSelf, State: "APPROVED", CommitOID: "h1"},
				{Author: attnTeammate, State: "APPROVED", CommitOID: "h2"},
			},
			wantNeed: false,
		},
		{
			name:     "I reviewed the latest head (re-reviewed after advance) -> NO attention",
			head:     "h2",
			self:     attnSelf,
			reviews:  []api.Review{{Author: attnSelf, State: "APPROVED", CommitOID: "h2"}},
			wantNeed: false,
		},
		{
			name:        "merge conflict dampens everything -> NO attention",
			head:        "h1",
			self:        attnSelf,
			hasConflict: true,
			wantNeed:    false,
		},
		{
			name:     "teammate CHANGES_REQUESTED does not close the team edge, but my own does",
			head:     "h1",
			self:     attnSelf,
			reviews:  []api.Review{{Author: attnSelf, State: "CHANGES_REQUESTED", CommitOID: "h1"}},
			wantNeed: false,
		},
		{
			name:       "dismissed approval (mine) does not stand -> re-review",
			head:       "h1",
			self:       attnSelf,
			reviews:    []api.Review{{Author: attnSelf, State: "DISMISSED", CommitOID: "h1"}},
			wantNeed:   true,
			wantReason: attentionReasonReReview,
		},
		{
			name:     "dismissed teammate approval does not close the team edge -> unreviewed",
			head:     "h1",
			self:     attnSelf,
			reviews:  []api.Review{{Author: attnTeammate, State: "DISMISSED", CommitOID: "h1"}},
			wantNeed: true, wantReason: attentionReasonUnreviewed,
		},
		{
			name: "only the latest review per author counts (older approval superseded)",
			head: "h1",
			self: attnSelf,
			reviews: []api.Review{
				{Author: attnTeammate, State: "APPROVED", CommitOID: "h1"},
				{Author: attnTeammate, State: "CHANGES_REQUESTED", CommitOID: "h1"},
			},
			wantNeed:   true,
			wantReason: attentionReasonUnreviewed,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			need, reason := needsAttentionForPR(tc.reviews, tc.self, tc.head, tc.hasConflict)
			if need != tc.wantNeed {
				t.Fatalf("need = %v, want %v", need, tc.wantNeed)
			}
			if reason != tc.wantReason {
				t.Fatalf("reason = %q, want %q", reason, tc.wantReason)
			}
		})
	}
}

// --- Backend.ListAttention: end-to-end over fakeGH. ---

func TestBackend_ListAttention_NoQueryConfigured_ReturnsEmpty(t *testing.T) {
	b := New(&fakeGH{})
	got, err := b.ListAttention(context.Background())
	if err != nil {
		t.Fatalf("ListAttention: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("len(got) = %d, want 0", len(got))
	}
}

func TestBackend_ListAttention_ScansConfiguredQueryAndFiltersByPredicate(t *testing.T) {
	gh := &fakeGH{
		viewerLogin: attnSelf,
		searchFn: func(ctx context.Context, query string) ([]api.PR, error) {
			return []api.PR{
				{Repo: "owner/repo", Number: 1, Title: "needs review", Author: attnTeammate},
				{Repo: "owner/repo", Number: 2, Title: "already approved by teammate", Author: attnTeammate},
			}, nil
		},
		reviewsWithCommitFn: func(ctx context.Context, repo string, number int) ([]api.Review, error) {
			if number == 2 {
				return []api.Review{{Author: attnTeammate, State: "APPROVED", CommitOID: "h1"}}, nil
			}
			return nil, nil
		},
	}
	// GetPR must answer per-number since fakeGH.pr is a single fixed value;
	// override via a small wrapper.
	gh.pr = &api.PR{Repo: "owner/repo", Number: 1, Title: "needs review", HeadSHA: "h1"}

	b := New(gh)
	ctx := scriptout.WithConfig(context.Background(), json.RawMessage(`{"attention_query":"is:open is:pr -author:@me"}`))
	got, err := b.ListAttention(ctx)
	if err != nil {
		t.Fatalf("ListAttention: %v", err)
	}
	// fakeGH.GetPR always answers gh.pr (repo#1) regardless of which
	// number was requested — both PR 1 and PR 2 are therefore evaluated
	// against the SAME full-PR fixture (HeadSHA "h1", no conflict), but
	// their per-number reviewsWithCommitFn answers differ, which is what
	// this test actually exercises: PR 2's teammate approval at head
	// takes it off the hook while PR 1's empty review list leaves it
	// needing a first review.
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1: %+v", len(got), got)
	}
	if got[0].ID != "owner/repo#1" {
		t.Fatalf("got[0].ID = %q, want owner/repo#1", got[0].ID)
	}
}

func TestBackend_ListAttention_RateLimitBelowReserve(t *testing.T) {
	gh := &fakeGH{rateLimit: 1}
	b := New(gh)
	ctx := scriptout.WithConfig(context.Background(), json.RawMessage(`{"attention_query":"is:open"}`))
	_, err := b.ListAttention(ctx)
	if err == nil {
		t.Fatal("expected an error when rate limit is below the reserve")
	}
	if !errors.Is(err, scriptout.ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
}
