package hygiene

import (
	"time"

	"github.com/phillipgreenii/pg-router/internal/handlers/config"
	"github.com/phillipgreenii/pg-router/internal/handlers/gwm"
	"github.com/phillipgreenii/pg-router/internal/handlers/worktree"
)

// teardownPorts are the external ports and tunables the closed-bead teardown
// and the bulk-delete passes use. The zero value selects production behavior
// for every field, so a Sweeper that sets nothing tears down exactly as before;
// a test substitutes one field on its own copy.
type teardownPorts struct {
	// gwm is the pg-gwm client the teardown ends leases through; nil means the
	// production CLI client at the repo root.
	gwm gwm.Client
	// removeTimeout is the deadline of one worktree removal in the teardown; zero
	// means worktree.DefaultRemoveTimeout. withRemoveTimeout adopts it from the
	// launch config.
	removeTimeout time.Duration
	// removal is the rate limit every bulk delete uses (a worktree or leftover
	// tree is tens of thousands of files, and each unlink is a filesystem event
	// for every listener); nil means the shared limiter.
	removal *worktree.Removal
}

// withRemoveTimeout returns tp with cfg's worktree removal deadline.
func (tp teardownPorts) withRemoveTimeout(cfg config.Config) teardownPorts {
	tp.removeTimeout = cfg.WorktreeRemoveTimeout
	return tp
}

// pacedRemoval returns the rate limit for bulk deletes.
func (tp teardownPorts) pacedRemoval() worktree.Removal {
	if tp.removal != nil {
		return *tp.removal
	}
	return worktree.Removal{Limiter: worktree.SharedLimiter}
}
