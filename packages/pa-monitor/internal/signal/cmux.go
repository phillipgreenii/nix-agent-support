package signal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// surfaceCacheTTL is how long a cmux --json top --processes result stays fresh.
// Within one signalNonWorking pass we want a single enumeration to serve every
// per-pid Detect, but we also want fresh data between user-triggered nudges.
const surfaceCacheTTL = 2 * time.Second

// CmuxSignaler sends keys to the cmux surface hosting a process.
// RunCmd and LookupEnv are injectable for tests; nil values fall back to
// exec.CommandContext and os.LookupEnv respectively.
type CmuxSignaler struct {
	RunCmd    func(ctx context.Context, name string, args ...string) ([]byte, error)
	LookupEnv func(key string) (string, bool)

	cacheMu   sync.Mutex
	cacheAt   time.Time
	cacheLocs map[int]surfaceLoc
	cacheErr  error

	// Independent cache for cmux server PIDs (used by ancestry-based Detect).
	// Refreshed at the same surfaceCacheTTL cadence as cacheLocs but kept
	// separate so the two enumeration commands stay independent.
	serverCacheAt   time.Time
	serverCachePids []int
	serverCacheErr  error
}

// surfaceLoc identifies a cmux surface by its enclosing workspace and surface ref.
type surfaceLoc struct {
	workspaceRef string
	surfaceRef   string
}

// cmuxTopOutput models the subset of `cmux --json top --processes` fields used
// to map a pid to its hosting surface. Fields not consumed here are deliberately
// omitted; encoding/json ignores unknown keys, so any unrelated schema additions
// in cmux are non-breaking.
type cmuxTopOutput struct {
	Windows []struct {
		Workspaces []struct {
			Ref   string `json:"ref"`
			Panes []struct {
				Surfaces []struct {
					Ref            string `json:"ref"`
					Type           string `json:"type"`
					Tty            string `json:"tty"`
					TtyProcessPids []int  `json:"tty_process_pids"`
				} `json:"surfaces"`
			} `json:"panes"`
		} `json:"workspaces"`
	} `json:"windows"`
}

func (c *CmuxSignaler) Name() string { return "cmux" }

// RequiredBinaries reports the executables CmuxSignaler shells out to. Detect
// works via ps ancestry without it, but surface enumeration and Send call the
// `cmux` CLI — so a missing binary breaks cmux auto-resume delivery silently.
func (c *CmuxSignaler) RequiredBinaries() []string { return []string{"cmux"} }

func (c *CmuxSignaler) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if c.RunCmd != nil {
		return c.RunCmd(ctx, name, args...)
	}
	out, err := exec.CommandContext(ctx, name, args...).Output()
	return out, enrichCmdErr(err)
}

// enrichCmdErr augments an *exec.ExitError with the subprocess's captured
// stderr. exec.ExitError.Error() renders only "exit status N" and drops
// Stderr, so a failing `cmux --json top --processes` surfaced as a bare
// "exit status 1" with no clue why (see pg2-il6j: enumerate is the dominant
// nudge Send failure and its cause was undiagnosable from the error string).
// .Output() populates ExitError.Stderr, so we fold it into the message.
// Non-ExitError errors (context timeout, binary-not-found) pass through.
func enrichCmdErr(err error) error {
	var ee *exec.ExitError
	if errors.As(err, &ee) && len(ee.Stderr) > 0 {
		return fmt.Errorf("%w: %s", err, bytes.TrimSpace(ee.Stderr))
	}
	return err
}

func (c *CmuxSignaler) lookupEnv(key string) (string, bool) {
	if c.LookupEnv != nil {
		return c.LookupEnv(key)
	}
	return os.LookupEnv(key)
}

// Detect returns true when targetPID has a cmux server in its ancestry.
// Implemented via ps-A enumeration of cmux server PIDs + ancestry walk; works
// regardless of whether pa-monitor itself is running inside cmux.
//
// (Earlier behaviour gated on CMUX_WORKSPACE_ID being set in pa-monitor's
// own env, which broke detection whenever the daemon was started by a
// LaunchAgent. The ancestry approach mirrors TmuxSignaler.)
func (c *CmuxSignaler) Detect(pid int) bool {
	_, ok := c.FindCmuxServerAncestor(pid)
	return ok
}

// FindCmuxServerAncestor walks the process tree from targetPID upward,
// returning the PID of the first ancestor whose process name (comm) is
// "cmux". Returns (0, false) if no such ancestor exists. Used by the
// poller to enrich session TerminalHost with bridge-status information.
func (c *CmuxSignaler) FindCmuxServerAncestor(targetPID int) (int, bool) {
	servers, err := c.cachedCmuxServerPIDs()
	if err != nil || len(servers) == 0 {
		return 0, false
	}
	serverSet := make(map[int]bool, len(servers))
	for _, p := range servers {
		serverSet[p] = true
	}
	pid := targetPID
	seen := map[int]bool{}
	for {
		if pid < 1 || seen[pid] {
			return 0, false
		}
		seen[pid] = true
		if serverSet[pid] {
			return pid, true
		}
		ppid, err := c.parentPID(pid)
		if err != nil || ppid < 1 {
			return 0, false
		}
		pid = ppid
	}
}

// cachedCmuxServerPIDs returns the PIDs of running cmux server processes
// (those where `ps -A -o pid,comm` reports comm == "cmux"). Cached for
// surfaceCacheTTL so a sweep over N sessions runs ps once.
func (c *CmuxSignaler) cachedCmuxServerPIDs() ([]int, error) {
	c.cacheMu.Lock()
	defer c.cacheMu.Unlock()
	if !c.serverCacheAt.IsZero() && time.Since(c.serverCacheAt) < surfaceCacheTTL {
		return c.serverCachePids, c.serverCacheErr
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pids, err := c.enumerateCmuxServerPIDs(ctx)
	c.serverCachePids, c.serverCacheErr, c.serverCacheAt = pids, err, time.Now()
	return pids, err
}

// enumerateCmuxServerPIDs runs `ps -A -o pid,comm` and returns the PIDs
// whose comm column resolves to a "cmux" executable. On macOS the comm
// column may be either a bare basename ("cmux") or an absolute path
// into a .app bundle ("/nix/store/.../cmux.app/Contents/MacOS/cmux").
// We take the path's basename and compare so both shapes match.
func (c *CmuxSignaler) enumerateCmuxServerPIDs(ctx context.Context) ([]int, error) {
	out, err := c.run(ctx, "ps", "-A", "-o", "pid,comm")
	if err != nil {
		return nil, fmt.Errorf("ps -A: %w", err)
	}
	var pids []int
	for line := range strings.SplitSeq(string(out), "\n") {
		// Manual split: ps formats this as <padded-pid> <comm>; comm may
		// itself contain spaces if the executable's path has any (rare
		// but possible on macOS / NixOS). strings.Fields would shred those.
		trimmed := strings.TrimLeft(line, " \t")
		if trimmed == "" {
			continue
		}
		i := strings.IndexAny(trimmed, " \t")
		if i <= 0 {
			continue
		}
		pidStr, comm := trimmed[:i], strings.TrimSpace(trimmed[i+1:])
		pid, err := strconv.Atoi(pidStr)
		if err != nil {
			continue // header row or junk
		}
		base := comm
		if j := strings.LastIndexByte(comm, '/'); j >= 0 {
			base = comm[j+1:]
		}
		if base == "cmux" {
			pids = append(pids, pid)
		}
	}
	return pids, nil
}

// parentPID returns the parent PID of pid via `ps -o ppid= -p <pid>`.
// Returns 0 if pid has no parent (or the lookup fails). Used by ancestry
// walking; mirrors the same idiom in TmuxSignaler.findPaneLocForPID.
func (c *CmuxSignaler) parentPID(pid int) (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := c.run(ctx, "ps", "-o", "ppid=", "-p", strconv.Itoa(pid))
	if err != nil {
		return 0, err
	}
	s := strings.TrimSpace(string(out))
	if s == "" {
		return 0, nil
	}
	ppid, err := strconv.Atoi(s)
	if err != nil {
		return 0, err
	}
	return ppid, nil
}

// cachedSurfaces returns the surface map, caching for surfaceCacheTTL so that a
// single signalNonWorking pass over N sessions runs the enumeration once, not N
// times. Both Detect and Send go through this helper. Errors are cached for the
// same window so a transient cmux failure doesn't fan out into N error reports.
func (c *CmuxSignaler) cachedSurfaces() (map[int]surfaceLoc, error) {
	c.cacheMu.Lock()
	defer c.cacheMu.Unlock()
	if c.cacheAt != (time.Time{}) && time.Since(c.cacheAt) < surfaceCacheTTL {
		return c.cacheLocs, c.cacheErr
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	locs, err := c.enumerateSurfaces(ctx)
	c.cacheLocs, c.cacheErr, c.cacheAt = locs, err, time.Now()
	return locs, err
}

// Send injects text followed by Enter into the cmux surface hosting pid.
//
// Steps:
//  1. Reuse the cached surface enumeration (or refresh it).
//  2. Look up pid in the surface map; if the agent pid isn't directly in any
//     surface's tty_process_pids (common when the agent is a child of a shell
//     under the cmux pane), walk ancestors until a match is found. Mirrors
//     the Detect-via-ancestry behavior in FindCmuxServerAncestor — otherwise
//     Detect would return true while Send returned "no cmux surface found".
//  3. cmux send + cmux send-key enter against the matched workspace+surface.
func (c *CmuxSignaler) Send(pid int, text string) error {
	locs, err := c.cachedSurfaces()
	if err != nil {
		return fmt.Errorf("cmux enumerate: %w", err)
	}
	loc, ok := c.findSurfaceForPID(locs, pid)
	if !ok {
		return fmt.Errorf("signal: no cmux surface found for pid %d", pid)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := c.run(ctx, "cmux", "send", "--workspace", loc.workspaceRef, "--surface", loc.surfaceRef, text); err != nil {
		return fmt.Errorf("cmux send: %w", err)
	}
	if _, err := c.run(ctx, "cmux", "send-key", "--workspace", loc.workspaceRef, "--surface", loc.surfaceRef, "enter"); err != nil {
		return fmt.Errorf("cmux send-key: %w", err)
	}
	return nil
}

// findSurfaceForPID returns the surfaceLoc whose tty_process_pids contains
// pid or one of pid's ancestors. Walks parents until the cmux server PID is
// hit or no further parent is available, mirroring the ancestry approach
// used by Detect. Returns (zero, false) if no surface matches.
func (c *CmuxSignaler) findSurfaceForPID(locs map[int]surfaceLoc, pid int) (surfaceLoc, bool) {
	seen := map[int]bool{}
	for {
		if pid < 1 || seen[pid] {
			return surfaceLoc{}, false
		}
		seen[pid] = true
		if loc, ok := locs[pid]; ok {
			return loc, true
		}
		ppid, err := c.parentPID(pid)
		if err != nil || ppid < 1 || ppid == pid {
			return surfaceLoc{}, false
		}
		pid = ppid
	}
}

// enumerateSurfaces returns a flat map keyed by every pid in any surface's
// tty_process_pids, so a target pid resolves directly to the surface that
// hosts it. Non-terminal surfaces and surfaces without a tty are skipped.
func (c *CmuxSignaler) enumerateSurfaces(ctx context.Context) (map[int]surfaceLoc, error) {
	out, err := c.run(ctx, "cmux", "--json", "top", "--processes")
	if err != nil {
		return nil, fmt.Errorf("cmux --json top --processes: %w", err)
	}
	var parsed cmuxTopOutput
	if err := json.Unmarshal(out, &parsed); err != nil {
		return nil, fmt.Errorf("parse cmux top: %w", err)
	}
	result := map[int]surfaceLoc{}
	for _, w := range parsed.Windows {
		for _, ws := range w.Workspaces {
			for _, p := range ws.Panes {
				for _, s := range p.Surfaces {
					if s.Type != "terminal" || s.Tty == "" {
						continue
					}
					for _, pid := range s.TtyProcessPids {
						result[pid] = surfaceLoc{workspaceRef: ws.Ref, surfaceRef: s.Ref}
					}
				}
			}
		}
	}
	return result, nil
}
