// Package beads provides typed wrappers around the bd CLI for the four
// pg-pr bead concepts: merge-request, processing-cycle, feedback, action.
// Phase 0 stubs only; bd shell-out implementation lands in Phase 1.
package beads

import "errors"

var ErrNotImplemented = errors.New("beads: not implemented in this phase")

// Type values used in bd custom-type config and in metadata.
const (
	TypeMergeRequest = "merge-request"
	TypeFeedback     = "feedback"
	// Processing-cycle and action beads use bd builtin task/bug types.
)

// FeedbackKind enumerates the upstream events feedback beads represent.
type FeedbackKind string

const (
	FeedbackKindCommentThread FeedbackKind = "comment-thread"
	FeedbackKindCIFailure     FeedbackKind = "ci-failure"
	FeedbackKindReviewThread  FeedbackKind = "review-thread"
	FeedbackKindReviewRequest FeedbackKind = "review-request"
	FeedbackKindJiraLink      FeedbackKind = "jira-link"
)

// AuthorRole enumerates author precedence levels.
type AuthorRole string

const (
	AuthorRoleSelf       AuthorRole = "self"
	AuthorRoleTeamMember AuthorRole = "team_member"
	AuthorRoleOrgMember  AuthorRole = "org_member"
	AuthorRoleBot        AuthorRole = "bot"
)
