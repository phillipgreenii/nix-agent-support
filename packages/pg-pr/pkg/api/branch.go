package api

// BranchInfo describes the current branch / PR context for a given cwd.
//
// It is returned by `pg-pr branch detect`. PRNumber is nil when the current
// branch has no associated open PR (or when the `gh` CLI is unavailable).
type BranchInfo struct {
	Repo         string `json:"repo"`
	Branch       string `json:"branch"`
	Base         string `json:"base"`
	WorktreeRoot string `json:"worktree_root"`
	PRNumber     *int   `json:"pr_id"`
}
