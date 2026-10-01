package internal

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-pr-github/internal/github"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/pr"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// Backend implements pr.ReviewSubmitter, so the review_submit op is registered
// and listed in capabilities.ops for the GitHub backend.
var _ pr.ReviewSubmitter = (*Backend)(nil)

// SubmitReview implements pr.ReviewSubmitter (contract 9.1): it posts a
// PENDING review anchored to req.HeadSHA with the bot marker stamped on the
// body and each comment. When req.SupersedePending is set it first deletes the
// actor's existing pending review; a failed lookup or delete is reported in
// the result's Supersede outcome and never fails the op.
//
// Per-comment side is carried through: "" and "RIGHT" post on the right side,
// "LEFT" on the left, anything else is invalid_argument (never silently
// downgraded to RIGHT).
func (b *Backend) SubmitReview(ctx context.Context, req pr.ReviewSubmitRequest) (pr.ReviewSubmitResult, error) {
	repo, number, err := parsePRID(req.ID)
	if err != nil {
		return pr.ReviewSubmitResult{}, scriptout.WrapError(scriptout.ErrInvalidArgument, err.Error())
	}
	if req.HeadSHA == "" {
		return pr.ReviewSubmitResult{}, scriptout.WrapError(scriptout.ErrInvalidArgument, "review_submit: head_sha is required")
	}
	comments := make([]github.ReviewSubmitComment, 0, len(req.Comments))
	for i, c := range req.Comments {
		if c.Path == "" || c.Line <= 0 {
			return pr.ReviewSubmitResult{}, scriptout.WrapError(scriptout.ErrInvalidArgument,
				fmt.Sprintf("review_submit: comment %d needs a path and a positive line", i))
		}
		side := strings.ToUpper(c.Side)
		if side != "" && side != "LEFT" && side != "RIGHT" {
			return pr.ReviewSubmitResult{}, scriptout.WrapError(scriptout.ErrInvalidArgument,
				fmt.Sprintf("review_submit: comment %d has unsupported side %q (want LEFT or RIGHT)", i, c.Side))
		}
		comments = append(comments, github.ReviewSubmitComment{Path: c.Path, Line: c.Line, Side: side, Body: c.Body})
	}

	var outcome *pr.SupersedeOutcome
	if req.SupersedePending {
		outcome = b.supersedePending(ctx, repo, number)
	}

	rev, err := b.gh.PostPendingReview(ctx, repo, number, req.HeadSHA, req.Body, comments)
	if err != nil {
		return pr.ReviewSubmitResult{}, classifyReviewSubmitError(err)
	}
	return pr.ReviewSubmitResult{
		ReviewID:  rev.ID,
		State:     "pending",
		HeadSHA:   req.HeadSHA,
		AsOf:      time.Now().UTC().Format(time.RFC3339),
		Supersede: outcome,
	}, nil
}

// supersedePending finds and deletes the actor's pending review. Attempted is
// false when there was nothing to delete; a lookup or delete failure is
// reported through Error, never returned.
func (b *Backend) supersedePending(ctx context.Context, repo string, number int) *pr.SupersedeOutcome {
	id, found, err := b.gh.FindPendingReview(ctx, repo, number)
	if err != nil {
		return &pr.SupersedeOutcome{Attempted: true, Error: err.Error()}
	}
	if !found {
		return &pr.SupersedeOutcome{}
	}
	if err := b.gh.DeleteReview(ctx, repo, number, id); err != nil {
		return &pr.SupersedeOutcome{Attempted: true, Error: err.Error()}
	}
	return &pr.SupersedeOutcome{Attempted: true, Deleted: true}
}

// classifyReviewSubmitError maps a review-post failure onto INV-ERR-1. Every
// HTTP 422 is invalid_argument (the head moved since head_sha, or a comment
// could not be anchored); auth failures and 404 reuse classifyGHError
// (deliberately not edited: it is hash-pinned); anything else is unavailable.
func classifyReviewSubmitError(err error) error {
	if strings.Contains(strings.ToLower(err.Error()), "http 422") {
		return scriptout.WrapError(scriptout.ErrInvalidArgument, err.Error())
	}
	err = classifyGHError(err)
	for _, s := range []error{scriptout.ErrNotFound, scriptout.ErrUnauthenticated, scriptout.ErrInvalidArgument} {
		if errors.Is(err, s) {
			return err
		}
	}
	return scriptout.WrapError(scriptout.ErrUnavailable, err.Error())
}
