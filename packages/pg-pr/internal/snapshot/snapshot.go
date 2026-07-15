// Package snapshot defines the JSON-serializable per-PR dashboard
// snapshot served by the pg-pr daemon's /api/v1/dashboard endpoint.
package snapshot

import "time"

// Snapshot is the top-level dashboard payload.
type Snapshot struct {
	GeneratedAt         time.Time `json:"generated_at"`
	SyncIntervalSeconds int       `json:"sync_interval_seconds"`
	Mine                []MineRow `json:"mine"`
	Team                []TeamRow `json:"team"`
}

// MineRow is one row in the "My PRs" table.
type MineRow struct {
	Repo          string `json:"repo"`
	Number        int    `json:"number"`
	Title         string `json:"title"`
	URL           string `json:"url"`
	Draft         bool   `json:"draft"`
	CIStatus      string `json:"ci_status"`
	HumanApproved bool   `json:"human_approved"`
	AgentApproved bool   `json:"agent_approved"`
	WaitingOnMe   bool   `json:"waiting_on_me"`
	// MergeStateStatus is GitHub's authoritative merge-readiness (CLEAN/BLOCKED/
	// …); the mine panel shows it separately from CIStatus. (pg2-dwfld)
	MergeStateStatus string `json:"merge_state_status,omitempty"`
	// AutoMergeEnabled is true when GitHub auto-merge is armed.
	AutoMergeEnabled bool `json:"auto_merge_enabled"`
	// NeedsMergeReminder is true for MY PR that is ready to merge (CLEAN) but has
	// no auto-merge armed — the "you forgot to merge / arm automerge" nudge. (pg2-dwfld)
	NeedsMergeReminder bool       `json:"needs_merge_reminder"`
	JIRA               []JIRAItem `json:"jira"`
	Beads              []BeadItem `json:"beads"`
}

// TeamRow is one row in the "PRs to Review" table (the not-mine review set:
// team-authored ∪ review-requested-of-me ∪ watch-labeled). The JSON key stays
// "team" for consumer compatibility (the external Grafana panel queries .team).
type TeamRow struct {
	Repo          string     `json:"repo"`
	Number        int        `json:"number"`
	Title         string     `json:"title"`
	Owner         string     `json:"owner"`
	URL           string     `json:"url"`
	CIStatus      string     `json:"ci_status"`
	HumanApproved bool       `json:"human_approved"`
	AgentApproved bool       `json:"agent_approved"`
	LinesChanged  int        `json:"lines_changed"`
	FilesChanged  int        `json:"files_changed"`
	JIRA          []JIRAItem `json:"jira"`
	// NeedsAttention flags a teammate PR that currently needs my review, derived
	// from the shared needsAttention predicate over persisted store facts. Stays
	// consistent with the open-attention-bead set (same predicate, same inputs).
	NeedsAttention bool `json:"needs_attention"`
	// AttentionReason is the (stable) reason string when NeedsAttention is true;
	// empty otherwise. See snapshot.AttentionReason* constants.
	AttentionReason string `json:"attention_reason,omitempty"`
	// MatchReason explains WHY this PR is in the review set: any of
	// MatchReasonTeamAuthored, MatchReasonReviewRequested, and one
	// MatchReasonLabelPrefix+<label> per matched watch label. May be empty for a
	// PR the ingest surfaced but none of the reasons currently identify (e.g. a
	// review-requested PR before the B2 GraphQL node populates ReviewRequestedOfMe).
	MatchReason []string `json:"match_reason,omitempty"`
}

// JIRAItem is one resolved JIRA issue referenced by a PR.
type JIRAItem struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	State string `json:"state"`
	URL   string `json:"url"`
}

// BeadItem is one bead from the recursive dep tree of a merge-request bead.
type BeadItem struct {
	ID     string   `json:"id"`
	Title  string   `json:"title"`
	Status string   `json:"status"`
	Labels []string `json:"labels"`
	URL    string   `json:"url"`
}
