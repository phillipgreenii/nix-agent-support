package signal

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// tmuxCacheTTL is how long an enumeration result stays fresh, so a single
// signalNonWorking pass over N non-Working sessions runs ps + per-socket
// list-panes once, not N times. It matches CmuxSignaler's serverPIDCacheTTL
// (both ration a whole-table `ps -A`). Worst-case
// staleness is this TTL plus up to one daemon tick (5 s); a pane created inside
// that window is missed by Detect until the cache refreshes.
const tmuxCacheTTL = 10 * time.Second

// paneLoc identifies a tmux pane by the socket name (`-L`) of its server and
// the canonical pane target string (`<session>:<window>.<pane>`).
type paneLoc struct {
	socketName string
	paneID     string
}

// TmuxSignaler detects the tmux pane hosting a process. Multi-socket
// aware: discovers running tmux servers via ps, enumerates panes per socket,
// caches the result for tmuxCacheTTL.
// RunCmd is injectable for tests; nil defaults to exec.CommandContext.
type TmuxSignaler struct {
	RunCmd func(ctx context.Context, name string, args ...string) ([]byte, error)

	// LookPath resolves the tmux executable for the availability gate (see
	// available). nil means: when RunCmd is injected the gate is open (the
	// injected runner replaces process execution entirely, so PATH is
	// irrelevant); otherwise exec.LookPath.
	LookPath func(file string) (string, error)

	// Procs, when set, answers every ancestry hop from the shared per-tick
	// process snapshot instead of one `ps -o ppid=,comm=` exec per hop.
	Procs ProcessTable

	cacheMu   sync.Mutex
	cacheAt   time.Time
	cacheLocs map[int]paneLoc
	cacheErr  error

	// now is the clock the TTL check reads; nil means time.Now. Tests inject
	// one so they can cross the TTL without sleeping.
	now func() time.Time
}

func (t *TmuxSignaler) clock() time.Time {
	if t.now != nil {
		return t.now()
	}
	return time.Now()
}

// errTmuxUnavailable is returned (uncached) when tmux is not resolvable on PATH.
var errTmuxUnavailable = errors.New("signal: tmux not on PATH")

// available reports whether tmux can possibly be in play: its executable is
// resolvable on PATH. When it is not, every TmuxSignaler entry point that would
// spawn a process (the whole-table `ps -A` enumeration and the per-ancestor
// `ps -o ppid=,comm=` walk) is short-circuited, so a cmux-only machine does not
// pay for - or emit security-logged process-discovery noise from - a tmux
// probe that can never find anything. exec.LookPath is stat calls only; it
// spawns nothing. Known trap: a launchd PATH can lack tmux even when it is
// installed; that case already failed before this gate (list-panes errored and
// the panes were skipped), so gating does not regress it, and the daemon's
// startup MissingBinaries ERROR remains the signal for it.
func (t *TmuxSignaler) available() bool {
	switch {
	case t.LookPath != nil:
		_, err := t.LookPath("tmux")
		return err == nil
	case t.RunCmd != nil:
		return true
	default:
		_, err := exec.LookPath("tmux")
		return err == nil
	}
}

func (t *TmuxSignaler) Name() string { return "tmux" }

// RequiredBinaries reports the executables TmuxSignaler shells out to. Used by
// the daemon's startup check: without `tmux` on PATH, enumeratePanes silently
// swallows the exec error and every tmux session classifies as "unknown".
func (t *TmuxSignaler) RequiredBinaries() []string { return []string{"tmux"} }

func (t *TmuxSignaler) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if t.RunCmd != nil {
		return t.RunCmd(ctx, name, args...)
	}
	return exec.CommandContext(ctx, name, args...).Output()
}

// Detect returns true iff a known tmux pane lists pid (or one of its
// ancestors) as its shell pid. Pid-aware via cachedPanes — comm-only matching
// (pre-Phase-3 behavior) is dropped because it produced false positives when
// a tmuxinator-like ancestor existed without a matching pane.
func (t *TmuxSignaler) Detect(pid int) bool {
	locs, err := t.cachedPanes()
	if err != nil {
		return false
	}
	return t.findPaneLocForPID(locs, pid) != nil
}

// findPaneLocForPID walks up the process tree from targetPID until it finds a
// pid that exists in the locs map, or runs out of ancestors. Returns *paneLoc
// (not just paneID).
func (t *TmuxSignaler) findPaneLocForPID(locs map[int]paneLoc, targetPID int) *paneLoc {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	seen := map[int]bool{}
	pid := targetPID
	for {
		if pid < 1 || seen[pid] {
			return nil
		}
		seen[pid] = true
		if loc, ok := locs[pid]; ok {
			return &loc
		}
		if t.Procs != nil {
			ppid, _ := t.Procs.Parent(pid)
			if ppid < 1 {
				return nil
			}
			pid = ppid
			continue
		}
		out, err := t.run(ctx, "ps", "-o", "ppid=,comm=", "-p", strconv.Itoa(pid))
		if err != nil {
			return nil
		}
		fields := strings.Fields(strings.TrimSpace(string(out)))
		if len(fields) < 1 {
			return nil
		}
		ppid, err := strconv.Atoi(fields[0])
		if err != nil || ppid < 1 {
			return nil
		}
		pid = ppid
	}
}

// enumeratePanes discovers running tmux servers via `ps -A -o pid,comm,args`
// (filtering comm == "tmux", parsing argv for `-L <name>`, defaulting absent
// `-L` to "default"), then runs `tmux -L <name> list-panes -a -F "..."` per
// deduplicated socket name and merges results into a single map keyed by
// pane shell pid.
//
// Per-socket errors (e.g. server died between ps and list-panes) are silently
// skipped — partial discovery is the normal case for transient process churn.
// A failure of the `ps` call itself returns an error.
func (t *TmuxSignaler) enumeratePanes(ctx context.Context) (map[int]paneLoc, error) {
	psOut, err := t.run(ctx, "ps", "-A", "-o", "pid,comm,args")
	if err != nil {
		return nil, fmt.Errorf("ps -A: %w", err)
	}
	socketNames := parseTmuxSocketNames(string(psOut))
	result := map[int]paneLoc{}
	for _, name := range socketNames {
		out, err := t.run(ctx, "tmux", "-L", name, "list-panes", "-a", "-F",
			"#{pane_pid} #{session_name}:#{window_index}.#{pane_index}")
		if err != nil {
			continue
		}
		for line := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
			fields := strings.Fields(line)
			if len(fields) < 2 {
				continue
			}
			pid, err := strconv.Atoi(fields[0])
			if err != nil {
				continue
			}
			result[pid] = paneLoc{socketName: name, paneID: fields[1]}
		}
	}
	return result, nil
}

// parseTmuxSocketNames takes `ps -A -o pid,comm,args` stdout and returns the
// deduplicated list of `-L <name>` values found across rows where the second
// field (comm) is exactly "tmux". Rows without an explicit `-L` flag yield
// the name "default".
func parseTmuxSocketNames(psOut string) []string {
	seen := map[string]bool{}
	var names []string
	for line := range strings.SplitSeq(strings.TrimSpace(psOut), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 || fields[1] != "tmux" {
			continue
		}
		name := "default"
		for i := 2; i < len(fields)-1; i++ {
			if fields[i] == "-L" {
				name = fields[i+1]
				break
			}
		}
		if !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	return names
}

// cachedPanes returns the pane map, caching for tmuxCacheTTL, so a single
// pass over N non-Working sessions runs ps + per-socket list-panes once, not N
// times.
// Errors are cached for the same window so a transient ps failure doesn't
// fan out into N error reports.
func (t *TmuxSignaler) cachedPanes() (map[int]paneLoc, error) {
	t.cacheMu.Lock()
	defer t.cacheMu.Unlock()
	if !t.available() {
		return nil, errTmuxUnavailable
	}
	if t.cacheAt != (time.Time{}) && t.clock().Sub(t.cacheAt) < tmuxCacheTTL {
		return t.cacheLocs, t.cacheErr
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	locs, err := t.enumeratePanes(ctx)
	t.cacheLocs, t.cacheErr, t.cacheAt = locs, err, t.clock()
	return locs, err
}
