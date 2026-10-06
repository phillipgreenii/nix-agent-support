// activity_review.go: the repeatable activity kinds pr.reviewed and
// pr.commented. Unlike pr.opened/merged/closed, a PR can carry many of
// these, so each item's id is "<OWNER/REPO#N>#<kind>#<event id>" where the
// event id is the system's own node id (the review's, or the comment's), and
// an item is dated by that review's submitted_at or that comment's own
// created_at, never by the PR's updated_at.
//
// Candidate discovery is a day-granular GitHub search (the "updated:"
// qualifier takes dates only): "reviewed-by:<viewer>" for reviews and
// "commenter:<viewer>" for comments, widened to whole UTC days. Each
// candidate PR's reviews/comments are then read and filtered by the item's
// own timestamp, inclusive since and exclusive before. Known limit: a
// candidate is found through the PR's last-updated day, so a review or
// comment whose PR was updated again after the range ends is not discovered
// by an end-bounded search; that is the search qualifier's reach, and the
// result is never padded with an approximate timestamp.
//
// Documented item "fields" keys (an object, always present):
//
//	pr.reviewed:  title, author (the PR's author), state
//	              (approved, changes-requested, commented or dismissed)
//	pr.commented: title, author (the PR's author)
package internal

import (
	"context"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-pr-github/internal/api"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
)

// Activity kinds this file emits.
const (
	KindPRReviewed  = "pr.reviewed"
	KindPRCommented = "pr.commented"
)

// ActivityKinds is the full vocabulary this backend emits, for capabilities
// vocabulary.activity_kinds.
var ActivityKinds = append(append([]string{}, ActivityKindsOnce...), KindPRReviewed, KindPRCommented)

// activityDayQualifier renders GitHub's day-granular "updated:" qualifier
// covering [since, before) widened to whole UTC days. A zero since uses the
// upper-bound-only form. The caller still filters each item by its own
// timestamp.
func activityDayQualifier(key string, since, before time.Time) string {
	const day = "2006-01-02"
	b := before.UTC().Format(day)
	if since.IsZero() {
		return key + ":<=" + b
	}
	return key + ":" + since.UTC().Format(day) + ".." + b
}

// reviewStateField normalizes GitHub's review state (APPROVED,
// CHANGES_REQUESTED, COMMENTED, DISMISSED) to the lowercase hyphenated form.
func reviewStateField(state string) string {
	return strings.ReplaceAll(strings.ToLower(state), "_", "-")
}

// collectReviewCommentKinds emits pr.reviewed for each review the viewer
// submitted in [since, before) and pr.commented for each top-level PR comment
// the viewer wrote in it. truncated is true when a candidate search returned
// the GitHub search cap.
func (b *Backend) collectReviewCommentKinds(ctx context.Context, viewer string, since, before time.Time) (items []schema.ActivityItem, truncated bool, err error) {
	reviewed, capped, err := b.searchActivity(ctx, "reviewed-by:"+viewer+" "+activityDayQualifier("updated", since, before))
	if err != nil {
		return nil, false, err
	}
	truncated = truncated || capped
	reviewItems, err := b.reviewedItems(ctx, viewer, since, before, reviewed)
	if err != nil {
		return nil, false, err
	}
	items = append(items, reviewItems...)

	commented, capped, err := b.searchActivity(ctx, "commenter:"+viewer+" "+activityDayQualifier("updated", since, before))
	if err != nil {
		return nil, false, err
	}
	truncated = truncated || capped
	commentItems, err := b.commentedItems(ctx, viewer, since, before, commented)
	if err != nil {
		return nil, false, err
	}
	items = append(items, commentItems...)

	return dedupeActivity(items), truncated, nil
}

// reviewedItems reads each candidate's reviews with their submitted_at and
// builds a pr.reviewed item per review the viewer submitted in range. A review
// without a submitted_at (pending, unsubmitted) is not emitted.
func (b *Backend) reviewedItems(ctx context.Context, viewer string, since, before time.Time, cands []api.PR) ([]schema.ActivityItem, error) {
	cands = uniquePRs(cands)
	got, err := parallelMap(ctx, cands, func(ctx context.Context, c api.PR) ([]api.Review, error) {
		rs, rerr := b.gh.ListReviewsSubmitted(ctx, c.Repo, c.Number)
		if rerr != nil {
			if isGHNotFound(rerr) {
				return nil, nil
			}
			return nil, rerr
		}
		return rs, nil
	})
	if err != nil {
		return nil, classifyGHError(err)
	}
	var items []schema.ActivityItem
	for i, c := range cands {
		for _, r := range got[i] {
			if r.ID == "" || !sameLogin(r.Author, viewer) || !inActivityRange(r.SubmittedAt, since, before) {
				continue
			}
			state := reviewStateField(r.State)
			id := formatPRID(c.Repo, c.Number)
			it := newActivityItem(id, KindPRReviewed, r.SubmittedAt, "reviewed "+id+" ("+state+"): "+c.Title, c.URL, []string{"repo:" + c.Repo}, map[string]any{
				"title": c.Title, "author": c.Author, "state": state,
			})
			it.ID = id + "#" + KindPRReviewed + "#" + r.ID
			items = append(items, it)
		}
	}
	return items, nil
}

// commentedItems reads each candidate's comments and builds a pr.commented
// item per top-level (issue) comment the viewer wrote in range. Inline
// review-thread comments (non-empty Path/ReviewID) and comments without their
// own created_at are not emitted.
func (b *Backend) commentedItems(ctx context.Context, viewer string, since, before time.Time, cands []api.PR) ([]schema.ActivityItem, error) {
	cands = uniquePRs(cands)
	got, err := parallelMap(ctx, cands, func(ctx context.Context, c api.PR) ([]api.Comment, error) {
		cs, cerr := b.gh.ListComments(ctx, c.Repo, c.Number)
		if cerr != nil {
			if isGHNotFound(cerr) {
				return nil, nil
			}
			return nil, cerr
		}
		return cs, nil
	})
	if err != nil {
		return nil, classifyGHError(err)
	}
	var items []schema.ActivityItem
	for i, c := range cands {
		for _, cm := range got[i] {
			if cm.ID == "" || cm.Path != "" || cm.ReviewID != "" || cm.ThreadID != "" {
				continue
			}
			if !sameLogin(cm.Author, viewer) || !inActivityRange(cm.CreatedAt, since, before) {
				continue
			}
			id := formatPRID(c.Repo, c.Number)
			it := newActivityItem(id, KindPRCommented, cm.CreatedAt, "commented on "+id+": "+c.Title, c.URL, []string{"repo:" + c.Repo}, map[string]any{
				"title": c.Title, "author": c.Author,
			})
			it.ID = id + "#" + KindPRCommented + "#" + cm.ID
			items = append(items, it)
		}
	}
	return items, nil
}

// uniquePRs drops repeated repo/number pairs, keeping the first.
func uniquePRs(prs []api.PR) []api.PR {
	seen := make(map[string]bool, len(prs))
	out := make([]api.PR, 0, len(prs))
	for _, p := range prs {
		k := formatPRID(p.Repo, p.Number)
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, p)
	}
	return out
}
