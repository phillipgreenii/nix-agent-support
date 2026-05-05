package prreview

// SetupResult contains the result of the setup command.
type SetupResult struct {
	PRNumber     int    `json:"pr_number"`
	Title        string `json:"title"`
	HeadBranch   string `json:"head_branch"`
	BaseBranch   string `json:"base_branch"`
	URL          string `json:"url"`
	CommitSHA    string `json:"commit_sha"`
	WorktreePath string `json:"worktree_path"`
}

// PostResult contains the result of the post command.
type PostResult struct {
	CommentsPosted    int    `json:"comments_posted"`
	DuplicatesSkipped int    `json:"duplicates_skipped"`
	PRLevelComments   int    `json:"pr_level_comments"`
	Mode              string `json:"mode"` // "created_new", "appended_to_existing", "none"
}

// CleanupResult contains the result of the cleanup command.
type CleanupResult struct {
	Status string `json:"status"` // "ok", "not_found"
}

// ReviewComment represents a comment from the review subagent.
type ReviewComment struct {
	Path     *string `json:"path"`     // nil for PR-level comments
	Lines    []int   `json:"lines"`    // nil for file-level, [n] for single-line, [start, end] for multi-line
	Message  string  `json:"message"`
	Severity string  `json:"severity"` // "error", "warning", "suggestion"
}

// ReviewOutput represents the JSON output from the review subagent.
type ReviewOutput struct {
	Comments []ReviewComment `json:"comments"`
}

// GitHubComment represents a comment formatted for the GitHub API.
type GitHubComment struct {
	Path        string `json:"path,omitempty"`
	Line        *int   `json:"line,omitempty"`
	StartLine   *int   `json:"start_line,omitempty"`
	Side        string `json:"side,omitempty"`
	SubjectType string `json:"subject_type,omitempty"`
	Body        string `json:"body"`
}

// PRInfo contains information about a pull request from GitHub.
type PRInfo struct {
	Number      int    `json:"number"`
	Title       string `json:"title"`
	HeadRefName string `json:"headRefName"`
	BaseRefName string `json:"baseRefName"`
	URL         string `json:"url"`
	State       string `json:"state"`
	HeadRefOid  string `json:"headRefOid"`
}

// ExistingReview represents a pending review from the GitHub API.
type ExistingReview struct {
	ID    int    `json:"id"`
	State string `json:"state"`
	Body  string `json:"body"`
}

// ExistingComment represents an existing comment from a pending review.
type ExistingComment struct {
	Path      string `json:"path"`
	Line      *int   `json:"line"`
	StartLine *int   `json:"start_line"`
	Body      string `json:"body"`
}

// FileInfo contains information about a changed file.
type FileInfo struct {
	Path      string `json:"path"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
}

// FilesResult contains the result of the files command.
type FilesResult struct {
	Files []FileInfo `json:"files"`
}

// CommitInfo contains information about a commit.
type CommitInfo struct {
	SHA     string `json:"sha"`
	Subject string `json:"subject"`
	Body    string `json:"body"`
	Author  string `json:"author"`
}

// CommitsResult contains the result of the commits command.
type CommitsResult struct {
	Commits []CommitInfo `json:"commits"`
}

// CheckInfo contains information about a PR check.
type CheckInfo struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
}

// PRInfoResult contains the result of the pr-info command.
type PRInfoResult struct {
	Description string      `json:"description"`
	Labels      []string    `json:"labels"`
	Reviewers   []string    `json:"reviewers"`
	Checks      []CheckInfo `json:"checks"`
}
