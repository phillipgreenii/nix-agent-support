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
	Repo          string     `json:"repo"`
	Number        int        `json:"number"`
	Title         string     `json:"title"`
	URL           string     `json:"url"`
	Draft         bool       `json:"draft"`
	CIStatus      string     `json:"ci_status"`
	HumanApproved bool       `json:"human_approved"`
	AgentApproved bool       `json:"agent_approved"`
	WaitingOnMe   bool       `json:"waiting_on_me"`
	JIRA          []JIRAItem `json:"jira"`
	Beads         []BeadItem `json:"beads"`
}

// TeamRow is one row in the "Team PRs" table.
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
