// Package provision drives the git side of a pool slot: creating the slot
// checkout, verifying it, resetting it in place for the next lease and
// detaching it on release. It sits on top of package pool (which owns the slot
// lock, lease records and state machine) and never touches the state itself
// except through QuarantineIfWedged.
package provision

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/phillipgreenii/pg-checkout-pool/internal/pool"
)

// Provisioner prepares the working tree of one slot. Implementations MUST be
// safe to call only while the caller holds the slot's exclusive lock.
type Provisioner interface {
	// Create makes a new checkout at dir at commit, detached, and runs the
	// repository's post-checkout hook once.
	Create(ctx context.Context, dir, commit string) error
	// Verify reports whether dir is still a healthy checkout of the
	// configured repository.
	Verify(ctx context.Context, dir string) error
	// Reset moves dir to commit (on branch when non-empty, else detached) and
	// removes every untracked and ignored file outside the keep list. It
	// returns the resulting HEAD commit.
	Reset(ctx context.Context, dir, commit, branch string) (string, error)
	// Detach leaves the lease branch and detaches HEAD, so the branch is
	// free to be checked out elsewhere.
	Detach(ctx context.Context, dir string) error
}

// Errors that mark a slot as unusable. A caller turns them into a quarantine
// with QuarantineIfWedged.
var (
	// ErrWedged: a git command exceeded its time bound and was killed.
	ErrWedged = errors.New("git command wedged (timed out)")
	// ErrStaleLock: a git lock file is present while the pool holds the slot.
	ErrStaleLock = errors.New("stale git lock file in slot")
	// ErrNotClean: files outside the keep list survived a reset.
	ErrNotClean = errors.New("slot tree not clean after reset")
	// ErrUnhealthy: Verify found the slot is not a checkout of the repository.
	ErrUnhealthy = errors.New("slot is not a healthy checkout")
)

// QuarantineIfWedged quarantines the slot behind h when err is one of the
// slot-unusable errors, recording err as the reason, and returns err
// unchanged (joined with any quarantine failure). Other errors pass through.
func QuarantineIfWedged(h *pool.Handle, err error) error {
	if err == nil {
		return nil
	}
	if !errors.Is(err, ErrWedged) && !errors.Is(err, ErrStaleLock) &&
		!errors.Is(err, ErrNotClean) && !errors.Is(err, ErrUnhealthy) {
		return err
	}
	if qerr := h.Quarantine(err.Error()); qerr != nil {
		return errors.Join(err, fmt.Errorf("quarantine: %w", qerr))
	}
	return err
}

// ValidateKeep checks a keep list: every entry a plain relative path inside
// the tree, with no pathspec magic or parent traversal. It returns the entries
// cleaned of any leading "./" or "/" and trailing "/".
func ValidateKeep(keep []string) ([]string, error) {
	out := make([]string, 0, len(keep))
	for _, k := range keep {
		c := strings.TrimRight(strings.TrimLeft(strings.TrimPrefix(k, "./"), "/"), "/")
		switch {
		case c == "":
			return nil, fmt.Errorf("keep list: empty entry %q", k)
		case strings.HasPrefix(c, "-") || strings.HasPrefix(c, ":") || strings.HasPrefix(c, "!"):
			return nil, fmt.Errorf("keep list: entry %q starts with a magic character", k)
		case strings.ContainsAny(c, "*?[\\\n"):
			return nil, fmt.Errorf("keep list: entry %q is not a plain path", k)
		}
		for _, seg := range strings.Split(c, "/") {
			if seg == ".." || seg == "." || seg == "" {
				return nil, fmt.Errorf("keep list: entry %q escapes or repeats a path segment", k)
			}
		}
		out = append(out, c)
	}
	return out, nil
}
