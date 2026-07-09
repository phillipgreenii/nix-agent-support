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
	// ReviewRequestedOfMe is true when the authenticated viewer is a requested
	// reviewer on the PR. Populated by the GitHub GraphQL enrich path (pg2-ynhr.13
	// B2); consumed by the dashboard's "PRs to Review" match reason. Defaults false
	// until B2 wires the GraphQL node.
	ReviewRequestedOfMe bool `json:"review_requested_of_me,omitempty"`
}
