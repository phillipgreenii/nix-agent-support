package review

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/github"
	pgposted "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/posted"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/pr"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// submitRun is one review_submit call, holding the per-PR lock.
type submitRun struct {
	gh       Provider
	store    pgposted.Store
	clock    governor.Clock
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
	// touched is true once the run changed, or may have changed, the pending
	// review on the host: a review was created, a section was written, or an item
	// came back landed or unconfirmed. The handler invalidates the pending group
	// when it is true, whatever the run's outcome.
	touched bool
}

// now is the run's clock as the RFC 3339 UTC string the result carries.
func (r *submitRun) now() string { return r.clock.Now().UTC().Format(time.RFC3339) }

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
	data, err := r.gh.GetPendingReview(ctx, r.repo, r.number)
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
	st, err := r.store.Load(r.owner, r.name, r.number)
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
		res.AsOf = r.now()
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
		rev, err := r.gh.CreateBodyOnlyPendingReview(ctx, r.repo, r.number, head, body)
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
		r.touched = true
		// The cross-process create race is detected, not repaired: look again,
		// and append to the lowest-numbered review whatever is found.
		seen, err := r.gh.GetPendingReview(ctx, r.repo, r.number)
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
		r.touched = r.touched || wrote
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
			sent, err := r.gh.WriteReviewItems(ctx, target.ID, writeItems)
			if err != nil {
				return res, false, scriptout.WrapError(scriptout.ErrInvalidArgument, "review_submit: "+err.Error())
			}
			for k, i := range sendIdx {
				if k < len(sent) {
					results[i] = sent[k]
				} else {
					results[i] = github.ReviewWriteResult{Reason: github.ReasonUnconfirmed}
				}
				// An unconfirmed item may have landed: the answer was lost.
				if results[i].Landed || results[i].Reason == github.ReasonUnconfirmed {
					r.touched = true
				}
			}
		}
	}

	// Reconcile by a re-read: the markers found on the review decide what
	// landed, never the answers of the write calls.
	final, err := r.gh.GetPendingReview(ctx, r.repo, r.number)
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
		now := r.now()
		st.AddFingerprints(confirmed...)
		if bodyConfirmed && res.ExtraPendingReviews == 0 {
			st.AddBodyHead(head)
		}
		st.LastAppend = &pgposted.LastAppend{At: now, Added: len(confirmed), Head: head}
		if err := r.store.Save(r.owner, r.name, r.number, st); err != nil {
			return res, false, scriptout.WrapError(scriptout.ErrUnavailable,
				fmt.Sprintf("review_submit: content was written to %s but the posted-sidecar could not be saved (%v); retry the identical request, which converges through the markers", finalRev.ID, err))
		}
		res.LastAppend = lastAppendOf(st)
	}
	res.AsOf = r.now()
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
	fresh, err := r.gh.GetPendingReview(ctx, r.repo, r.number)
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
	if err := r.gh.UpdateReviewBody(ctx, reviewID, body); err != nil {
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
	hist, err := r.gh.GetPRHistory(ctx, r.repo, r.number)
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
	files, err := r.gh.GetComparedFiles(ctx, r.repo, baseOID, head, paths)
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
