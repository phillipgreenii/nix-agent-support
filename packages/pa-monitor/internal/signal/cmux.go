package signal

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// serverPIDCacheTTL is how long the cmux server-PID list (`ps -A -o pid,comm`)
// stays fresh. Server PIDs only change when the cmux app starts or stops, so
// the whole-table ps is the expensive, security-logged (process discovery) call
// this cache exists to ration. Worst-case staleness is this TTL plus up to one daemon tick (5 s).
const serverPIDCacheTTL = 10 * time.Second

// CmuxSignaler detects whether a process runs under a cmux server.
// RunCmd and LookupEnv are injectable for tests; nil values fall back to
// exec.CommandContext and os.LookupEnv respectively.
type CmuxSignaler struct {
	RunCmd    func(ctx context.Context, name string, args ...string) ([]byte, error)
	LookupEnv func(key string) (string, bool)

	// Procs, when set, answers every ancestry hop from the shared per-tick
	// process snapshot instead of one `ps -o ppid=` exec per hop.
	Procs ProcessTable

	cacheMu sync.Mutex

	// Cache for cmux server PIDs (used by ancestry-based Detect), refreshed on
	// the serverPIDCacheTTL cadence.
	serverCacheAt   time.Time
	serverCachePids []int
	serverCacheErr  error

	// now is the clock the TTL checks read; nil means time.Now. Tests inject
	// a controllable clock so they can cross a TTL without sleeping.
	now func() time.Time
}

func (c *CmuxSignaler) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

func (c *CmuxSignaler) Name() string { return "cmux" }

// RequiredBinaries reports the executables the cmux integration needs. Detect
// itself works via ps ancestry without it; the `cmux` CLI is run by the cmux
// status reporter (through RunCmux).
func (c *CmuxSignaler) RequiredBinaries() []string { return []string{"cmux"} }

func (c *CmuxSignaler) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if c.RunCmd != nil {
		return c.RunCmd(ctx, name, args...)
	}
	return RunCmux(ctx, name, args...)
}

// RunCmux is the single cmux-CLI exec seam: it runs the subprocess, returns its
// stdout, and augments a failure with the captured stderr (enrichCmdErr) so a
// failing `cmux` surfaces its cause instead of a bare "exit status N". Both the
// CmuxSignaler's default subprocess calls and the cmuxstatus reporter route
// through it, so neither duplicates the exec nor drops the stderr enrichment.
func RunCmux(ctx context.Context, name string, args ...string) ([]byte, error) {
	out, err := exec.CommandContext(ctx, name, args...).Output()
	return out, enrichCmdErr(err)
}

// enrichCmdErr augments an *exec.ExitError with the subprocess's captured
// stderr. exec.ExitError.Error() renders only "exit status N" and drops
// Stderr, so a failing `cmux` call surfaced as a bare "exit status 1" with no
// clue why.
// .Output() populates ExitError.Stderr, so we fold it into the message.
// Non-ExitError errors (context timeout, binary-not-found) pass through.
func enrichCmdErr(err error) error {
	var ee *exec.ExitError
	if errors.As(err, &ee) && len(ee.Stderr) > 0 {
		return fmt.Errorf("%w: %s", err, bytes.TrimSpace(ee.Stderr))
	}
	return err
}

// Detect returns true when targetPID has a cmux server in its ancestry.
// Implemented via ps-A enumeration of cmux server PIDs + ancestry walk; works
// regardless of whether pa-monitor itself is running inside cmux.
//
// (Earlier behaviour gated on CMUX_WORKSPACE_ID being set in pa-monitor's
// own env, which broke detection whenever the daemon was started by a
// LaunchAgent. The ancestry approach mirrors TmuxSignaler.)
func (c *CmuxSignaler) Detect(pid int) bool {
	_, ok := c.FindCmuxServerAncestor(context.Background(), pid)
	return ok
}

// FindCmuxServerAncestor walks the process tree from targetPID upward,
// returning the PID of the first ancestor whose process name (comm) is
// "cmux". Returns (0, false) if no such ancestor exists. Used by the
// poller to enrich session TerminalHost with bridge-status information.
func (c *CmuxSignaler) FindCmuxServerAncestor(ctx context.Context, targetPID int) (int, bool) {
	servers, err := c.cachedCmuxServerPIDs(ctx)
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
		ppid, err := c.parentPID(ctx, pid)
		if err != nil || ppid < 1 {
			return 0, false
		}
		pid = ppid
	}
}

// cachedCmuxServerPIDs returns the PIDs of running cmux server processes
// (those where `ps -A -o pid,comm` reports comm == "cmux"). Cached for
// serverPIDCacheTTL so a sweep over N sessions runs ps once.
func (c *CmuxSignaler) cachedCmuxServerPIDs(ctx context.Context) ([]int, error) {
	c.cacheMu.Lock()
	defer c.cacheMu.Unlock()
	if !c.serverCacheAt.IsZero() && c.clock().Sub(c.serverCacheAt) < serverPIDCacheTTL {
		return c.serverCachePids, c.serverCacheErr
	}
	// The result is cached and shared by every later caller, so one caller's
	// cancellation must not poison the cache: detach (values kept) and bound it.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	pids, err := c.enumerateCmuxServerPIDs(ctx)
	c.serverCachePids, c.serverCacheErr, c.serverCacheAt = pids, err, c.clock()
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
func (c *CmuxSignaler) parentPID(ctx context.Context, pid int) (int, error) {
	if c.Procs != nil {
		return hopFromTable(c.Procs, pid)
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
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
