package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/interpret"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/pkg/deskapi"
)

// reviewNow is the clock the last_append age is measured against; a test seam.
var reviewNow interpret.Clock = interpret.SystemClock{}

// The pending-review state of a PR in the composite view [pg2-kftf9.18,
// pending-review investigation policy 11]. It is read from the facts the
// generic PR hydration stored (the pg-connector `pr review pending` record);
// the view itself looks nothing up and changes nothing. Facts stored before
// the escalation fact was retired may still carry review_escalations or
// digest_state; they are ignored.

// Review states in viewReview.State.
const (
	reviewStateNone    = deskapi.ReviewNone    // the lookup affirmatively found no pending review
	reviewStateCurrent = deskapi.ReviewCurrent // a pending review with something anchored to the PR head
	reviewStateStale   = deskapi.ReviewStale   // a pending review with nothing anchored to the PR head
	reviewStateUnknown = deskapi.ReviewUnknown // the lookup failed or was never made, or predates the per-head counts
)

// buildReview derives a PR's review state from its stored facts JSON. Facts
// stored before the lookup existed, or by a hydration that skipped it, carry
// neither key and read as unknown.
func buildReview(factsJSON string) *viewReview {
	var facts struct {
		ReviewPending *struct {
			Result json.RawMessage `json:"result"`
			Error  string          `json:"error"`
		} `json:"review_pending"`
	}
	_ = json.Unmarshal([]byte(factsJSON), &facts)

	r := &viewReview{State: reviewStateUnknown}

	switch p := facts.ReviewPending; {
	case p == nil:
		r.Error = "pending-review state was not looked up; run show --refresh"
	case p.Error != "":
		r.Error = p.Error
	default:
		decodeReviewRecord(r, p.Result)
	}

	return r
}

// decodeReviewRecord fills r from the connector's review_pending record. A
// record it cannot read leaves r unknown with the reason, never none. The
// stale verdict is the connector's: it is read, not recomputed, so a record
// that predates the per-head counts (no comments_at_head, or no stale) reads
// unknown and asks for a refresh rather than guessing from the commits.
func decodeReviewRecord(r *viewReview, raw json.RawMessage) {
	var rec struct {
		Pending *bool  `json:"pending"`
		HeadSHA string `json:"head_sha"`
		Review  *struct {
			ReviewID            string `json:"review_id"`
			URL                 string `json:"url"`
			Commit              string `json:"commit_sha"`
			CommentsTotal       *int   `json:"comments_total"`
			CommentsAtHead      *int   `json:"comments_at_head"`
			Stale               *bool  `json:"stale"`
			ExtraPendingReviews int    `json:"extra_pending_reviews"`
			LastAppend          *struct {
				At    string `json:"at"`
				Added int    `json:"added"`
				Head  string `json:"head"`
			} `json:"last_append"`
		} `json:"review"`
	}
	if err := json.Unmarshal(raw, &rec); err != nil || rec.Pending == nil {
		r.Error = "the stored pending-review record is unreadable"
		return
	}
	pending := *rec.Pending
	switch {
	case !pending:
		r.State, r.Pending, r.HeadSHA = reviewStateNone, &pending, rec.HeadSHA
	case rec.Review == nil:
		r.Error = "the stored pending-review record says pending but carries no review"
	case rec.Review.CommentsAtHead == nil || rec.Review.CommentsTotal == nil || rec.Review.Stale == nil:
		r.Error = "the stored pending-review record predates the per-head comment counts; run show --refresh"
	default:
		rv := rec.Review
		r.State = reviewStateCurrent
		if *rv.Stale {
			r.State = reviewStateStale
		}
		r.Pending, r.Stale = &pending, rv.Stale
		r.ReviewID, r.URL = rv.ReviewID, rv.URL
		r.AnchoredCommit, r.HeadSHA = rv.Commit, rec.HeadSHA
		r.CommentsTotal, r.CommentsAtHead = rv.CommentsTotal, rv.CommentsAtHead
		extra := rv.ExtraPendingReviews
		r.ExtraPendingReviews = &extra
		if la := rv.LastAppend; la != nil {
			r.LastAppend = &viewLastAppend{At: la.At, Added: la.Added, Head: la.Head}
		}
	}
}

// renderReviewLine is the human line for the review state, e.g.
//
//	review: pending=yes  commit=4b1d7aa  head=9f3c1e2  comments=5 at_head=0  stale=yes  last_append=2h (+3)
//	review: pending=no
//	review: pending=unknown (<reason>)
func renderReviewLine(r *viewReview) string {
	parts := []string{}
	switch r.State {
	case reviewStateNone:
		parts = append(parts, "pending=no")
	case reviewStateCurrent, reviewStateStale:
		parts = append(parts, "pending=yes", "commit="+orDash(shortSHA(r.AnchoredCommit)), "head="+orDash(shortSHA(r.HeadSHA)),
			fmt.Sprintf("comments=%d at_head=%d", intOr0(r.CommentsTotal), intOr0(r.CommentsAtHead)),
			"stale="+yesNo(r.State == reviewStateStale))
		if la := r.LastAppend; la != nil {
			parts = append(parts, fmt.Sprintf("last_append=%s (+%d)", appendAge(la.At, reviewNow.Now()), la.Added))
		}
		if n := intOr0(r.ExtraPendingReviews); n > 0 {
			parts = append(parts, fmt.Sprintf("extra=%d", n))
		}
	default:
		parts = append(parts, "pending=unknown ("+oneLine(r.Error)+")")
	}
	return "review: " + strings.Join(parts, "  ")
}

func shortSHA(s string) string {
	if len(s) > 7 {
		return s[:7]
	}
	return s
}

// oneLine collapses whitespace so an error never breaks the line.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func intOr0(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

// appendAge renders how long ago an RFC 3339 instant was as one unit: 45s,
// 5m, 2h, 3d. An unreadable instant reads "?" and a future one "0s".
func appendAge(at string, now time.Time) string {
	t, err := time.Parse(time.RFC3339, at)
	if err != nil {
		return "?"
	}
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(max(d, 0)/time.Second))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d/time.Minute))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d/time.Hour))
	default:
		return fmt.Sprintf("%dd", int(d/(24*time.Hour)))
	}
}
