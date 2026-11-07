//go:build darwin

package provision

import (
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

// cloneDir clones the directory tree src to dst with one clonefile(2) call.
func cloneDir(src, dst string) error {
	err := unix.Clonefile(src, dst, 0)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, unix.ENOTSUP), errors.Is(err, unix.ENOSYS), errors.Is(err, unix.EXDEV):
		return fmt.Errorf("%w: clonefile %s -> %s: %v", ErrCloneUnsupported, src, dst, err)
	}
	return fmt.Errorf("clonefile %s -> %s: %w", src, dst, err)
}
