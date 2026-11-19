package hygiene

import (
	"github.com/phillipgreenii/pg-router/internal/handlers/procprobe"
)

// The live-process working-directory probe is shared with the dispatch-time
// worktree cleanup (internal/procprobe); the sweep keeps these local names.

// cwdProbe returns the working directory of every process the caller can see.
type cwdProbe = procprobe.Probe

// worktreeHeldByProcess reports whether any cwd in cwds is the worktree or lies
// inside it.
func worktreeHeldByProcess(cwds []string, c leakedWorktree) bool {
	return procprobe.Held(cwds, c.path)
}
