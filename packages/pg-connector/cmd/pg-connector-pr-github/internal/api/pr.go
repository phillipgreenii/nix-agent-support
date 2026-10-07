package api

// PR is one PR's live-provider-shaped identity/state. internal/provider.go's
// toSchemaPR maps it onto pkg/schema.PR at the Backend.Show boundary — it is
// not itself marshaled as a command's top-level JSON shape.
type PR struct {
	Repo   string `json:"repo"`
	Number int    `json:"number"`
	Title  string `json:"title"`
	State  string `json:"state"`
	Branch string `json:"branch"`
	Base   string `json:"base"`
	Author string `json:"author"`
	URL    string `json:"url"`
	Draft  bool   `json:"draft"`
	Merged bool   `json:"merged"`
	// MergedAt is the merge timestamp (RFC3339), populated only when Merged
	// is true; empty on an open/draft/closed-without-merge PR. Carried
	// (rather than reduced to the Merged bool alone) so the snapshot layer
	// can compute how long ago a PR merged — its dashboard-retention window
	// is measured from this instant, not from when the daemon noticed the
	// merge (pg2-ew4kf).
	MergedAt string `json:"merged_at,omitempty"`
	// CreatedAt/ClosedAt are the PR's own creation and close timestamps
	// (RFC3339). Only SearchPRsActivity fills them (the activity capability's
	// pr.opened and pr.closed kinds); every other path leaves them empty.
	CreatedAt    string `json:"created_at,omitempty"`
	ClosedAt     string `json:"closed_at,omitempty"`
	Additions    int    `json:"additions,omitempty"`
	Deletions    int    `json:"deletions,omitempty"`
	ChangedFiles int    `json:"changed_files,omitempty"`
	// HeadSHA is the OID of the PR's current head commit. Used to write
	// store.PullRequest.HeadSHA and to drive ReconcileStaleness.
	HeadSHA string `json:"head_sha,omitempty"`
	// BaseSHA is the OID of the commit the PR's base branch points at.
	// GetPR fills it from baseRefOid on the show path; SearchPRsEnriched (the
	// list path) never does, so a base-branch move cannot change list
	// summaries. Empty when baseRefOid is empty; never synthesized.
	BaseSHA string `json:"base_sha,omitempty"`
	// Body is the PR description text. Added for urgency keyword scanning;
	// fully populated in Task 8.
	Body string `json:"body,omitempty"`
	// Labels are the PR's label names. Used by enrichment's urgency signal.
	Labels []string `json:"labels,omitempty"`
	// RequestedReviewers are the login names of accounts (users, bots, mannequins)
	// requested to review the PR — teams, which have no login, are excluded.
	// Populated from `gh pr view --json reviewRequests`; the sync layer derives
	// ReviewRequestedOfMe from this against the configured SelfLogin.
	RequestedReviewers []string `json:"requested_reviewers,omitempty"`
	// Assignees are the login names of accounts assigned to the PR. Populated
	// from `gh pr view --json assignees` (REST path) and the GraphQL enrich
	// path's assignees connection. The sync layer derives an "assigned to me"
	// signal from this against the configured SelfLogin, mirroring how
	// ReviewRequestedOfMe is derived from RequestedReviewers.
	Assignees []string `json:"assignees,omitempty"`
	// ReviewRequestedOfMe is true when the configured self login is among
	// RequestedReviewers. Set by the sync layer (which knows self); consumed by the
	// dashboard's "PRs to Review" match reason (pg2-ynhr.13 B2).
	ReviewRequestedOfMe bool `json:"review_requested_of_me,omitempty"`
	// AssignedToMe is true when the configured self login is among Assignees.
	// Set by the sync layer (which knows self, via assignedToSelf) mirroring
	// how ReviewRequestedOfMe is derived; consumed by the dashboard's "PRs to
	// Review" match reason (pg2-4dz88.11.4).
	AssignedToMe bool `json:"assigned_to_me,omitempty"`
	// Mergeable is GitHub's merge-conflict signal: MERGEABLE | CONFLICTING |
	// UNKNOWN. Populated directly from `gh pr view --json mergeable`
	// (bead pg2-2j5ac.28.2 — this backend has no separate GraphQL enrich
	// path today; see internal/github/github.go's prListFields).
	Mergeable string `json:"mergeable,omitempty"`
	// MergeStateStatus is GitHub's authoritative merge-readiness: CLEAN |
	// BLOCKED | BEHIND | DIRTY | UNSTABLE | DRAFT | HAS_HOOKS | UNKNOWN. It
	// reflects branch protection (approvals, required checks, policy-bot) and is
	// the source of truth for "can I merge now" — distinct from the CI-health
	// rollup. Populated directly from `gh pr view --json mergeStateStatus`
	// (bead pg2-2j5ac.28.2, same as Mergeable above).
	MergeStateStatus string `json:"merge_state_status,omitempty"`
	// ChecksRollup folds the PR's head-commit check-run/status-context data
	// (`gh pr view --json statusCheckRollup`) into one of "success",
	// "failure", "pending", "none" — see internal/github/github.go's
	// checksRollupFromContexts for the fold rule. Added by bead
	// pg2-2j5ac.28.2 for schema.PR.ChecksRollup — distinct from
	// MergeStateStatus, which is GitHub's own branch-protection/merge-
	// readiness signal, not a CI-health rollup.
	ChecksRollup string `json:"checks_rollup,omitempty"`
	// Checks are the PR's head-commit check runs / status contexts that
	// ChecksRollup folds, each with its own classified Outcome. Filled by
	// GetPR only (gh pr view --json statusCheckRollup); search results leave
	// it nil. The per-check view (bead pg2-fnqqi) lets a consumer drop individual
	// checks, which the single folded ChecksRollup string cannot express.
	Checks []Check `json:"checks,omitempty"`
	// ReviewRequests are the PR's currently-requested reviewers, both
	// individual account logins AND team slugs — unlike RequestedReviewers
	// above (which deliberately drops teams for the "requested of me"
	// self-match use). Populated from the same `gh pr view --json
	// reviewRequests` response RequestedReviewers reads, just without the
	// team-dropping filter. Added by bead pg2-2j5ac.28.2 for
	// schema.PR.ReviewRequests ("logins and team slugs").
	ReviewRequests []string `json:"review_requests,omitempty"`
	// AutoMergeEnabled is true when GitHub auto-merge is armed on the PR.
	AutoMergeEnabled bool `json:"auto_merge_enabled,omitempty"`
	// StackID identifies the native GitHub stack this PR belongs to
	// (PullRequestStack.id, via PullRequestStackEntry.stack.id). Populated by
	// the GraphQL enrich path only when GitHub's native stacked-PR fields
	// (private preview) are present and non-null for this PR; empty when the
	// PR isn't stacked, the fields are null/absent from the response (older
	// schema, or the preview withdrawn), or on the REST fallback path.
	StackID string `json:"stack_id,omitempty"`
	// StackPosition is this PR's 1-based position within its native stack
	// (PullRequestStackEntry.position). Zero when not stacked.
	StackPosition int `json:"stack_position,omitempty"`
	// StackSize is the total number of PRs in this PR's native stack
	// (PullRequestStack.size). Zero when not stacked.
	StackSize int `json:"stack_size,omitempty"`
	// StackUpstreamHeadRefName is the head ref of the stack entry immediately
	// upstream of this PR (StackPosition-1) — the branch this PR is stacked
	// on. Empty when this PR is the bottommost stacked entry or not stacked.
	StackUpstreamHeadRefName string `json:"stack_upstream_head_ref_name,omitempty"`
	// StackDownstreamHeadRefName is the head ref of the stack entry
	// immediately downstream of this PR (StackPosition+1) — the next PR
	// stacked on top of this one. Empty when this PR is the topmost stacked
	// entry or not stacked.
	StackDownstreamHeadRefName string `json:"stack_downstream_head_ref_name,omitempty"`

	// UpdatedAt/CommentCount/ReviewCount (bead pg2-2j5ac.30.6) were first
	// added as raw fields for a fingerprint cursor that no longer exists;
	// they now feed schema.PR's summary fields (bead pg2-2j5ac.52.6.1).
	//
	// UpdatedAt is the PR's last-updated time. SearchPRsEnriched fills it
	// (GraphQL updatedAt) and so does GetPR (gh pr view --json updatedAt).
	UpdatedAt string `json:"updated_at,omitempty"`
	// CommentCount is the PR's count of top-level PR comments only, never
	// review-thread comments. SearchPRsEnriched fills it from
	// comments { totalCount }. GetPR leaves it at zero, because ghPR has no
	// comments field: the show path (provider.go's toSchemaPR) counts the
	// issue-endpoint comments it is handed instead.
	CommentCount int `json:"comment_count,omitempty"`
	// ReviewCount is the PR's review count. SearchPRsEnriched fills it from
	// reviews { totalCount } (bead pg2-2j5ac.52.6.1 added that selection) and
	// GetPR from the length of the reviews array already in prListFields:
	// the same reviews connection on both paths.
	ReviewCount int `json:"review_count,omitempty"`
	// NodeID is the PR object's own GraphQL node id (id in GraphQL, and in
	// gh pr view --json), the same mechanism this backend already uses for
	// comment and review node ids, applied one level up. It is the
	// rename-proof key for dedup and adoption. Filled from the batched
	// search on the list path and from gh pr view on the show path; never
	// derived from Repo and Number.
	NodeID string `json:"node_id,omitempty"`
	// ReviewDecision is GitHub's aggregate review verdict, carried verbatim:
	// APPROVED | CHANGES_REQUESTED | REVIEW_REQUIRED, or empty when GitHub
	// reports none (a PR with no review requirement).
	ReviewDecision string `json:"review_decision,omitempty"`
	// ReviewThreadCount is the PR's inline code-review comment thread count
	// (GraphQL's PullRequest.reviewThreads.totalCount — distinct from
	// CommentCount's issue-level comments) — originally the 8th and final
	// raw field a future GitHub fingerprint cursor needed (bead
	// pg2-2j5ac.30.7), via internal/github.Provider.ReviewThreadCount's own
	// dedicated raw-GraphQL call, mirroring pg-pr's own proven mechanism
	// for this exact field (packages/pg-pr/pkg/provider/vcs/github/
	// fingerprint.go). List stopped populating this field as of bead
	// pg2-aehpr (its FingerprintCursor consumer became vestigial), and no
	// other code path in this module ever populated it. Bead pg2-x3h8c.2
	// brings it back for the list path only: SearchPRsEnriched fills it from
	// the batched search's reviewThreads { totalCount }, a selection that adds
	// no points per page. The show path leaves it zero.
	ReviewThreadCount int `json:"review_thread_count,omitempty"`
	// LabelCount is the PR's total label count (bead pg2-x3h8c.2).
	// SearchPRsEnriched fills it from labels { totalCount }; Labels holds only
	// the first 20 names, so this is the figure that still moves past that.
	// GetPR fills it with the number of labels it decoded.
	LabelCount int `json:"label_count,omitempty"`
}

// Check outcomes (Check.Outcome), the same three-way split
// ChecksRollup's fold uses.
const (
	CheckFailure = "failure"
	CheckPending = "pending"
	CheckSuccess = "success"
)

// Check is one check run or legacy status context on a PR's head commit.
type Check struct {
	// Name is the check's job name (CheckRun.name) or status context name.
	Name string `json:"name,omitempty"`
	// Workflow is the workflow the check ran under (CheckRun.workflowName);
	// empty for a legacy status context.
	Workflow string `json:"workflow,omitempty"`
	// Outcome is CheckFailure, CheckPending or CheckSuccess.
	Outcome string `json:"outcome"`
}

// HasConflict reports whether GitHub signals a merge conflict on this PR, via
// either the mergeability enum (CONFLICTING) or the merge-state status (DIRTY).
// UNKNOWN (GitHub still computing) is deliberately NOT a conflict.
func (pr PR) HasConflict() bool {
	return pr.Mergeable == "CONFLICTING" || pr.MergeStateStatus == "DIRTY"
}

// File is one changed-file entry in a PR's diff, used only by the "files"
// targeted op (pkg/provider/pr.Provider.Files, bead pg2-2j5ac.28.2).
// internal/provider.go's toSchemaFiles maps it onto pkg/schema.PRFile at
// the Files boundary.
type File struct {
	Path      string `json:"path"`
	Additions int    `json:"additions,omitempty"`
	Deletions int    `json:"deletions,omitempty"`
}

// Commit is one commit on a PR's branch, used only by the "commits"
// targeted op (pkg/provider/pr.Provider.Commits, bead pg2-2j5ac.28.2).
// Author MUST be the commit's own GitHub author login — the co-owned-
// ownership classification's consumer — empty when GitHub has no linked
// user account for the commit's author identity. internal/provider.go's
// toSchemaCommits maps it onto pkg/schema.PRCommit at the Commits
// boundary.
type Commit struct {
	SHA     string `json:"sha"`
	Author  string `json:"author"`
	Message string `json:"message,omitempty"`
}
