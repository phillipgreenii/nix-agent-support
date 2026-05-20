package signal

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// tmuxCacheTTL is how long an enumeration result stays fresh. Mirrors
// CmuxSignaler's surfaceCacheTTL so a single signalNonWorking pass over N
// non-Working sessions runs ps + per-socket list-panes once, not N times.
const tmuxCacheTTL = 2 * time.Second

// paneLoc identifies a tmux pane by the socket name (`-L`) of its server and
// the canonical pane target string (`<session>:<window>.<pane>`).
type paneLoc struct {
	socketName string
	paneID     string
}

// TmuxSignaler sends keys to the tmux pane hosting a process. Multi-socket
// aware: discovers running tmux servers via ps, enumerates panes per socket,
// caches the result for tmuxCacheTTL.
// RunCmd is injectable for tests; nil defaults to exec.CommandContext.
type TmuxSignaler struct {
	RunCmd func(ctx context.Context, name string, args ...string) ([]byte, error)

	cacheMu   sync.Mutex
	cacheAt   time.Time
	cacheLocs map[int]paneLoc
	cacheErr  error
}

func (t *TmuxSignaler) Name() string { return "tmux" }

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
// (not just paneID) so Send (Task 6) can use the socketName too.
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

// Send injects text + Enter into the tmux pane that contains pid. Uses
// cachedPanes for socket+pane discovery, so signalNonWorking over N sessions
// pays the enumeration cost once per cache TTL window. The matched pane's
// socket name is threaded through the send-keys call via -L.
func (t *TmuxSignaler) Send(pid int, text string) error {
	locs, err := t.cachedPanes()
	if err != nil {
		return fmt.Errorf("tmux enumerate: %w", err)
	}
	loc := t.findPaneLocForPID(locs, pid)
	if loc == nil {
		return fmt.Errorf("signal: no tmux pane found for pid %d", pid)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = t.run(ctx, "tmux", "-L", loc.socketName, "send-keys", "-t", loc.paneID, text, "Enter")
	return err
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

// cachedPanes returns the pane map, caching for tmuxCacheTTL. Mirrors
// CmuxSignaler.cachedSurfaces — a single signalNonWorking pass over N
// non-Working sessions runs ps + per-socket list-panes once, not N times.
// Errors are cached for the same window so a transient ps failure doesn't
// fan out into N error reports.
func (t *TmuxSignaler) cachedPanes() (map[int]paneLoc, error) {
	t.cacheMu.Lock()
	defer t.cacheMu.Unlock()
	if t.cacheAt != (time.Time{}) && time.Since(t.cacheAt) < tmuxCacheTTL {
		return t.cacheLocs, t.cacheErr
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	locs, err := t.enumeratePanes(ctx)
	t.cacheLocs, t.cacheErr, t.cacheAt = locs, err, time.Now()
	return locs, err
}
