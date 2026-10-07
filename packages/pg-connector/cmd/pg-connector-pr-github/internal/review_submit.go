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

// Backend implements pr.ReviewSubmitter, so the review_submit op is registered
// and listed in capabilities.ops for the GitHub backend.
var _ pr.ReviewSubmitter = (*Backend)(nil)

const (
	// maxRequestComments caps the comments of one request.
	//
	// Ruling (bead pg2-m79ch, 2026-10-07; options: lower the cap, extend the
	// verb's exec timeout, or document retry-on-kill): the cap is lowered so
	// that a request at the cap always completes inside scriptout's
	// DefaultBackendTimeout (pkg/scriptout/limits.go, 30s), which bounds the whole
	// run: the lookup, the sequential write documents (10 mutations each, see
	// github.maxWriteAliasesPerDocument) and the re-read. The previous cap of
	// 200 (20 documents) was not reachable in 30s: live, a 105-comment request
	// (11 documents) was killed ("signal: killed") after 70 comments had
	// landed, i.e. 7 documents plus the first read fit in under 30s, so one
	// document costs at most about 4s. 40 comments is 4 documents, about 16s
	// of writes; with a generous 8s for the two reads the worst case is about 24s,
	// under the 30s timeout. Raising the timeout was rejected
	// because it would also lengthen the bound on a genuinely hung gh for
	// every other op; retry-on-kill was rejected because the first call
	// would still fail mid-way. A caller with more comments splits them
	// across requests: the replay is idempotent (already_present), so
	// sequential requests converge on the same review.
	maxRequestComments = 40
	// maxOlderHeadComments caps the comments of a request that saves at an
	// EARLIER head of the PR (INV-REVHEAD-3). That path reads two more things
	// inside the same 30s run, the PR's commit list and the compared patches
	// (one or more REST pages), so the cap is lowered from the 40 above to 20:
	// two write documents (about 8s at the measured 4s each), the two reads
	// the live-head path has (about 8s, generous) and the two extra reads
	// (about 8s, generous) come to about 24s, under the 30s timeout with the
	// same 20% margin the live-head arithmetic keeps. A caller with more
	// comments splits them across requests; the replay is idempotent.
	maxOlderHeadComments = 20
	// maxFailuresListed caps the failures an error message lists.
	maxFailuresListed = 20
	// maxReviewBody is GitHub's limit on a review body.
	maxReviewBody = 65536
	// maxSubmitAttempts bounds how often a run starts over after the host
	// changed under it (a pending review appeared or went away between its
	// read and its write).
	maxSubmitAttempts = 3
)

// SubmitReview implements pr.ReviewSubmitter: it puts the request's content
// into the acting identity's PENDING review. It uses the existing pending
// review when there is one, creates one when there is not, and never deletes,
// replaces or submits anything: new content is merged in and added, so the
// operator's edits survive.
//
// One run, under a per-PR lock:
//
//  1. Read the live head, every pending review of the identity (all comments
//     paginated) and the identity's comments in submitted reviews, in one
//     lookup. A head_sha that is not the live head is accepted only when it is
//     one of the PR's commits (saved-at an earlier head, INV-REVHEAD-1..3,
//     see saveAtEarlierHead); otherwise it is invalid_argument and nothing is
//     written.
//  2. Classify every requested comment by the hidden fingerprint marker it
//     would carry: already on the host (already_present), written before and
//     since deleted by the operator (dismissed, from the posted-sidecar), or
//     to be written. Identical items in one request are one item.
//  3. When nothing is to be written and no body section is needed the status
//     is no_change and no review is created. Otherwise, with no pending review,
//     a body-only review is created at the saved-at head, which is the live
//     head unless step 1 accepted an earlier one (a 422 "one pending review"
//     starts the run over, which then appends); with one or more, the lowest
//     database id is used.
//  4. The review body is a series of delimited per-head sections. A section for
//     the saved-at head (followed, for an earlier head, by a line saying where
//     it was saved and what the PR head was) is added only when missing and
//     never rewrites existing text; with more than one pending review the body is not touched, and a
//     review whose body is empty (GitHub refuses to edit it) is left alone
//     (body: skipped_empty_review). A review this tool creates therefore never
//     has an empty body: without a section it carries the attribution line.
//  5. Comments go out in failure-isolated batches: by line and side at the
//     live head, or, for an earlier head, by diff position at that commit (a
//     point whose line is not in that diff fails alone as anchor_rejected;
//     resolveAnchors). The review is then re-read
//     and what landed is decided by the markers found there, not by the write
//     answers. The sidecar records only confirmed fingerprints.
//
// A run in which some comments did not land is an error whose message has the
// stable shape "<n> of <m> comments landed; failed: <reason>:<fingerprint>
// [:<path>:<line>] ..." (see failedMessage); replaying the identical request
// skips what landed and retries what did not.
func (b *Backend) SubmitReview(ctx context.Context, req pr.ReviewSubmitRequest) (pr.ReviewSubmitResult, error) {
	repo, number, err := parsePRID(req.ID)
	if err != nil {
		return pr.ReviewSubmitResult{}, scriptout.WrapError(scriptout.ErrInvalidArgument, err.Error())
	}
	if strings.TrimSpace(req.HeadSHA) == "" {
		return pr.ReviewSubmitResult{}, scriptout.WrapError(scriptout.ErrInvalidArgument, "review_submit: head_sha is required")
	}
	if !github.IsFullSHA(req.HeadSHA) {
		return pr.ReviewSubmitResult{}, scriptout.WrapError(scriptout.ErrInvalidArgument,
			fmt.Sprintf("review_submit: head_sha %q is not a full 40-character commit sha (an abbreviated sha is not accepted); nothing was written", req.HeadSHA))
	}
	items, err := buildSubmitItems(req.Comments)
	if err != nil {
		return pr.ReviewSubmitResult{}, err
	}
	bodyText := pgposted.NormalizeBody(req.Body)
	if bodyText != "" {
		if strings.Contains(bodyText, pgposted.SectionClose) || strings.Contains(bodyText, "<!-- pg-section") {
			return pr.ReviewSubmitResult{}, scriptout.WrapError(scriptout.ErrInvalidArgument,
				"review_submit: body must not contain a review body section delimiter")
		}
	}
	owner, name, _ := strings.Cut(repo, "/")

	lock, err := b.locker.Acquire(owner, name, number)
	if err != nil {
		return pr.ReviewSubmitResult{}, scriptout.WrapError(scriptout.ErrUnavailable,
			fmt.Sprintf("review_submit: could not take the per-PR lock for %s: %v", req.ID, err))
	}
	defer func() { _ = lock.Release() }()

	run := &submitRun{b: b, req: req, repo: repo, owner: owner, name: name, number: number, items: items, bodyText: bodyText}
	for range maxSubmitAttempts {
		res, again, err := run.once(ctx)
		if !again {
			return res, err
		}
	}
	return pr.ReviewSubmitResult{}, scriptout.WrapError(scriptout.ErrUnavailable,
		fmt.Sprintf("review_submit: the pending reviews of %s kept changing during the run; retry", req.ID))
}

// submitItem is one distinct comment of a request, normalized.
type submitItem struct {
	fp       string
	reply    bool
	threadID string
	path     string
	line     int
	side     string // "LEFT" or "RIGHT"; points only
	body     string // normalized request text, without marker or attribution
}

// buildSubmitItems validates a request's comments and merges items that are
// identical after normalization into one.
func buildSubmitItems(comments []pr.ReviewComment) ([]submitItem, error) {
	if len(comments) > maxRequestComments {
		return nil, scriptout.WrapError(scriptout.ErrInvalidArgument,
			fmt.Sprintf("review_submit: %d comments exceed the limit of %d per request", len(comments), maxRequestComments))
	}
	seen := map[string]bool{}
	out := make([]submitItem, 0, len(comments))
	for i, c := range comments {
		var it submitItem
		if thread := strings.TrimSpace(c.ThreadID); thread != "" {
			if c.Path != "" || c.Line != 0 {
				return nil, scriptout.WrapError(scriptout.ErrInvalidArgument,
					fmt.Sprintf("review_submit: comment %d has a thread_id together with path or line; a reply carries only thread_id and body", i))
			}
			it = submitItem{
				reply: true, threadID: thread, body: pgposted.NormalizeBody(c.Body),
				fp: pgposted.ReplyFingerprint(thread, c.Body),
			}
		} else {
			if c.Path == "" || c.Line <= 0 {
				return nil, scriptout.WrapError(scriptout.ErrInvalidArgument,
					fmt.Sprintf("review_submit: comment %d needs a path and a positive line (or a thread_id to reply)", i))
			}
			side := strings.ToUpper(strings.TrimSpace(c.Side))
			if side == "" {
				side = "RIGHT"
			}
			if side != "LEFT" && side != "RIGHT" {
				return nil, scriptout.WrapError(scriptout.ErrInvalidArgument,
					fmt.Sprintf("review_submit: comment %d has unsupported side %q (want LEFT or RIGHT)", i, c.Side))
			}
			it = submitItem{
				path: c.Path, line: c.Line, side: side, body: pgposted.NormalizeBody(c.Body),
				fp: pgposted.PointFingerprint(c.Path, side, c.Line, c.Body),
			}
		}
		if seen[it.fp] {
			continue
		}
		seen[it.fp] = true
		out = append(out, it)
	}
	return out, nil
}

// submitRun is one SubmitReview call, holding the per-PR lock.
type submitRun struct {
	b        *Backend
	req      pr.ReviewSubmitRequest
	repo     string
	owner    string
	name     string
	number   int
	items    []submitItem
	bodyText string
	// sectionText is what a body section for the saved-at head holds: bodyText,
	// plus the saved-at line when the review is saved at an earlier head. Set by
	// once() on every attempt.
	sectionText string
}

// hostFingerprints returns every comment fingerprint marker found on comments
// the acting identity authored on the PR, in its pending reviews and its
// submitted reviews.
func hostFingerprints(data *github.PendingReviewData) map[string]bool {
	out := map[string]bool{}
	add := func(cs []github.PendingReviewComment) {
		for _, c := range cs {
			for _, fp := range pgposted.ExtractFingerprints(c.Body) {
				out[fp] = true
			}
		}
	}
	for _, r := range data.Reviews {
		add(r.Comments)
	}
	for _, r := range data.Submitted {
		add(r.Comments)
	}
	return out
}

// reviewByID finds a pending review of data by its node id.
func reviewByID(data *github.PendingReviewData, id string) *github.PendingReviewNode {
	for i := range data.Reviews {
		if data.Reviews[i].ID == id {
			return &data.Reviews[i]
		}
	}
	return nil
}

// buildSection renders the body section for head.
func buildSection(head, text string) string {
	return pgposted.SectionOpen(head) + "\n" + text + "\n" + pgposted.SectionClose
}

// appendSection appends section to a review body, leaving the existing text
// untouched.
func appendSection(existing, section string) string {
	if strings.TrimSpace(existing) == "" {
		return section
	}
	return existing + "\n\n" + section
}

// bodyHeadRecorded reports whether the sidecar records a body section as
// written for head. Heads match on their section key, as in the body.
func bodyHeadRecorded(st pgposted.State, head string) bool {
	open := pgposted.SectionOpen(head)
	for _, h := range st.BodyHeads {
		if pgposted.SectionOpen(h) == open {
			return true
		}
	}
	return false
}

// bodyPlan is what the run will do about the review body.
type bodyPlan struct {
	// disposition is the pr.Body* value reported unless the write changes it.
	disposition string
	// write is true when a section for the head is to be added.
	write bool
}

// planBody decides the body disposition from the review the run would use
// (nil when there is none).
func (r *submitRun) planBody(target *github.PendingReviewNode, extras int, st pgposted.State, head string) bodyPlan {
	switch {
	case r.bodyText == "":
		return bodyPlan{disposition: pr.BodyAbsent}
	case extras > 0:
		return bodyPlan{disposition: pr.BodySkippedExtraPending}
	}
	existing := ""
	if target != nil {
		existing = target.Body
		if _, _, ok := pgposted.FindSection(existing, head); ok {
			return bodyPlan{disposition: pr.BodyKept}
		}
	}
	if bodyHeadRecorded(st, head) {
		return bodyPlan{disposition: pr.BodyDismissed}
	}
	if len(appendSection(existing, buildSection(head, r.sectionText))) > maxReviewBody {
		return bodyPlan{disposition: pr.BodyTooLarge}
	}
	return bodyPlan{disposition: pr.BodyWritten, write: true}
}

// once runs the read-classify-write-reconcile sequence one time. again is true
// when the host changed under the run before anything was written, so the run
// starts over from its read.
func (r *submitRun) once(ctx context.Context) (res pr.ReviewSubmitResult, again bool, err error) {
	data, err := r.b.gh.GetPendingReview(ctx, r.repo, r.number)
	if err != nil {
		return res, false, classifyReviewSubmitError(err)
	}
	if data == nil || data.HeadSHA == "" {
		return res, false, scriptout.WrapError(scriptout.ErrUnavailable,
			fmt.Sprintf("review_submit: could not determine the current head of %s; nothing was written", r.req.ID))
	}
	live := data.HeadSHA
	head := live
	moved := !strings.EqualFold(live, r.req.HeadSHA)
	var baseOID string
	if moved {
		head, baseOID, err = r.saveAtEarlierHead(ctx, live)
		if err != nil {
			return res, false, err
		}
	}
	r.sectionText = r.bodyText
	if moved && r.bodyText != "" {
		r.sectionText = r.bodyText + "\n\n" + savedAtLine(head, live)
	}
	st, err := r.b.posted.Load(r.owner, r.name, r.number)
	if err != nil {
		return res, false, scriptout.WrapError(scriptout.ErrUnavailable,
			fmt.Sprintf("review_submit: the posted-sidecar for %s could not be read (%v); nothing was written", r.req.ID, err))
	}

	fps := make([]string, len(r.items))
	for i, it := range r.items {
		fps[i] = it.fp
	}
	verdicts := pgposted.Classify(fps, hostFingerprints(data), st)
	var toWrite []submitItem
	var alreadyPresent, dismissed int
	for i, v := range verdicts {
		switch v {
		case pgposted.AlreadyPresent:
			alreadyPresent++
		case pgposted.Dismissed:
			dismissed++
		default:
			toWrite = append(toWrite, r.items[i])
		}
	}

	target := data.Lowest()
	extras := max(len(data.Reviews)-1, 0)
	plan := r.planBody(target, extras, st, head)
	res = pr.ReviewSubmitResult{
		State:               "pending",
		HeadSHA:             head,
		HeadMoved:           moved,
		LiveHeadSHA:         live,
		Status:              pr.StatusNoChange,
		AlreadyPresent:      alreadyPresent,
		Dismissed:           dismissed,
		Body:                plan.disposition,
		ExtraPendingReviews: extras,
		LastAppend:          lastAppendOf(st),
	}
	if len(toWrite) == 0 && !plan.write {
		if target == nil {
			res.State = pr.StateNone
		} else {
			res.ReviewID, res.URL = target.ID, target.URL
		}
		res.AsOf = nowRFC3339()
		return res, false, nil
	}

	// Saved at an earlier head: resolve every new point's diff position before
	// anything is written, so a failed read leaves the host untouched. A point
	// that does not resolve is a per-item anchor_rejected, not an abort.
	anchors, err := r.resolveAnchors(ctx, toWrite, moved, baseOID, head)
	if err != nil {
		return res, false, err
	}

	created := false
	if target == nil {
		// GitHub refuses to edit a review whose body is empty (pg2-16jqj), so
		// a review created without a section still carries the attribution
		// line; a later request's section is then appended after it.
		body := attribution(head)
		if moved {
			body += "\n\n" + savedAtLine(head, live)
		}
		if plan.write {
			body = buildSection(head, r.sectionText)
		}
		rev, err := r.b.gh.CreateBodyOnlyPendingReview(ctx, r.repo, r.number, head, body)
		if errors.Is(err, github.ErrPendingReviewExists) {
			return res, true, nil
		}
		if err != nil {
			return res, false, classifyReviewSubmitError(err)
		}
		if rev == nil || rev.NodeID == "" {
			return res, false, scriptout.WrapError(scriptout.ErrUnavailable,
				"review_submit: the host created a pending review but did not identify it; retry the identical request")
		}
		created = true
		// The cross-process create race is detected, not repaired: look again,
		// and append to the lowest-numbered review whatever is found.
		seen, err := r.b.gh.GetPendingReview(ctx, r.repo, r.number)
		if err != nil {
			return res, false, wrapAfterWrite(classifyReviewSubmitError(err), "a pending review was created")
		}
		target = seen.Lowest()
		if target == nil {
			return res, false, scriptout.WrapError(scriptout.ErrUnavailable,
				"review_submit: the pending review that was just created is not visible; retry the identical request")
		}
		extras = max(len(seen.Reviews)-1, 0)
		res.ExtraPendingReviews = extras
		if extras > 0 && plan.write {
			plan = bodyPlan{disposition: pr.BodySkippedExtraPending}
		}

	}

	wroteBody := created && plan.write
	if !created && plan.write {
		body, wrote, restart, err := r.writeBody(ctx, target.ID, head)
		if err != nil {
			return res, false, err
		}
		if restart {
			return res, true, nil
		}
		plan.disposition, wroteBody = body, wrote
	}
	res.Body = plan.disposition

	var results []github.ReviewWriteResult
	if len(toWrite) > 0 {
		results = make([]github.ReviewWriteResult, len(toWrite))
		var sendIdx []int
		var writeItems []github.ReviewWriteItem
		for i, it := range toWrite {
			if anchors[i].rejected {
				results[i] = github.ReviewWriteResult{Reason: github.ReasonAnchorRejected}
				continue
			}
			sendIdx = append(sendIdx, i)
			writeItems = append(writeItems, r.writeItem(it, head, anchors[i]))
		}
		if len(writeItems) > 0 {
			sent, err := r.b.gh.WriteReviewItems(ctx, target.ID, writeItems)
			if err != nil {
				return res, false, scriptout.WrapError(scriptout.ErrInvalidArgument, "review_submit: "+err.Error())
			}
			for k, i := range sendIdx {
				if k < len(sent) {
					results[i] = sent[k]
				} else {
					results[i] = github.ReviewWriteResult{Reason: github.ReasonUnconfirmed}
				}
			}
		}
	}

	// Reconcile by a re-read: the markers found on the review decide what
	// landed, never the answers of the write calls.
	final, err := r.b.gh.GetPendingReview(ctx, r.repo, r.number)
	var finalRev *github.PendingReviewNode
	if err == nil {
		finalRev = reviewByID(final, target.ID)
	}
	if finalRev == nil {
		cause := "the review could not be found again"
		if err != nil {
			cause = err.Error()
		}
		return res, false, scriptout.WrapError(scriptout.ErrUnavailable,
			fmt.Sprintf("review_submit: writes were sent but could not be confirmed by a re-read (%s); nothing was recorded; retry the identical request", cause))
	}
	onHost := hostFingerprints(final)
	var failures []submitFailure
	var confirmed []string
	for i, it := range toWrite {
		if onHost[it.fp] {
			confirmed = append(confirmed, it.fp)
			continue
		}
		reason := github.ReasonUnconfirmed
		if i < len(results) && !results[i].Landed && results[i].Reason != "" {
			reason = results[i].Reason
		}
		failures = append(failures, submitFailure{reason: reason, item: it})
	}
	bodyConfirmed := false
	if wroteBody {
		_, _, bodyConfirmed = pgposted.FindSection(finalRev.Body, head)
	}

	res.ReviewID, res.URL = finalRev.ID, finalRev.URL
	res.ExtraPendingReviews = max(len(final.Reviews)-1, 0)
	res.Added = len(confirmed)
	wrote := created || bodyConfirmed || len(confirmed) > 0
	switch {
	case created:
		res.Status = pr.StatusPosted
	case wrote:
		res.Status = pr.StatusAppend
	}

	if wrote {
		now := nowRFC3339()
		st.AddFingerprints(confirmed...)
		if bodyConfirmed && res.ExtraPendingReviews == 0 {
			st.AddBodyHead(head)
		}
		st.LastAppend = &pgposted.LastAppend{At: now, Added: len(confirmed), Head: head}
		if err := r.b.posted.Save(r.owner, r.name, r.number, st); err != nil {
			return res, false, scriptout.WrapError(scriptout.ErrUnavailable,
				fmt.Sprintf("review_submit: content was written to %s but the posted-sidecar could not be saved (%v); retry the identical request, which converges through the markers", finalRev.ID, err))
		}
		res.LastAppend = lastAppendOf(st)
	}
	res.AsOf = nowRFC3339()
	if len(failures) > 0 {
		return res, false, failedError(len(confirmed), len(toWrite), failures)
	}
	return res, false, nil
}

// writeBody adds the section for head to the body of the pending review id.
// It re-reads the review immediately before the write and builds the new body
// from that fresh read, so text the operator typed meanwhile survives. again is
// true when the review is no longer the one the run planned for.
func (r *submitRun) writeBody(ctx context.Context, reviewID, head string) (disposition string, wrote, again bool, err error) {
	fresh, err := r.b.gh.GetPendingReview(ctx, r.repo, r.number)
	if err != nil {
		return "", false, false, classifyReviewSubmitError(err)
	}
	if fresh == nil || fresh.Lowest() == nil || fresh.Lowest().ID != reviewID {
		return "", false, true, nil
	}
	rev := fresh.Lowest()
	switch {
	case len(fresh.Reviews) > 1:
		return pr.BodySkippedExtraPending, false, false, nil
	}
	if _, _, ok := pgposted.FindSection(rev.Body, head); ok {
		return pr.BodyKept, false, false, nil
	}
	body := appendSection(rev.Body, buildSection(head, r.sectionText))
	if len(body) > maxReviewBody {
		return pr.BodyTooLarge, false, false, nil
	}
	if err := r.b.gh.UpdateReviewBody(ctx, reviewID, body); err != nil {
		if errors.Is(err, github.ErrTwoPendingReviews) {
			return pr.BodySkippedExtraPending, false, false, nil
		}
		if errors.Is(err, github.ErrEmptyReviewBody) {
			return pr.BodySkippedEmptyReview, false, false, nil
		}
		return "", false, false, classifyReviewSubmitError(err)
	}
	return pr.BodyWritten, true, false, nil
}

// writeItem renders one item for the write call: the request text, then the
// attribution line and the hidden fingerprint marker.
func (r *submitRun) writeItem(it submitItem, head string, a anchor) github.ReviewWriteItem {
	body := it.body + "\n\n" + attribution(head) + "\n" + pgposted.Marker(it.fp)
	if it.reply {
		return github.ReviewWriteItem{Body: body, ReplyToThreadID: it.threadID}
	}
	if a.atCommit {
		return github.ReviewWriteItem{Path: a.path, CommitOID: head, Position: a.position, Body: body}
	}
	return github.ReviewWriteItem{Path: it.path, Line: it.line, Side: it.side, Body: body}
}

// attribution is the visible line every written comment ends with.
func attribution(head string) string {
	return "*Posted by pg-connector at " + short7(head) + ".*"
}

func short7(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// savedAtLine is the plain line a review saved at an earlier head carries in
// its body, so an operator submitting it can tell a mix of comments anchored
// at different commits apart (INV-REVHEAD-2).
func savedAtLine(saved, live string) string {
	return fmt.Sprintf("Saved at %s; PR head was %s when saved.", short7(saved), short7(live))
}

// saveAtEarlierHead decides whether a head_sha that is not the live head may
// be saved at (INV-REVHEAD-1). It returns the commit as the host spells it
// and the current tip of the PR's base branch, or an error with nothing
// written: invalid_argument when the sha is not a commit of the PR,
// unavailable when the commit list could not be read in full.
func (r *submitRun) saveAtEarlierHead(ctx context.Context, live string) (head, baseOID string, err error) {
	if len(r.req.Comments) > maxOlderHeadComments {
		return "", "", scriptout.WrapError(scriptout.ErrInvalidArgument,
			fmt.Sprintf("review_submit: %d comments exceed the limit of %d per request saved at an earlier head; "+
				"split them across requests; nothing was written", len(r.req.Comments), maxOlderHeadComments))
	}
	hist, err := r.b.gh.GetPRHistory(ctx, r.repo, r.number)
	if err != nil {
		return "", "", classifyReviewSubmitError(err)
	}
	if hist == nil {
		return "", "", scriptout.WrapError(scriptout.ErrUnavailable,
			fmt.Sprintf("review_submit: could not read the commits of %s; nothing was written", r.req.ID))
	}
	for _, sha := range hist.CommitSHAs {
		if strings.EqualFold(sha, r.req.HeadSHA) {
			if !github.IsFullSHA(hist.BaseOID) {
				return "", "", scriptout.WrapError(scriptout.ErrUnavailable,
					fmt.Sprintf("review_submit: could not determine the base of %s; nothing was written", r.req.ID))
			}
			return sha, hist.BaseOID, nil
		}
	}
	if hist.Truncated {
		return "", "", scriptout.WrapError(scriptout.ErrUnavailable,
			fmt.Sprintf("review_submit: head_sha %s was not found among the commits read of %s, but the commit list is longer than "+
				"could be read, so it cannot be ruled out; nothing was written", r.req.HeadSHA, r.req.ID))
	}
	return "", "", scriptout.WrapError(scriptout.ErrInvalidArgument,
		fmt.Sprintf("review_submit: head moved: head_sha %s is not the current head of %s (current head is %s) and is not a commit of "+
			"the pull request; refresh the PR and retry against the current head, or a commit it still has; nothing was written",
			r.req.HeadSHA, r.req.ID, live))
}

// anchor is where one comment to write goes: by diff position at a named
// commit (atCommit), by line and side at the live head (neither flag), or
// refused because its line is not in the diff at the saved-at head (rejected).
type anchor struct {
	atCommit bool
	rejected bool
	path     string
	position int
}

// resolveAnchors resolves the new points of toWrite when the review is saved at
// an earlier head; otherwise every comment keeps its line-and-side anchor. The
// compared patches are read only when a new point needs them.
func (r *submitRun) resolveAnchors(ctx context.Context, toWrite []submitItem, moved bool, baseOID, head string) ([]anchor, error) {
	out := make([]anchor, len(toWrite))
	if !moved {
		return out, nil
	}
	seen := map[string]bool{}
	var paths []string
	for _, it := range toWrite {
		if !it.reply && !seen[it.path] {
			seen[it.path] = true
			paths = append(paths, it.path)
		}
	}
	if len(paths) == 0 {
		return out, nil
	}
	files, err := r.b.gh.GetComparedFiles(ctx, r.repo, baseOID, head, paths)
	if err != nil {
		return nil, classifyReviewSubmitError(err)
	}
	for i, it := range toWrite {
		if it.reply {
			continue
		}
		path, pos, ok := github.ResolveAnchor(files, it.path, it.side, it.line)
		if !ok {
			out[i] = anchor{rejected: true}
			continue
		}
		out[i] = anchor{atCommit: true, path: path, position: pos}
	}
	return out, nil
}

func nowRFC3339() string { return time.Now().UTC().Format(time.RFC3339) }

// lastAppendOf renders the sidecar's last append for the result, nil for none.
func lastAppendOf(st pgposted.State) *pr.LastAppend {
	if st.LastAppend == nil {
		return nil
	}
	return &pr.LastAppend{At: st.LastAppend.At, Added: st.LastAppend.Added, Head: st.LastAppend.Head}
}

// submitFailure is one comment that did not land.
type submitFailure struct {
	reason github.WriteFailReason
	item   submitItem
}

// failedError builds the error for a run in which some comments did not land.
// The error envelope carries only code and message, so the message has a
// stable shape:
//
//	<n> of <m> comments landed; failed: <reason>:<fingerprint>[:<path>:<line>] ...
//
// It lists at most maxFailuresListed failures and ends with "(+<k> more)" when
// there were more. The code is unavailable when any failure could succeed on
// retry (rate_limited, unconfirmed) and invalid_argument when every failure is
// permanent (anchor_rejected, thread_not_found).
func failedError(landed, total int, failures []submitFailure) error {
	listed := failures
	if len(listed) > maxFailuresListed {
		listed = listed[:maxFailuresListed]
	}
	parts := make([]string, 0, len(listed))
	retryable := false
	for _, f := range failures {
		if f.reason == github.ReasonRateLimited || f.reason == github.ReasonUnconfirmed {
			retryable = true
		}
	}
	for _, f := range listed {
		p := string(f.reason) + ":" + f.item.fp
		if !f.item.reply {
			p += fmt.Sprintf(":%s:%d", f.item.path, f.item.line)
		}
		parts = append(parts, p)
	}
	msg := fmt.Sprintf("%d of %d comments landed; failed: %s", landed, total, strings.Join(parts, " "))
	if extra := len(failures) - len(listed); extra > 0 {
		msg += fmt.Sprintf(" (+%d more)", extra)
	}
	if retryable {
		return scriptout.WrapError(scriptout.ErrUnavailable, msg)
	}
	return scriptout.WrapError(scriptout.ErrInvalidArgument, msg)
}

// wrapAfterWrite notes on err that a write already reached the host, so a
// caller knows the identical request converges.
func wrapAfterWrite(err error, what string) error {
	return fmt.Errorf("%w; %s, so retry the identical request", err, what)
}

// classifyReviewSubmitError maps a host failure onto INV-ERR-1. An HTTP 422 is
// invalid_argument (the host rejected what was sent); auth failures and 404
// reuse classifyGHError (deliberately not edited: it is hash-pinned);
// everything else, rate limits included, is unavailable and is not retried
// inside the call.
func classifyReviewSubmitError(err error) error {
	if strings.Contains(strings.ToLower(err.Error()), "http 422") {
		return scriptout.WrapError(scriptout.ErrInvalidArgument, "review_submit: GitHub rejected the request (HTTP 422): "+err.Error())
	}
	err = classifyGHError(err)
	for _, s := range []error{scriptout.ErrNotFound, scriptout.ErrUnauthenticated, scriptout.ErrInvalidArgument} {
		if errors.Is(err, s) {
			return err
		}
	}
	return scriptout.WrapError(scriptout.ErrUnavailable, err.Error())
}
