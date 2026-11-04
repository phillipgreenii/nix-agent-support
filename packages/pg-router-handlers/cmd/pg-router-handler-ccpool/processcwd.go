package main

import (
	"github.com/phillipgreenii/pg-router-handlers/internal/procprobe"
)

// The live-process working-directory probe is shared with the dispatch-time
// worktree cleanup (internal/procprobe); the sweep keeps these local names.

// cwdProbe returns the working directory of every process the caller can see.
type cwdProbe = procprobe.Probe

// processCWDs is the production cwdProbe: `lsof -d cwd` over all processes.
var processCWDs = procprobe.CWDs

// parseLsofCWDs extracts the path of every `n<path>` field line from `lsof -Fn`.
var parseLsofCWDs = procprobe.Parse

// worktreeHeldByProcess reports whether any cwd in cwds is the worktree or lies
// inside it.
func worktreeHeldByProcess(cwds []string, c leakedWorktree) bool {
	return procprobe.Held(cwds, c.path)
}
