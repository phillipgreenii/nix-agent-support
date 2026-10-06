package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/interpret"
)

// reviewNow is the clock the last_append age is measured against; a test seam.
var reviewNow interpret.Clock = interpret.SystemClock{}

// The pending-review state of a PR in the composite view [pg2-kftf9.18,
// pending-review investigation policy 11]. It is read from the facts the
// generic PR hydration stored (the pg-connector `pr review pending` record
// and the open escalation beads covering the PR); the view itself looks
// nothing up and changes nothing. The display is an addition to the
// escalation raised by pg-router-review-escalator, never a substitute.

// Review states in viewReview.State.
const (
	reviewStateNone    = "none"    // the lookup affirmatively found no pending review
	reviewStateCurrent = "current" // a pending review with something anchored to the PR head
	reviewStateStale   = "stale"   // a pending review with nothing anchored to the PR head
	reviewStateUnknown = "unknown" // the lookup failed or was never made, or predates the per-head counts
)

// Escalation states in viewReviewEscalation.State.
const (
	escalationStateNone    = "none"
	escalationStateOpen    = "open"
	escalationStateUnknown = "unknown"
)

// viewReview is the `review` object of a PR's pg-desk.view/v1 (absent for
// other types). A failed or missing lookup is state "unknown" with the
// reason in Error: it is never reported as "none".
type viewReview struct {
	// State is none, current, stale or unknown.
	State string `json:"state"`
	// Pending is whether the acting identity has a pending review; null when
	// State is unknown.
	Pending *bool `json:"pending"`
	// ReviewID and URL identify the pending review, when there is one.
	ReviewID string `json:"review_id,omitempty"`
	URL      string `json:"url,omitempty"`
	// AnchoredCommit is the review-level commit the pending review was
	// created at; HeadSHA is the PR head the lookup compared against.
	AnchoredCommit string `json:"anchored_commit,omitempty"`
	HeadSHA        string `json:"head_sha,omitempty"`
	// CommentsTotal is how many comments the pending review holds and
	// CommentsAtHead how many of them were made at the current head; both are
	// absent without a pending review.
	CommentsTotal  *int `json:"comments_total,omitempty"`
	CommentsAtHead *int `json:"comments_at_head,omitempty"`
	// Stale is the CONNECTOR's verdict, read from the stored record and never
	// recomputed here: true only when a pending review exists, nothing of it
	// is at the head (comments_at_head 0) and the head is not otherwise
	// reviewed. Null when there is no pending review or State is unknown.
	Stale *bool `json:"stale"`
	// LastAppend is the connector's record of the tool's last append to the
	// pending review; absent when nothing was ever appended.
	LastAppend *viewLastAppend `json:"last_append,omitempty"`
	// ExtraPendingReviews is how many further pending reviews the identity
	// has beyond the one reported; absent without a pending review.
	ExtraPendingReviews *int `json:"extra_pending_reviews,omitempty"`
	// Error is why State is unknown.
	Error      string               `json:"error,omitempty"`
	Escalation viewReviewEscalation `json:"escalation"`
}

// viewLastAppend is the last append to a PR's pending review: when, how many
// comments it added, and the head it was made at.
type viewLastAppend struct {
	At    string `json:"at"`
	Added int    `json:"added"`
	Head  string `json:"head,omitempty"`
}

// viewReviewEscalation is the open escalation beads for the PR. BeadIDs is
// empty exactly when State is none or unknown.
type viewReviewEscalation struct {
	State   string   `json:"state"`
	BeadIDs []string `json:"bead_ids"`
	Error   string   `json:"error,omitempty"`
}

// buildReview derives a PR's review state from its stored facts JSON. Facts
// stored before the lookup existed, or by a hydration that skipped it, carry
// neither key and read as unknown.
func buildReview(factsJSON string) *viewReview {
	var facts struct {
		ReviewPending *struct {
			Result json.RawMessage `json:"result"`
			Error  string          `json:"error"`
		} `json:"review_pending"`
		ReviewEscalations *struct {
			Open []struct {
				ID string `json:"id"`
			} `json:"open"`
			Error string `json:"error"`
		} `json:"review_escalations"`
	}
	_ = json.Unmarshal([]byte(factsJSON), &facts)

	r := &viewReview{State: reviewStateUnknown, Escalation: viewReviewEscalation{State: escalationStateUnknown, BeadIDs: []string{}}}

	switch p := facts.ReviewPending; {
	case p == nil:
		r.Error = "pending-review state was not looked up; run show --refresh"
	case p.Error != "":
		r.Error = p.Error
	default:
		decodeReviewRecord(r, p.Result)
	}

	switch e := facts.ReviewEscalations; {
	case e == nil:
		r.Escalation.Error = "escalations were not looked up; run show --refresh"
	case e.Error != "":
		r.Escalation.Error = e.Error
	case len(e.Open) == 0:
		r.Escalation.State = escalationStateNone
	default:
		r.Escalation.State = escalationStateOpen
		for _, b := range e.Open {
			r.Escalation.BeadIDs = append(r.Escalation.BeadIDs, b.ID)
		}
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
//	review: pending=yes  commit=4b1d7aa  head=9f3c1e2  comments=5 at_head=0  stale=yes  last_append=2h (+3)  escalation=bd-77
//	review: pending=no  escalation=none
//	review: pending=unknown (<reason>)  escalation=unknown (<reason>)
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
	switch r.Escalation.State {
	case escalationStateOpen:
		parts = append(parts, "escalation="+strings.Join(r.Escalation.BeadIDs, ","))
	case escalationStateNone:
		parts = append(parts, "escalation=none")
	default:
		parts = append(parts, "escalation=unknown ("+oneLine(r.Escalation.Error)+")")
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
