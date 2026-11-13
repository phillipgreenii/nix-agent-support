package metrics

import (
	"bufio"
	"context"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ParsePSPids returns the pids of `ps -axo pid=,command=` lines that run the
// git fsmonitor daemon.
func ParsePSPids(ps string) []int {
	var pids []int
	sc := bufio.NewScanner(strings.NewReader(ps))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 2 || !strings.Contains(sc.Text(), "fsmonitor--daemon") {
			continue
		}
		if pid, err := strconv.Atoi(f[0]); err == nil {
			pids = append(pids, pid)
		}
	}
	return pids
}

// CountInside counts the processes of `lsof -Fpn` output (fields p<pid> and
// n<cwd>) whose working directory is inside dir.
func CountInside(lsof, dir string) int {
	prefix := strings.TrimSuffix(dir, "/") + "/"
	n := 0
	var pid string
	counted := map[string]bool{}
	for _, l := range strings.Split(lsof, "\n") {
		switch {
		case strings.HasPrefix(l, "p"):
			pid = l[1:]
		case strings.HasPrefix(l, "n") && strings.HasPrefix(l[1:], prefix) && !counted[pid]:
			counted[pid] = true
			n++
		}
	}
	return n
}

// Proc is one entry of the process table, reduced to what the daemon census
// needs. Start is the process start time: with Pid it identifies a process
// even after the kernel reuses the pid. Start is zero when the platform
// reader cannot supply it (the ps fallback), which disables caching.
type Proc struct {
	Pid   int
	Start time.Time
	Args  string
}

// ProcLister returns the processes that may be fsmonitor daemons.
type ProcLister func() ([]Proc, error)

// LsofFunc runs `lsof -a -d cwd -Fpn -p <pids>` and returns its output.
type LsofFunc func(ctx context.Context, pids []int) ([]byte, error)

type procKey struct {
	pid   int
	start time.Time
}

type cwdEntry struct {
	cwd     string
	expires time.Time
}

// DaemonCounter counts the git fsmonitor daemons running inside a slots
// directory. It reads the process table in-process (no ps launch on darwin)
// and caches each daemon's working directory, keyed by pid and process start
// time, so lsof only runs for daemons it has not seen before.
type DaemonCounter struct {
	list  ProcLister
	lsof  LsofFunc
	now   func() time.Time
	ttl   time.Duration
	mu    sync.Mutex
	cache map[procKey]cwdEntry
}

// NewDaemonCounter returns a counter wired to the platform process-table
// reader and the real lsof.
func NewDaemonCounter() *DaemonCounter {
	return NewDaemonCounterWith(ListProcs, runLsof, time.Now)
}

// NewDaemonCounterWith injects the process-table reader, lsof and the clock.
func NewDaemonCounterWith(list ProcLister, lsof LsofFunc, now func() time.Time) *DaemonCounter {
	return &DaemonCounter{list: list, lsof: lsof, now: now, ttl: 5 * time.Minute, cache: map[procKey]cwdEntry{}}
}

func runLsof(ctx context.Context, pids []int) ([]byte, error) {
	ids := make([]string, len(pids))
	for i, p := range pids {
		ids[i] = strconv.Itoa(p)
	}
	return exec.CommandContext(ctx, "lsof", "-a", "-d", "cwd", "-Fpn", "-p", strings.Join(ids, ",")).Output()
}

// parseCwds maps pid to working directory from `lsof -Fpn` output.
func parseCwds(lsof string) map[int]string {
	out := map[int]string{}
	pid := 0
	for _, l := range strings.Split(lsof, "\n") {
		switch {
		case strings.HasPrefix(l, "p"):
			pid, _ = strconv.Atoi(l[1:])
		case strings.HasPrefix(l, "n") && pid != 0:
			if _, seen := out[pid]; !seen {
				out[pid] = l[1:]
			}
		}
	}
	return out
}

// CountDaemons counts the fsmonitor daemons running inside slotsDir. The pool
// keeps one daemon per slot, so a count above the slot count means orphans.
func (c *DaemonCounter) CountDaemons(slotsDir string) (int, error) {
	if r, err := filepath.EvalSymlinks(slotsDir); err == nil {
		slotsDir = r
	}
	prefix := strings.TrimSuffix(slotsDir, "/") + "/"
	procs, err := c.list()
	if err != nil {
		return 0, err
	}
	now := c.now()
	c.mu.Lock()
	defer c.mu.Unlock()

	live := map[procKey]bool{}
	var daemons []Proc
	for _, p := range procs {
		if !strings.Contains(p.Args, "fsmonitor--daemon") {
			continue
		}
		daemons = append(daemons, p)
		live[procKey{p.Pid, p.Start}] = true
	}
	for k, e := range c.cache {
		if !live[k] || !now.Before(e.expires) {
			delete(c.cache, k)
		}
	}
	if len(daemons) == 0 {
		return 0, nil
	}

	cwds := map[int]string{}
	var missing []int
	for _, p := range daemons {
		if e, ok := c.cache[procKey{p.Pid, p.Start}]; ok && !p.Start.IsZero() {
			cwds[p.Pid] = e.cwd
		} else {
			missing = append(missing, p.Pid)
		}
	}
	if len(missing) > 0 {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		// lsof exits 1 when some pid vanished; the output it did produce is valid.
		out, err := c.lsof(ctx, missing)
		if err != nil && len(out) == 0 {
			if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 {
				return 0, nil
			}
			return 0, err
		}
		found := parseCwds(string(out))
		for _, p := range daemons {
			cwd, ok := found[p.Pid]
			if !ok {
				continue
			}
			cwds[p.Pid] = cwd
			if !p.Start.IsZero() {
				c.cache[procKey{p.Pid, p.Start}] = cwdEntry{cwd: cwd, expires: now.Add(c.ttl)}
			}
		}
	}
	n := 0
	for _, p := range daemons {
		if cwd, ok := cwds[p.Pid]; ok && strings.HasPrefix(cwd, prefix) {
			n++
		}
	}
	return n, nil
}
