package internal

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-pr-github/internal/archive"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-pr-github/internal/github"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/pr"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// Backend implements pr.ReviewSubmitter, so the review_submit op is registered
// and listed in capabilities.ops for the GitHub backend.
var _ pr.ReviewSubmitter = (*Backend)(nil)

// SubmitReview implements pr.ReviewSubmitter (contract 9.1): it posts a
// PENDING review anchored to req.HeadSHA with the bot marker stamped on each
// comment and the content-digest marker on the body.
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
// Without req.SupersedePending the review is simply posted (status posted),
// and an existing pending review makes GitHub answer 422, worded as "a pending
// review already exists".
//
// With req.SupersedePending the guarded supersede runs (see supersede), and
// the result carries exactly one status: posted, skipped, replaced or
// blocked_human_pending.
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

	if req.SupersedePending {
		return b.supersede(ctx, req, repo, number, comments)
	}
	rev, err := b.gh.PostPendingReview(ctx, repo, number, req.HeadSHA, req.Body, comments)
	if err != nil {
		return pr.ReviewSubmitResult{}, classifyReviewSubmitError(err, nil)
	}
	return posted(req, rev.ID, pr.StatusPosted, nil), nil
}

// posted builds the result for a review that was posted.
func posted(req pr.ReviewSubmitRequest, reviewID, status string, outcome *pr.SupersedeOutcome) pr.ReviewSubmitResult {
	return pr.ReviewSubmitResult{
		ReviewID:  reviewID,
		State:     "pending",
		HeadSHA:   req.HeadSHA,
		AsOf:      time.Now().UTC().Format(time.RFC3339),
		Status:    status,
		Supersede: outcome,
	}
}

// blocked builds the result for a supersede that left everything untouched and
// needs a human: nothing was posted (so review_id is empty) and the review it
// names, when there is one, is unmodified.
func blocked(req pr.ReviewSubmitRequest, reason, message string, ref *pr.PendingReviewRef, outcome *pr.SupersedeOutcome) pr.ReviewSubmitResult {
	return pr.ReviewSubmitResult{
		HeadSHA:       req.HeadSHA,
		AsOf:          time.Now().UTC().Format(time.RFC3339),
		Status:        pr.StatusBlockedHumanPending,
		Reason:        reason,
		Message:       message,
		PendingReview: ref,
		Supersede:     outcome,
	}
}

// supersede is the guarded supersede_pending path (entity-change-flow contract
// 9.1; pending-review handling policies 2 to 4 and 7). It never deletes
// unless every guard holds, and it never submits anything.
//
//  1. Look up the actor's pending review (review_pending). A failed lookup is
//     fail-closed: blocked_human_pending, reason detection_failed, nothing
//     posted or deleted. No pending review: post (posted).
//  2. A pending review at the head being reviewed: do not post (skipped,
//     reason pending_review_exists_same_head). The comparison is the REVIEW's
//     commit against req.HeadSHA, never a comment's commit (not stable, P4).
//  3. A stale pending review (different or null commit) is replaced only when
//     the marker is on the body AND every comment AND the body's content digest
//     verifies against the content as read (so a marker-preserving text edit,
//     a removed marker, an added comment and a review with no digest are all
//     refused: blocked, reason human_edited). The edit signal is the digest,
//     never lastEditedAt (null after a web-UI edit, G2).
//  4. The content is archived BEFORE the delete; a failed archive write blocks
//     (archive_failed) and nothing is deleted.
//  5. The delete is attempted once. Any failure blocks (delete_refused). An
//     HTTP 422 non-pending (REST) or UNPROCESSABLE (GraphQL) answer, the shape
//     of a lost submit-vs-delete race, re-lists once so the message can say
//     what the review is now; the delete is never retried.
//  6. Then the new review is posted (replaced). If that post fails after the
//     delete, the error says the old review was deleted and where it is
//     archived.
func (b *Backend) supersede(ctx context.Context, req pr.ReviewSubmitRequest, repo string, number int, comments []github.ReviewSubmitComment) (pr.ReviewSubmitResult, error) {
	look, err := b.PendingReview(ctx, pr.PendingReviewRequest{ID: req.ID})
	if err != nil {
		msg := "the actor's pending review could not be determined (" + err.Error() + "); nothing was posted, deleted or submitted"
		return blocked(req, pr.ReasonDetectionFailed, msg, nil, &pr.SupersedeOutcome{Attempted: true, Error: err.Error()}), nil
	}
	if !look.Pending || look.Review == nil {
		rev, err := b.gh.PostPendingReview(ctx, repo, number, req.HeadSHA, req.Body, comments)
		if err != nil {
			return pr.ReviewSubmitResult{}, classifyReviewSubmitError(err, &pr.SupersedeOutcome{})
		}
		return posted(req, rev.ID, pr.StatusPosted, &pr.SupersedeOutcome{}), nil
	}

	old := look.Review
	ref := &pr.PendingReviewRef{ReviewID: old.ReviewID, DatabaseID: old.DatabaseID, URL: old.URL, CommitSHA: old.CommitSHA}

	if strings.EqualFold(old.CommitSHA, req.HeadSHA) {
		res := posted(req, old.ReviewID, pr.StatusSkipped, &pr.SupersedeOutcome{})
		res.Reason = pr.ReasonSameHead
		res.Message = "a pending review already exists at this head; nothing was posted"
		res.PendingReview = ref
		return res, nil
	}

	if detail := editedDetail(old); detail != "" {
		msg := "the stale pending review " + reviewLabel(ref) + " is not provably unedited agent-authored content (" + detail +
			"); it was left untouched and nothing was posted"
		return blocked(req, pr.ReasonHumanEdited, msg, ref, &pr.SupersedeOutcome{}), nil
	}
	if old.DatabaseID <= 0 {
		return blocked(req, pr.ReasonDetectionFailed,
			"the pending review "+reviewLabel(ref)+" carried no database id, so it cannot be archived or deleted; it was left untouched and nothing was posted",
			ref, &pr.SupersedeOutcome{}), nil
	}

	archivePath, err := b.archiveReview(repo, number, look)
	if err != nil {
		msg := "the stale pending review " + reviewLabel(ref) + " could not be archived (" + err.Error() +
			"); it was NOT deleted and nothing was posted"
		return blocked(req, pr.ReasonArchiveFailed, msg, ref, &pr.SupersedeOutcome{}), nil
	}

	if err := b.gh.DeleteReview(ctx, repo, number, old.DatabaseID); err != nil {
		msg := "the host refused to delete the stale pending review " + reviewLabel(ref) + " (" + err.Error() + ")"
		if isLostDeleteRace(err) {
			msg += "; " + b.relistDetail(ctx, req.ID, old.ReviewID)
		}
		msg += "; it was left as is and nothing was posted"
		return blocked(req, pr.ReasonDeleteRefused, msg, ref, &pr.SupersedeOutcome{Attempted: true, Error: err.Error()}), nil
	}

	outcome := &pr.SupersedeOutcome{Attempted: true, Deleted: true}
	rev, err := b.gh.PostPendingReview(ctx, repo, number, req.HeadSHA, req.Body, comments)
	if err != nil {
		cerr := classifyReviewSubmitError(err, outcome)
		return pr.ReviewSubmitResult{}, fmt.Errorf("%w; the deleted review's content is archived at %s", cerr, archivePath)
	}
	res := posted(req, rev.ID, pr.StatusReplaced, outcome)
	res.Superseded = &pr.SupersededReview{
		PendingReviewRef: *ref,
		ArchivePath:      archivePath,
		Body:             old.Body,
		Comments:         old.Comments,
	}
	return res, nil
}

// editedDetail returns "" when the pending review is provably unedited,
// fully-marked agent content, else a short description of why it is not.
func editedDetail(old *pr.PendingReview) string {
	if !old.AllMarked {
		return "the bot marker is missing from the body or a comment"
	}
	switch github.DigestState(old.DigestState) {
	case github.DigestVerified:
		return ""
	case github.DigestMissing:
		return "it carries no content digest"
	case github.DigestUnreadable:
		return "its content digest is unreadable"
	case github.DigestMismatch:
		return "its content no longer matches the digest recorded when it was posted"
	default:
		return "its content digest could not be verified"
	}
}

// reviewLabel names a review for a message: its URL when known, else its id.
func reviewLabel(ref *pr.PendingReviewRef) string {
	if ref.URL != "" {
		return ref.URL
	}
	return ref.ReviewID
}

// archiveReview persists the full pending review before it is deleted, and
// returns where. A nil archiver (no location configured) is a failure: the
// delete MUST NOT happen without an archive.
func (b *Backend) archiveReview(repo string, number int, look pr.PendingReviewResult) (string, error) {
	if b.archiver == nil {
		return "", errors.New("no archive location is configured")
	}
	old := look.Review
	rec := archive.Record{
		Repo: repo, PR: number,
		ReviewID: old.ReviewID, DatabaseID: old.DatabaseID, URL: old.URL,
		CommitSHA: old.CommitSHA, HeadSHA: look.HeadSHA, DigestState: old.DigestState,
		Body: old.Body, Comments: make([]archive.Comment, 0, len(old.Comments)),
	}
	for _, c := range old.Comments {
		rec.Comments = append(rec.Comments, archive.Comment{ID: c.ID, Path: c.Path, Line: c.Line, Body: c.Body, Marked: c.Marked})
	}
	return b.archiver.Write(rec)
}

// isLostDeleteRace reports whether a delete failure has the shape of GitHub
// refusing to delete a review that is no longer pending: REST answers HTTP 422
// ("Can not delete a non-pending pull request review"), GraphQL answers
// UNPROCESSABLE (prerequisite P2).
func isLostDeleteRace(err error) bool {
	low := strings.ToLower(err.Error())
	return strings.Contains(low, "http 422") || strings.Contains(low, "unprocessable") || strings.Contains(low, "non-pending")
}

// relistDetail re-lists the actor's pending review ONCE after a refused delete
// and says what it found. It is informational: the caller reports
// delete_refused whatever this finds, and never retries the delete.
func (b *Backend) relistDetail(ctx context.Context, id, reviewID string) string {
	again, err := b.PendingReview(ctx, pr.PendingReviewRequest{ID: id})
	switch {
	case err != nil:
		return "re-listing it afterwards failed (" + err.Error() + ")"
	case !again.Pending || again.Review == nil:
		return "re-listing shows it is no longer pending (it was submitted or deleted concurrently)"
	case again.Review.ReviewID != reviewID:
		return "re-listing shows a different pending review now exists"
	default:
		return "re-listing shows it is still pending"
	}
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
