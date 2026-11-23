package client

// PRShowResult is the subset of a `pr show` answer that a caller deciding
// whether a pull request is finished needs: its state and merged flag.
type PRShowResult struct {
	State  string `json:"state"`
	Merged bool   `json:"merged"`
}

// PendingReviewRecord is the subset of a pending review record that a caller
// needs to judge it: whether it has gone stale against its pull request.
type PendingReviewRecord struct {
	Stale bool `json:"stale"`
}

// PendingReviewResult is a `pr review pending` answer: whether a pending
// review exists and, when it does, its record. Review is nil when absent.
type PendingReviewResult struct {
	Pending bool                 `json:"pending"`
	Review  *PendingReviewRecord `json:"review"`
}
