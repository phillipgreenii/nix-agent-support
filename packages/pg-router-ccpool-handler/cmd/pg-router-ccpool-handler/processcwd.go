package main

import (
	"bytes"
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Live-process working directories (bead pg2-e5yw3).
//
// A per-bead worktree is shared by every role's session for the bead, but the
// worktree sweep only sees the dispatching role's own ccpool pool, and the
// per-bead flock dies with the handler that held it. A session of ANOTHER role,
// spared at a daemon restart (so its handler is gone), is therefore invisible to
// every row- and lock-based guard even though claude is still running in the
// worktree. The one signal that does not depend on a pool, a handler or a lease
// is the kernel's own: some live process has the worktree as its working
// directory. processCWDs reads that signal for the sweep.

// cwdProbeTimeout bounds one lsof invocation: it walks every process, which is
// normally well under a second but must never wedge a dispatch.
const cwdProbeTimeout = 30 * time.Second

// cwdProbe returns the working directory of every process the caller can see.
type cwdProbe func(ctx context.Context) ([]string, error)

// processCWDs is the production cwdProbe: `lsof -d cwd` over all processes.
// lsof exits non-zero when it cannot inspect some process (another user's, a
// zombie) while still printing the rest, so a non-empty listing is trusted
// whatever the exit status; only an empty listing WITH an error is a failure,
// and the sweep treats a failed probe as "cannot prove the worktree is unused".
func processCWDs(ctx context.Context) ([]string, error) {
	tctx, cancel := context.WithTimeout(ctx, cwdProbeTimeout)
	defer cancel()
	cmd := exec.CommandContext(tctx, "lsof", "-n", "-P", "-w", "-d", "cwd", "-Fn")
	var so bytes.Buffer
	cmd.Stdout = &so
	err := cmd.Run()
	cwds := parseLsofCWDs(so.Bytes())
	if len(cwds) == 0 && err != nil {
		return nil, err
	}
	return cwds, nil
}

// parseLsofCWDs extracts the path of every `n<path>` field line from `lsof -Fn`.
func parseLsofCWDs(out []byte) []string {
	var cwds []string
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "n") && len(line) > 1 {
			cwds = append(cwds, line[1:])
		}
	}
	return cwds
}

// worktreeHeldByProcess reports whether any cwd in cwds is the worktree or lies
// inside it. Both the raw and the symlink-resolved forms of the worktree path are
// compared, since lsof reports the resolved path while a pool row (and
// WorktreeDir) may spell it through a symlink.
func worktreeHeldByProcess(cwds []string, c leakedWorktree) bool {
	roots := []string{filepath.Clean(c.path)}
	if r, err := filepath.EvalSymlinks(c.path); err == nil && filepath.Clean(r) != roots[0] {
		roots = append(roots, filepath.Clean(r))
	}
	for _, cwd := range cwds {
		cwd = filepath.Clean(cwd)
		for _, root := range roots {
			if cwd == root || strings.HasPrefix(cwd, root+string(filepath.Separator)) {
				return true
			}
		}
	}
	return false
}
