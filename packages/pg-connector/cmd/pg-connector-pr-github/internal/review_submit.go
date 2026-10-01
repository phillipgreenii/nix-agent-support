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
// body and each comment.
//
// Live-head pre-check (bead pg2-qr4sr): before anything is deleted or posted,
// the PR's current head is read and compared to req.HeadSHA. GitHub itself
// accepts ANY commit that exists in the repo as a review anchor (an older head
// or a force-push-orphaned one posts fine), so without this check a stale
// head_sha would silently anchor the review to the wrong commit. A mismatch is
// invalid_argument (the closed INV-ERR-1 set has no head_moved code) whose
// message says the head moved and names the current head; nothing is posted or
// deleted. A head that moves between the pre-check and the post is not
// detectable here (GitHub accepts it); the window is one API round trip.
//
// When req.SupersedePending is set it then deletes the actor's existing
// pending review; a failed lookup or delete never fails the op by itself and
// is reported in the result's Supersede outcome. If the post that follows
// FAILS, that outcome is folded into the error message (an error carries no
// result body), and "a pending review already exists" is worded distinctly
// from other 422s.
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

	cur, err := b.gh.GetPR(ctx, repo, number)
	if err != nil {
		return pr.ReviewSubmitResult{}, classifyReviewSubmitError(err, nil)
	}
	if cur == nil || cur.HeadSHA == "" {
		return pr.ReviewSubmitResult{}, scriptout.WrapError(scriptout.ErrUnavailable,
			fmt.Sprintf("review_submit: could not determine the current head of %s; nothing was posted", req.ID))
	}
	if !strings.EqualFold(cur.HeadSHA, req.HeadSHA) {
		return pr.ReviewSubmitResult{}, scriptout.WrapError(scriptout.ErrInvalidArgument,
			fmt.Sprintf("review_submit: head moved: head_sha %s is not the current head of %s (current head is %s); "+
				"refresh the PR and retry against the current head; nothing was posted or deleted",
				req.HeadSHA, req.ID, cur.HeadSHA))
	}

	var outcome *pr.SupersedeOutcome
	if req.SupersedePending {
		outcome = b.supersedePending(ctx, repo, number)
	}

	rev, err := b.gh.PostPendingReview(ctx, repo, number, req.HeadSHA, req.Body, comments)
	if err != nil {
		return pr.ReviewSubmitResult{}, classifyReviewSubmitError(err, outcome)
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

// pendingExistsMarker is the phrase GitHub puts in the 422 body when the actor
// already has a pending review on the PR ("User can only have one pending
// review per pull request").
const pendingExistsMarker = "one pending review"

// classifyReviewSubmitError maps a review-post failure onto INV-ERR-1. Every
// HTTP 422 is invalid_argument, but the message tells the causes apart: a
// "pending review already exists" 422 says so and names supersede_pending as
// the remedy; any other 422 (the head was verified against the live head
// before posting, so this is typically a comment that could not be anchored)
// is reported as a rejected review. Auth failures and 404 reuse
// classifyGHError (deliberately not edited: it is hash-pinned); anything else
// is unavailable. outcome, when non-nil, is the supersede attempt that
// preceded the failed post and is appended to the message.
func classifyReviewSubmitError(err error, outcome *pr.SupersedeOutcome) error {
	suffix := supersedeSuffix(outcome)
	low := strings.ToLower(err.Error())
	if strings.Contains(low, "http 422") {
		if strings.Contains(low, pendingExistsMarker) {
			return scriptout.WrapError(scriptout.ErrInvalidArgument,
				"review_submit: a pending review already exists for this PR (GitHub allows one pending review per user per PR); "+
					"retry with supersede_pending=true or delete it first: "+err.Error()+suffix)
		}
		return scriptout.WrapError(scriptout.ErrInvalidArgument,
			"review_submit: GitHub rejected the review (HTTP 422; head_sha matched the live head, so check the comment anchors): "+
				err.Error()+suffix)
	}
	err = classifyGHError(err)
	for _, s := range []error{scriptout.ErrNotFound, scriptout.ErrUnauthenticated, scriptout.ErrInvalidArgument} {
		if errors.Is(err, s) {
			if suffix == "" {
				return err
			}
			return fmt.Errorf("%w%s", err, suffix)
		}
	}
	return scriptout.WrapError(scriptout.ErrUnavailable, err.Error()+suffix)
}

// supersedeSuffix renders a supersede outcome for an error message, or "" when
// no supersede was requested.
func supersedeSuffix(o *pr.SupersedeOutcome) string {
	switch {
	case o == nil:
		return ""
	case o.Deleted:
		return "; supersede: the previous pending review was deleted before the post failed"
	case o.Error != "":
		return "; supersede: attempted but failed: " + o.Error + " (the previous pending review is untouched)"
	case o.Attempted:
		return "; supersede: attempted, not deleted"
	default:
		return "; supersede: no pending review was found to delete"
	}
}
