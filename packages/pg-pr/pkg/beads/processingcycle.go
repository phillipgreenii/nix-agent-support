// Package beads — processing-cycle bead wrappers (bd type=task).
package beads

import "context"

// CreateProcessingCycle creates a processing-cycle bead. Phase 0 stub.
func CreateProcessingCycle(_ context.Context, _, _ string) (id string, err error) {
	return "", ErrNotImplemented
}

// FindOpenProcessingCycle finds an open processing-cycle bead for a PR. Phase 0 stub.
func FindOpenProcessingCycle(_ context.Context, _ string) (id string, found bool, err error) {
	return "", false, ErrNotImplemented
}

// CloseProcessingCycle closes a processing-cycle bead. Phase 0 stub.
func CloseProcessingCycle(_ context.Context, _, _ string) error { return ErrNotImplemented }
