// Package provision drives the git side of a managed worktree's life:
// creating it, switching it to another branch, cleaning it, finding unsaved
// work, preserving that work on a branch, and removing it. It never touches
// the manager's state; callers hold the repository lock and the worktree's
// operation hold around every call (INV-GWM-31).
package provision

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// BranchSpec says what a worktree checks out. Commit is already resolved.
// Create corresponds to -b (a new branch), Reset to -B (a new or moved
// branch), Detach to --detach. With none of the three, Branch is an existing
// branch.
type BranchSpec struct {
	Branch string
	Create bool
	Reset  bool
	Detach bool
	Commit string
}

// Unsaved is work in a worktree that a removal or reuse would lose.
type Unsaved struct {
	// Files are the paths with tracked changes or untracked files; ignored
	// files are not listed.
	Files []string
	// Unreachable are commits reachable from HEAD that no branch or tag
	// reaches.
	Unreachable []string
	// Blocker is non-empty for a merge, rebase or cherry-pick in progress,
	// or a submodule change; it names the reason.
	Blocker string
}

// Any reports whether anything would be lost: files, unreachable commits, or
// an operation in progress. An ignored-only tree reports false.
func (u Unsaved) Any() bool {
	return len(u.Files) > 0 || len(u.Unreachable) > 0 || u.Blocker != ""
}

// Provisioner is the host side of a managed worktree (INTF-GWM-HOST).
type Provisioner interface {
	// Create runs git worktree add --lock --reason with the branch flags;
	// the post-checkout hook runs.
	Create(ctx context.Context, dir string, b BranchSpec, lockReason string) error
	// Switch runs git switch (or --detach). With runHook false it passes
	// -c core.hooksPath=/dev/null to that command only (INV-GWM-25).
	Switch(ctx context.Context, dir string, b BranchSpec, runHook bool) error
	// Clean resets tracked files and removes every untracked and ignored
	// file outside keep, then proves the tree holds only keep paths or
	// returns ErrNotClean.
	Clean(ctx context.Context, dir string, keep []string) error
	// Unsaved reports the work a removal or reuse would lose.
	Unsaved(ctx context.Context, dir string) (Unsaved, error)
	// Preserve commits every change (never ignored files) to a new branch
	// created at the current head. message is the commit body.
	Preserve(ctx context.Context, dir, branch, message string) error
	// Unlock removes the worktree's lock.
	Unlock(ctx context.Context, dir string) error
	// Remove deletes the worktree; it refuses while the worktree is locked.
	Remove(ctx context.Context, dir string) error
	// Prune forgets worktrees whose directory is gone.
	Prune(ctx context.Context) error
	// StopFSMonitor stops the worktree's file-system monitor daemon; a daemon
	// that is not running is success.
	StopFSMonitor(ctx context.Context, dir string) error
	// RemoveStaleIndexLock deletes a zero-byte index.lock older than
	// olderThan and reports whether it did. The manager calls it before
	// every Switch and Clean while it holds the operation hold.
	RemoveStaleIndexLock(dir string, olderThan time.Duration) (bool, error)
}

var (
	// ErrNotClean: files outside the keep list survived a clean.
	ErrNotClean = errors.New("worktree not clean after clean")
	// ErrPreserveBlocked: a merge, rebase, cherry-pick or submodule change is
	// in progress, so committing the work would corrupt it.
	ErrPreserveBlocked = errors.New("preserve blocked by an operation in progress")
)

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
