package internal

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-pr-github/internal/github"
	pgposted "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-pr-github/internal/posted"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/pr"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// Backend implements pr.PendingReviewReader, so the review_pending op is
// registered and listed in capabilities.ops for the GitHub backend.
var _ pr.PendingReviewReader = (*Backend)(nil)

// PendingReview implements pr.PendingReviewReader (contract 9.1a): it
// resolves the acting identity's PENDING review on req.ID to a structured
// record, or to the explicit none result.
//
// It is read-only and fail-closed. "No pending review" (Pending false) and
// "lookup failed" (an error) are distinct outcomes: any condition that leaves
// the answer uncertain is an error whose message begins
// "review_pending: detection_failed:", so a caller can report reason
// detection_failed without a new error code (INV-ERR-1 is a closed set). More
// than one pending review is NOT a failure.
//
// Which review is described. With several pending reviews the record
// describes the one with the lowest database id: comments_total,
// comments_at_head, the body searched for the live head's pg-section, and
// commit_sha all belong to it, and extra_pending_reviews counts the others.
// (Every pending review's review-level commit still counts towards
// reviewed_head, because "any review of the viewer at the head" is a property
// of the viewer, not of one review.)
//
// Head anchoring. A comment is on the live head when its originalCommit
// equals it (never commit.oid, REST commit_id or the review-level commit: they
// move or reflect where the review was created). reviewed_head is true when
// the described review's body holds a pg-section for the live head
// (pgposted.FindSection), or when any pending or submitted review of the
// viewer has the live head as its review-level commit. stale is true iff a
// pending review exists AND comments_at_head is 0 AND reviewed_head is false;
// the connector, not the dashboard, owns that verdict.
//
// last_append comes from the posted-sidecar. A sidecar that is missing,
// unconfigured or unreadable simply yields no last_append: it is local
// bookkeeping and has no bearing on whether a review is pending.
func (b *Backend) PendingReview(ctx context.Context, req pr.PendingReviewRequest) (pr.PendingReviewResult, error) {
	repo, number, err := parsePRID(req.ID)
	if err != nil {
		return pr.PendingReviewResult{}, scriptout.WrapError(scriptout.ErrInvalidArgument, err.Error())
	}
	if err := b.checkRateReserve(ctx); err != nil {
		return pr.PendingReviewResult{}, err
	}
	data, err := b.gh.GetPendingReview(ctx, repo, number)
	if err != nil {
		return pr.PendingReviewResult{}, classifyPendingReviewError(err)
	}
	if data == nil || data.HeadSHA == "" {
		return pr.PendingReviewResult{}, scriptout.WrapError(scriptout.ErrUnavailable,
			fmt.Sprintf("review_pending: detection_failed: could not determine the current head of %s", req.ID))
	}
	res := pr.PendingReviewResult{
		HeadSHA: data.HeadSHA,
		AsOf:    time.Now().UTC().Format(time.RFC3339),
	}
	rev := data.Lowest()
	if rev == nil {
		return res, nil
	}
	head := data.HeadSHA
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
	out.ReviewedHead = reviewedHead(data, rev)
	out.Stale = out.CommentsAtHead == 0 && !out.ReviewedHead
	out.LastAppend = b.lastAppend(repo, number)
	res.Pending = true
	res.Review = out
	return res, nil
}

// reviewedHead reports whether the live head has been reviewed: the described
// review's body holds a section for it, or any pending or submitted review of
// the viewer has it as its review-level commit.
func reviewedHead(data *github.PendingReviewData, described *github.PendingReviewNode) bool {
	head := data.HeadSHA
	if _, _, ok := pgposted.FindSection(described.Body, head); ok {
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

// lastAppend reads the sidecar's last_append for repo#number, or nil when
// there is none or the sidecar cannot be read.
func (b *Backend) lastAppend(repo string, number int) *pr.LastAppend {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || b.posted.Dir == "" {
		return nil
	}
	st, err := b.posted.Load(owner, name, number)
	if err != nil || st.LastAppend == nil {
		return nil
	}
	return &pr.LastAppend{At: st.LastAppend.At, Added: st.LastAppend.Added, Head: st.LastAppend.Head}
}

// classifyPendingReviewError maps a failed lookup onto INV-ERR-1 (auth ->
// unauthenticated, a genuinely missing PR -> not_found, anything else ->
// unavailable) and prefixes the message with "detection_failed:" so a caller
// can report it as reason detection_failed. No new error code is introduced.
func classifyPendingReviewError(err error) error {
	msg := "review_pending: detection_failed: " + err.Error()
	classified := classifyGHError(err)
	switch {
	case errors.Is(classified, scriptout.ErrNotFound):
		return scriptout.WrapError(scriptout.ErrNotFound, msg)
	case errors.Is(classified, scriptout.ErrUnauthenticated):
		return scriptout.WrapError(scriptout.ErrUnauthenticated, msg)
	default:
		return scriptout.WrapError(scriptout.ErrUnavailable, msg)
	}
}
