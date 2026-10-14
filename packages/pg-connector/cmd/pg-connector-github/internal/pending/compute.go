package pending

import (
	"fmt"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/github"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/posted"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/pr"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// Compute builds the review_pending op's result from the stored data of a
// pending group, by the old backend's rules (PendingReview in
// cmd/pg-connector-pr-github/internal/review_pending.go), read from stored
// data instead of a live read.
//
// The described review is the pending review with the lowest database id
// (Lowest); comments_total, comments_at_head, the body searched for the live
// head's pg-section and commit_sha all belong to it, and
// extra_pending_reviews counts the others. A comment is on the live head when
// its original commit equals it (case-insensitively); reviewed_head is true
// when the described review's body holds a pg-section for the live head or any
// pending or submitted review of the viewer has the live head as its
// review-level commit; stale is true iff a pending review exists AND
// comments_at_head is 0 AND reviewed_head is false.
//
// The live head is liveHead (the stored summary's headRefOid, which the op
// passes) when it is non-empty and data.HeadSHA otherwise, so a head that moved
// inside pending_ttl is judged against the new head; head_sha in the result is
// that live head. as_of is fetchedAt, the honest as-of of a stored read. last
// is the sidecar's last_append, nil when there is none.
//
// Compute is fail-closed: data that is nil, or has no head and no liveHead was
// given, is an unavailable error whose message begins
// "review_pending: detection_failed:", never a "no pending review" answer.
func Compute(data *github.PendingReviewData, liveHead string, repo string, number int, fetchedAt time.Time, last *pr.LastAppend) (pr.PendingReviewResult, error) {
	head := liveHead
	if head == "" && data != nil {
		head = data.HeadSHA
	}
	if data == nil || head == "" {
		return pr.PendingReviewResult{}, scriptout.WrapError(scriptout.ErrUnavailable,
			fmt.Sprintf("review_pending: detection_failed: could not determine the current head of %s#%d", repo, number))
	}
	res := pr.PendingReviewResult{
		HeadSHA: head,
		AsOf:    fetchedAt.UTC().Format(time.RFC3339),
	}
	rev := data.Lowest()
	if rev == nil {
		return res, nil
	}
	out := &pr.PendingReview{
		ReviewID:            rev.ID,
		DatabaseID:          rev.DatabaseID,
		URL:                 rev.URL,
		State:               "pending",
		CommitSHA:           rev.CommitOID,
		Body:                rev.Body,
		CommentsTotal:       len(rev.Comments),
		ExtraPendingReviews: len(data.Reviews) - 1,
		Comments:            make([]pr.PendingReviewComment, 0, len(rev.Comments)),
	}
	for _, c := range rev.Comments {
		if strings.EqualFold(c.OriginalCommitOID, head) {
			out.CommentsAtHead++
		}
		out.Comments = append(out.Comments, pr.PendingReviewComment{
			ID: c.ID, Path: c.Path, Line: c.Line, Body: c.Body, OriginalCommit: c.OriginalCommitOID,
		})
	}
	out.ReviewedHead = reviewedHead(data, rev, head)
	out.Stale = out.CommentsAtHead == 0 && !out.ReviewedHead
	out.LastAppend = last
	res.Pending = true
	res.Review = out
	return res, nil
}

// reviewedHead reports whether head has been reviewed: the described review's
// body holds a section for it, or any pending or submitted review of the viewer
// has it as its review-level commit.
func reviewedHead(data *github.PendingReviewData, described *github.PendingReviewNode, head string) bool {
	if _, _, ok := posted.FindSection(described.Body, head); ok {
		return true
	}
	for _, r := range data.Reviews {
		if r.CommitOID != "" && strings.EqualFold(r.CommitOID, head) {
			return true
		}
	}
	for _, r := range data.Submitted {
		if r.CommitOID != "" && strings.EqualFold(r.CommitOID, head) {
			return true
		}
	}
	return false
}
