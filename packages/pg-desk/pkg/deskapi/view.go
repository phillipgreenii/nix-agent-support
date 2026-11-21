package deskapi

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// ViewContract is the contract string every composite view carries.
const ViewContract = "pg-desk.view/v1"

// Review states in Review.State.
const (
	ReviewNone    = "none"    // the lookup affirmatively found no pending review
	ReviewCurrent = "current" // a pending review with something anchored to the PR head
	ReviewStale   = "stale"   // a pending review with nothing anchored to the PR head
	ReviewUnknown = "unknown" // the lookup failed or was never made, or predates the per-head counts
)

// View is the composite view of one stored entity (pg-desk.view/v1).
type View struct {
	Contract    string          `json:"contract"`
	Type        string          `json:"type"`
	ID          string          `json:"id"`
	Version     int64           `json:"version"`
	AsOf        string          `json:"as_of"`
	Stale       bool            `json:"stale"`
	Snapshot    json.RawMessage `json:"snapshot"`
	Decorations Decorations     `json:"decorations"`
	Annotations Annotations     `json:"annotations"`
	// Review is a PR's pending-agent-review state (pr only; omitted for the
	// other types).
	Review *Review `json:"review,omitempty"`
	Links  []Link  `json:"links"`
	// LinksAsOf is the newest time any of the entity's links was last
	// confirmed; null when it has no links.
	LinksAsOf *string `json:"links_as_of"`
	// CI is a PR's CI runs for its head commit (pr only; omitted for the
	// other types). It is the last member: an additive extension of
	// pg-desk.view/v1.
	CI *CI `json:"ci,omitempty"`
}

// CI is the ci section: raw stored CI run data with no computed verdict.
// A build id is a run's id plus its attempt.
type CI struct {
	Runs []CIRun `json:"runs"`
}

// CIRun is one run, named after pg-connector's schema.CIRun fields
// (which pg-desk does not import). Attempt is 0 when the backend reported
// none.
type CIRun struct {
	ID         string `json:"id"`
	Attempt    int    `json:"attempt"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	URL        string `json:"url"`
	// Required is pg-connector's per-run `required` fact passed through: true
	// when the merge gate requires the run's check, false when it does not, and
	// omitted when the connector did not say (a consumer treats that as
	// required).
	Required *bool `json:"required,omitempty"`
}

// Decorations are the stored interpretation of the entity.
type Decorations struct {
	Relationship string        `json:"relationship"`
	Dispositions []Disposition `json:"dispositions"`
	// Urgency is the stored urgency level ("" when the entity has no stored
	// interpretation).
	Urgency  string `json:"urgency"`
	Category string `json:"category"`
}

// Disposition is one comment disposition: the computed verdict and the
// operator override, if any.
type Disposition struct {
	CommentID string  `json:"comment_id"`
	Computed  string  `json:"computed"`
	Override  *string `json:"override"`
}

// Hidden is the decoded hidden annotation.
type Hidden struct {
	Value  bool    `json:"value"`
	Reason *string `json:"reason"`
}

// Annotations are the entity annotations the view exposes.
type Annotations struct {
	Hidden      Hidden   `json:"hidden"`
	WIP         bool     `json:"wip"`
	Suppress    []string `json:"suppress"`
	ForceReview bool     `json:"force_review"`
	// ForceReviewSHA is the head SHA the force_review flag was requested at,
	// or null when no flag is set.
	ForceReviewSHA *string `json:"force_review_sha"`
	// ReadyToLand is the reserved ready_to_land annotation: true or false
	// once written, null when unset.
	ReadyToLand *bool `json:"ready_to_land"`
	// FocusSelected is the reserved focus_selected annotation: a period key
	// or "none" once written, null when unset. It is always emitted, so a
	// reader can tell an unset selection from a view that predates the member.
	FocusSelected *string                      `json:"focus_selected"`
	Decider       map[string]map[string]string `json:"decider"`
}

// Link is one links[] entry. State, Labels, Metadata and Assignee are
// present only when the linked entity has a stored row; Assignee is "" when
// it is unclaimed. There is deliberately no closed_by [design 9.5, S26].
type Link struct {
	Type     string `json:"type"`
	ID       string `json:"id"`
	Relation string `json:"relation"`
	// Title is the linked entity's stored title, "" when none is stored.
	Title    string             `json:"title"`
	URL      string             `json:"url,omitempty"`
	State    string             `json:"state,omitempty"`
	Labels   *[]string          `json:"labels,omitempty"`
	Metadata *map[string]string `json:"metadata,omitempty"`
	Assignee *string            `json:"assignee,omitempty"`
	// Priority is a linked work item's stored priority, backend-native
	// ("P0".."P4" for the beads backend). It is omitted when the linked
	// entity has no stored row or its snapshot carries no priority.
	Priority string   `json:"priority,omitempty"`
	Origins  []Origin `json:"origins"`
}

// Origin is one way a link was discovered.
type Origin struct {
	Origin string `json:"origin"`
	At     string `json:"at,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// hiddenState is the decoded hidden annotation.
// Review is the `review` object of a PR's pg-desk.view/v1 (absent for
// other types). A failed or missing lookup is state "unknown" with the
// reason in Error: it is never reported as "none".
type Review struct {
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
	LastAppend *LastAppend `json:"last_append,omitempty"`
	// ExtraPendingReviews is how many further pending reviews the identity
	// has beyond the one reported; absent without a pending review.
	ExtraPendingReviews *int `json:"extra_pending_reviews,omitempty"`
	// Error is why State is unknown.
	Error string `json:"error,omitempty"`
}

// LastAppend is the last append to a PR's pending review: when, how many
// comments it added, and the head it was made at.
type LastAppend struct {
	At    string `json:"at"`
	Added int    `json:"added"`
	Head  string `json:"head,omitempty"`
}

// EncodeView encodes a composite view as the indented JSON pg-desk prints.
func EncodeView(v View) ([]byte, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode view: %w", err)
	}
	return b, nil
}

// DecodeView strictly decodes a composite view: an unknown member is an error.
func DecodeView(data []byte) (View, error) {
	var v View
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&v); err != nil {
		return View{}, fmt.Errorf("decode view: %w", err)
	}
	return v, nil
}
