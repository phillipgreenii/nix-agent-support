// Package classify holds the type-agnostic core of pg-desk's change
// classification: the Snapshot input, the Record output, the change-kind
// catalogue, a per-type classifier registry (Strategy per entity type) and
// the top-level Classify that enforces the invariants common to all types
// before delegating to the registered per-type classifier.
//
// Classification is a pure function of (old, new): no clock, no I/O, no
// global state beyond the registry, and nothing here consults decider or
// router configuration.
package classify

// Kind is a change kind. Its value is the exact catalogue string persisted in
// the change log. It deliberately avoids the name used by the gather package's
// hydration hint and the store's change constants, to keep the concepts apart.
type Kind string

// Kinds common to every entity type.
const (
	KindAdded             Kind = "added"
	KindRemoved           Kind = "removed"
	KindReconcile         Kind = "reconcile"
	KindAnnotationChanged Kind = "annotation_changed"
	KindLinkChanged       Kind = "link_changed"
)

// Kinds for the pr type.
const (
	KindOpened              Kind = "opened"
	KindReopened            Kind = "reopened"
	KindClosed              Kind = "closed"
	KindMerged              Kind = "merged"
	KindDraftChanged        Kind = "draft_changed"
	KindHeadChanged         Kind = "head_changed"
	KindBaseChanged         Kind = "base_changed"
	KindCiChanged           Kind = "ci_changed"
	KindMergeabilityChanged Kind = "mergeability_changed"
	KindReviewChanged       Kind = "review_changed"
	KindFeedbackChanged     Kind = "feedback_changed"
	KindWorkChanged         Kind = "work_changed"
)

// Kinds for the issue type (opened, reopened and closed are shared with pr).
const (
	KindStatusChanged   Kind = "status_changed"
	KindAssigneeChanged Kind = "assignee_changed"
	KindCommentsChanged Kind = "comments_changed"
	KindDepsChanged     Kind = "deps_changed"
)

// Kinds for the thread type.
const (
	KindMessageAdded Kind = "message_added"
	KindResolved     Kind = "resolved"
)
