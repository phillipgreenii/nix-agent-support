package api

// PR is the JSON shape returned by `pg-pr pr show`.
type PR struct {
	Repo         string `json:"repo"`
	Number       int    `json:"number"`
	Title        string `json:"title"`
	State        string `json:"state"`
	Branch       string `json:"branch"`
	Base         string `json:"base"`
	Author       string `json:"author"`
	URL          string `json:"url"`
	Draft        bool   `json:"draft"`
	Merged       bool   `json:"merged"`
	Additions    int    `json:"additions,omitempty"`
	Deletions    int    `json:"deletions,omitempty"`
	ChangedFiles int    `json:"changed_files,omitempty"`
	// HeadSHA is the OID of the PR's current head commit. Used to write
	// store.PullRequest.HeadSHA and to drive ReconcileStaleness.
	HeadSHA string `json:"head_sha,omitempty"`
	// BaseSHA is the OID of the PR's base commit. Populated by the GitHub GraphQL
	// path; empty on the REST fallback path. The revision row stores it.
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
	// ReviewRequestedOfMe is true when the configured self login is among
	// RequestedReviewers. Set by the sync layer (which knows self); consumed by the
	// dashboard's "PRs to Review" match reason (pg2-ynhr.13 B2).
	ReviewRequestedOfMe bool `json:"review_requested_of_me,omitempty"`
	// Mergeable is GitHub's merge-conflict signal: MERGEABLE | CONFLICTING |
	// UNKNOWN. Populated by the GraphQL enrich path; empty on REST fallback.
	Mergeable string `json:"mergeable,omitempty"`
	// MergeStateStatus is GitHub's authoritative merge-readiness: CLEAN |
	// BLOCKED | BEHIND | DIRTY | UNSTABLE | DRAFT | HAS_HOOKS | UNKNOWN. It
	// reflects branch protection (approvals, required checks, policy-bot) and is
	// the source of truth for "can I merge now" — distinct from the CI-health
	// rollup. Empty on REST fallback.
	MergeStateStatus string `json:"merge_state_status,omitempty"`
	// AutoMergeEnabled is true when GitHub auto-merge is armed on the PR.
	AutoMergeEnabled bool `json:"auto_merge_enabled,omitempty"`
}
