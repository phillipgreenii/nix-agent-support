//go:build !darwin

package provision

import "fmt"

// cloneDir is unavailable off macOS; the Clone provisioner reports
// ErrCloneUnsupported and callers fall back to the Worktree provisioner.
func cloneDir(src, dst string) error {
	return fmt.Errorf("%w: only implemented on macOS", ErrCloneUnsupported)
}
