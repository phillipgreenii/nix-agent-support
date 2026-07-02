// Team-PR review sink (pg2-4c5i.35).
//
// For a TEAMMATE PR the produced review is applied to the GitHub PR as a
// PENDING (draft) review the human submits — never auto-submitted. There is no
// submit mutation anywhere in pg-pr: PostReview posts with NO `event` field, so
// GitHub records the review as PENDING (design §2.6, Q4). The human owns
// submission via the GitHub UI.
//
// This reuses the same staged path as `pg-pr review post`: dedup inline comments
// against the PR's existing comments, marker-stamp the body + each unique
// comment, then PostReview. On a successful post the staged Draft + Result
// sidecar are cleared so a re-run is idempotent.
//
// Q2 (LOCKED): before posting, ApplyPendingReview actively detects an existing
// PENDING review authored by the viewer (via HasPendingReviewByViewer — a
// GraphQL read, because ListReviews cannot see PENDING reviews) and SKIPS when
// one exists, leaving the earlier review untouched so it never clobbers a human's
// in-progress edits.
//
// M4 (documented limitation): PR-level (fileless) findings fold into the review
// BODY, which is NOT deduped by the (path,line,body-prefix) inline-comment dedup.
// Because the skip-if-pending rule covers the common case, a duplicate body only
// arises if the human submitted/deleted the earlier review and the sink re-runs
// against a freshly staged Draft. The body IS marker-stamped so a later sync
// classifies it is_ours, but body text is NOT idempotent across a submit+re-stage
// cycle. This is accepted, not fixed, in this slice.
package reviewsink

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/marker"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/reviewstage"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
)

// VCSReviewer is the narrow provider surface the team-PR sink needs
// (Interface Segregation). Satisfied by *github.Provider. Kept minimal so the
// sink never gains an accidental submit/merge capability.
type VCSReviewer interface {
	// HasPendingReviewByViewer reports whether the authenticated viewer already
	// has a PENDING (unsubmitted) review on the PR (Q2 skip detection).
	HasPendingReviewByViewer(ctx context.Context, repo string, number int) (bool, error)
	// ListComments returns the PR's existing comments (for inline-comment dedup).
	ListComments(ctx context.Context, repo string, number int) ([]api.Comment, error)
	// PostReview posts a PENDING review (no `event`); the human submits it.
	PostReview(ctx context.Context, repo string, number int, body string, comments []api.Comment) (*api.Review, error)
	// GetPR fetches the live PR so the sink can detect a stale head (R6 warn).
	GetPR(ctx context.Context, repo string, number int) (*api.PR, error)
}

// ApplyPendingReview applies a produced TEAMMATE-PR review to GitHub as a
// PENDING review the human submits (the team-PR sink, pg2-4c5i.35). It:
//
//  1. skips (returns nil, no post, no clear) when the viewer already has a
//     PENDING review on the PR (Q2);
//  2. loads the staged Draft — a missing Draft ⇒ "review not yet produced" ⇒
//     no-op (idempotent);
//  3. WARNs (does not block) when the reviewed head SHA differs from the live
//     PR head (R6 staleness);
//  4. dedups inline comments against the PR's existing comments, marker-stamps
//     the body + each unique comment, and PostReviews (PENDING, no event);
//  5. clears the Draft + Result sidecar ONLY on a successful post.
//
// It NEVER submits and performs no other GitHub write.
func ApplyPendingReview(ctx context.Context, rv VCSReviewer, dir string, result reviewstage.Result, log *slog.Logger) error {
	if log == nil {
		log = slog.Default()
	}
	repo, prNumber := result.Repo, result.PR

	// (1) Skip if the viewer already has a PENDING review — do not clobber the
	// human's in-progress edits (Q2). Fail closed on a detection error so we
	// never post over an existing review we simply couldn't see.
	hasPending, err := rv.HasPendingReviewByViewer(ctx, repo, prNumber)
	if err != nil {
		return fmt.Errorf("teamsink: detect pending review %s#%d: %w", repo, prNumber, err)
	}
	if hasPending {
		log.Info("team sink: pending review already exists; leaving for the human",
			"repo", repo, "pr", prNumber, "bead", result.BeadID)
		return nil
	}

	// (2) Load the staged Draft. Missing ⇒ not yet produced ⇒ no-op.
	draft, err := reviewstage.Load(dir, repo, prNumber)
	if err != nil {
		if os.IsNotExist(err) {
			log.Info("team sink: no staged draft; review not produced yet (no-op)",
				"repo", repo, "pr", prNumber, "bead", result.BeadID)
			return nil
		}
		return fmt.Errorf("teamsink: load draft %s#%d: %w", repo, prNumber, err)
	}

	// (3) Staleness check (R6): WARN, do not block.
	if pr, gerr := rv.GetPR(ctx, repo, prNumber); gerr == nil && pr != nil &&
		result.HeadSHA != "" && pr.HeadSHA != "" && result.HeadSHA != pr.HeadSHA {
		log.Warn("team sink: staged review is stale (reviewed head != live head); posting anyway",
			"repo", repo, "pr", prNumber, "reviewed_head", result.HeadSHA, "live_head", pr.HeadSHA)
	}

	// (4) Marker-stamp, dedup inline comments, and post as PENDING (no event).
	// The staged post path (postStaged in cmd/pg-pr/review.go) is the model;
	// this sink stamps BEFORE dedup so a finding whose prior post is already
	// marker-stamped in `existing` dedups against it — making re-runs genuinely
	// idempotent (the acceptance criterion). Existing comments the sink posted
	// carry the marker, so the (path,line,stamped-body-prefix) key matches.
	stamped := make([]api.Comment, len(draft.Comments))
	for i, c := range draft.Comments {
		c.Body = marker.Stamp(c.Body)
		stamped[i] = c
	}
	existing, _ := rv.ListComments(ctx, repo, prNumber)
	unique, skipped := reviewstage.Dedup(stamped, existing)
	body := draft.Body
	if body != "" {
		body = marker.Stamp(body)
	}

	rev, err := rv.PostReview(ctx, repo, prNumber, body, unique)
	if err != nil {
		return fmt.Errorf("teamsink: post pending review %s#%d: %w", repo, prNumber, err)
	}

	// (5) Clear the staged artifacts only on success (idempotent re-runs).
	if cerr := reviewstage.Clear(dir, repo, prNumber); cerr != nil {
		log.Warn("team sink: clear draft failed after post", "repo", repo, "pr", prNumber, "err", cerr.Error())
	}
	if cerr := reviewstage.ClearResult(dir, repo, prNumber); cerr != nil {
		log.Warn("team sink: clear result sidecar failed after post", "repo", repo, "pr", prNumber, "err", cerr.Error())
	}

	log.Info("team sink: posted PENDING review",
		"repo", repo, "pr", prNumber, "bead", result.BeadID,
		"comments_posted", len(unique), "duplicates_skipped", skipped,
		"review_id", rev.ID, "review_state", rev.State)
	return nil
}
