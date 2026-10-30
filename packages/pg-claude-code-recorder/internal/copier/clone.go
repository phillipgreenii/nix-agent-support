package copier

import (
	"errors"
	"syscall"

	"golang.org/x/sys/unix"
)

// ClonePolicy is the clone seam: the probe result, the clone call and the
// private-size query. The unit tier fakes it to cover clone mode, byte mode and
// EXDEV without a special volume.
type ClonePolicy interface {
	// Probe reports whether clone mode applies: the state directory is on the
	// same volume as the Claude directory and the volume reports the clone
	// capability. reason says why not.
	Probe(stateDir, claudeDir string) (ok bool, reason string)
	// Clone clones src to the not-yet-existing dst without following symbolic
	// links.
	Clone(src, dst string) error
	// PrivateSize is the space a clone holds that is not shared with another file.
	PrivateSize(path string) (int64, error)
}

// byteModeErr reports whether err selects byte mode (ENOTSUP and EXDEV).
func byteModeErr(err error) (string, bool) {
	switch {
	case errors.Is(err, unix.EXDEV):
		return "EXDEV", true
	case errors.Is(err, unix.ENOTSUP), errors.Is(err, syscall.EOPNOTSUPP):
		return "ENOTSUP", true
	}
	return "", false
}

func sameDevice(a, b string) bool {
	var sa, sb unix.Stat_t
	if unix.Stat(a, &sa) != nil || unix.Stat(b, &sb) != nil {
		return false
	}
	return uint64(sa.Dev) == uint64(sb.Dev)
}
