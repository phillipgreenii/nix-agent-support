// Package procprobe answers one question for every worktree reclaim path: does
// a live process anywhere on the machine have a directory as, or inside, its
// working directory?
//
// A per-bead worktree is shared by every role's session for the bead, but each
// reclaim only sees its own role's pool, and a per-bead lock dies with the
// handler that held it. The one signal that depends on no pool, handler or
// lease is the kernel's own: some live process has the directory as its working
// directory. The probe is injectable so callers are tested without lsof, and a
// failed probe is never read as "unused": every caller keeps the directory.
package procprobe

import (
	"bytes"
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Timeout bounds one lsof invocation: it walks every process, which is normally
// well under a second but must never wedge a dispatch.
const Timeout = 30 * time.Second

// Probe returns the working directory of every process the caller can see.
type Probe func(ctx context.Context) ([]string, error)

// Freshness rule: a probe that gates a removal (the sweep's under-lock probe,
// the admission leaked-worktree reclaim, the leftover-directory reclaim, the
// purge_pending retry and the dispatch worktree cleanup) MUST run fresh and
// MUST NOT be served from a cache: a process can enter a directory at any
// moment, and a stale listing would remove a directory that is now in use.
// The floor is therefore 1 + N probes for N removable candidates in one sweep
// (one pass pre-filter probe shared by all candidates, then one fresh probe per
// candidate under its lock). Only that pre-filter probe is shared, and it is
// already one probe per pass. Do not add a cache here.
//
// CWDs is the production Probe: `lsof -d cwd` over all processes. lsof exits
// non-zero when it cannot inspect some process (another user's, a zombie) while
// still printing the rest, so a non-empty listing is trusted whatever the exit
// status; only an empty listing WITH an error is a failure.
func CWDs(ctx context.Context) ([]string, error) {
	tctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	cmd := exec.CommandContext(tctx, "lsof", "-n", "-P", "-w", "-d", "cwd", "-Fn")
	var so bytes.Buffer
	cmd.Stdout = &so
	err := cmd.Run()
	cwds := Parse(so.Bytes())
	if len(cwds) == 0 && err != nil {
		return nil, err
	}
	return cwds, nil
}

// Parse extracts the path of every `n<path>` field line from `lsof -Fn`.
func Parse(out []byte) []string {
	var cwds []string
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "n") && len(line) > 1 {
			cwds = append(cwds, line[1:])
		}
	}
	return cwds
}

// Held reports whether any cwd in cwds is dir or lies inside it. Both the raw
// and the symlink-resolved forms of dir are compared, since lsof reports the
// resolved path while a pool row (and the worktree directory setting) may spell
// it through a symlink.
func Held(cwds []string, dir string) bool {
	roots := []string{filepath.Clean(dir)}
	if r, err := filepath.EvalSymlinks(dir); err == nil && filepath.Clean(r) != roots[0] {
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

// Keep runs probe (nil means CWDs) and reports whether dir MUST be kept
// because a process holds it or the listing cannot be read. err is non-nil only
// for the unreadable-listing case, so a caller can log it.
func Keep(ctx context.Context, probe Probe, dir string) (keep bool, err error) {
	if probe == nil {
		probe = CWDs
	}
	cwds, err := probe(ctx)
	if err != nil {
		return true, err
	}
	return Held(cwds, dir), nil
}
