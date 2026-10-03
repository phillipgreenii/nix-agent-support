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

// Backend implements pr.PendingReviewReader, so the review_pending op is
// registered and listed in capabilities.ops for the GitHub backend.
var _ pr.PendingReviewReader = (*Backend)(nil)

// legacyPGPRMarker is the invisible marker pg-pr stamps on the review bodies
// and comments it posts. pg-pr is still the live review path until its
// retirement, so a pending review it left behind is agent-authored and MUST
// count as marked here; otherwise the guarded supersede would treat every
// such review as human-edited. It is a literal copy: pg-pr's marker package
// is Go-internal and not importable from this module.
const legacyPGPRMarker = "<!-- pg-pr -->"

// hasBotMarker reports whether text carries a bot-authorship marker: this
// backend's plain marker, its digest-bearing body marker, or pg-pr's.
func hasBotMarker(text string) bool {
	return strings.Contains(text, github.BotMarker) ||
		strings.Contains(text, github.DigestMarkerPrefix) ||
		strings.Contains(text, legacyPGPRMarker)
}

// PendingReview implements pr.PendingReviewReader (contract 9.1a): it
// resolves the acting identity's PENDING review on req.ID to a structured
// record, or to the explicit none result.
//
// It is read-only and fail-closed. "No pending review" (Pending false) and
// "lookup failed" (an error) are distinct outcomes: any condition that leaves
// the answer uncertain is an error whose message begins
// "review_pending: detection_failed:", so a caller can report reason
// detection_failed without a new error code (INV-ERR-1 is a closed set).
//
// Staleness compares the REVIEW-level commit against the PR head read from
// GraphQL headRefOid in the same query. A comment's own commit is never read:
// it is not stable once the head advances (prerequisite P4). A review with no
// commit is reported stale. Marker presence is reported for the body and for
// each comment; it does not by itself prove the content is unedited (a
// text-only edit keeps the marker). The record's DigestState does: it is the
// result of checking the body's content digest, stamped at post time, against
// the body and every comment as read (github.VerifyDigest), and only
// "verified" proves the content unedited. A review with no digest (posted
// before digests existed, by pg-pr, or by a human) is "missing", never
// verified.
func (b *Backend) PendingReview(ctx context.Context, req pr.PendingReviewRequest) (pr.PendingReviewResult, error) {
	repo, number, err := parsePRID(req.ID)
	if err != nil {
		return pr.PendingReviewResult{}, scriptout.WrapError(scriptout.ErrInvalidArgument, err.Error())
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
	if data.Review == nil {
		return res, nil
	}
	rev := data.Review
	out := &pr.PendingReview{
		ReviewID:   rev.ID,
		DatabaseID: rev.DatabaseID,
		URL:        rev.URL,
		State:      "pending",
		CommitSHA:  rev.CommitOID,
		Stale:      rev.CommitOID == "" || !strings.EqualFold(rev.CommitOID, data.HeadSHA),
		Body:       rev.Body,
		BodyMarked: hasBotMarker(rev.Body),
		Comments:   make([]pr.PendingReviewComment, 0, len(rev.Comments)),
	}
	out.AllMarked = out.BodyMarked
	commentTexts := make([]string, 0, len(rev.Comments))
	for _, c := range rev.Comments {
		marked := hasBotMarker(c.Body)
		out.AllMarked = out.AllMarked && marked
		commentTexts = append(commentTexts, c.Body)
		out.Comments = append(out.Comments, pr.PendingReviewComment{
			ID: c.ID, Path: c.Path, Line: c.Line, Body: c.Body, Marked: marked,
		})
	}
	out.DigestState = string(github.VerifyDigest(rev.Body, commentTexts))
	res.Pending = true
	res.Review = out
	return res, nil
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
