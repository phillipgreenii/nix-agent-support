// Package beads — merge-request bead wrappers.
package beads

import "context"

// CreateMergeRequestInput is the typed input for creating a merge-request bead.
type CreateMergeRequestInput struct {
	Repo   string
	Number int
	State  string
	Branch string
	Base   string
	Author string
	URL    string
}

// CreateMergeRequest creates a merge-request bead. Phase 0 stub.
func CreateMergeRequest(_ context.Context, _ CreateMergeRequestInput) (id string, err error) {
	return "", ErrNotImplemented
}

// CloseMergeRequest closes a merge-request bead. Phase 0 stub.
func CloseMergeRequest(_ context.Context, _, _ string) error { return ErrNotImplemented }
