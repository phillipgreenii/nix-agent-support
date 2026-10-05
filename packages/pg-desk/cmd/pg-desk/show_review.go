package main

import (
	"encoding/json"
	"strings"
)

// The pending-review state of a PR in the composite view [pg2-kftf9.18,
// pending-review investigation policy 11]. It is read from the facts the
// generic PR hydration stored (the pg-connector `pr review pending` record
// and the open escalation beads covering the PR); the view itself looks
// nothing up and changes nothing. The display is an addition to the
// escalation raised by pg-router-review-escalator, never a substitute.

// Review states in viewReview.State.
const (
	reviewStateNone    = "none"    // the lookup affirmatively found no pending review
	reviewStateCurrent = "current" // a pending review anchored to the PR head
	reviewStateStale   = "stale"   // a pending review anchored to an older commit
	reviewStateUnknown = "unknown" // the lookup failed or was never made
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
	// AnchoredCommit is the review-level commit the pending review is
	// anchored to; HeadSHA is the PR head the lookup compared it with.
	AnchoredCommit string `json:"anchored_commit,omitempty"`
	HeadSHA        string `json:"head_sha,omitempty"`
	// Stale is whether the pending review is anchored to something other than
	// the PR head; null when there is no pending review or State is unknown.
	Stale *bool `json:"stale"`
	// Error is why State is unknown.
	Error      string               `json:"error,omitempty"`
	Escalation viewReviewEscalation `json:"escalation"`
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

// decodeReviewRecord fills r from the 9.1a record. A record it cannot read
// leaves r unknown with the reason, never none.
func decodeReviewRecord(r *viewReview, raw json.RawMessage) {
	var rec struct {
		Pending *bool  `json:"pending"`
		HeadSHA string `json:"head_sha"`
		Review  *struct {
			ReviewID string `json:"review_id"`
			URL      string `json:"url"`
			Commit   string `json:"commit_sha"`
			Stale    *bool  `json:"stale"`
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
	default:
		stale := !strings.EqualFold(rec.Review.Commit, rec.HeadSHA) // an empty commit is stale
		if rec.Review.Stale != nil {
			stale = *rec.Review.Stale
		}
		r.State = reviewStateCurrent
		if stale {
			r.State = reviewStateStale
		}
		r.Pending, r.Stale = &pending, &stale
		r.ReviewID, r.URL = rec.Review.ReviewID, rec.Review.URL
		r.AnchoredCommit, r.HeadSHA = rec.Review.Commit, rec.HeadSHA
	}
}

// renderReviewLine is the human line for the review state, e.g.
//
//	review: pending=yes  commit=4b1d7aa  head=9f3c1e2  stale=yes  escalation=bd-77
//	review: pending=no  escalation=none
//	review: pending=unknown (<reason>)  escalation=unknown (<reason>)
func renderReviewLine(r *viewReview) string {
	parts := []string{}
	switch r.State {
	case reviewStateNone:
		parts = append(parts, "pending=no")
	case reviewStateCurrent, reviewStateStale:
		parts = append(parts, "pending=yes", "commit="+orDash(shortSHA(r.AnchoredCommit)), "head="+orDash(shortSHA(r.HeadSHA)), "stale="+yesNo(r.State == reviewStateStale))
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
