package metrics

import (
	"bufio"
	"context"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
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

// CountDaemons counts the fsmonitor daemons running inside slotsDir, using ps
// and lsof (both ship with macOS). The pool keeps one daemon per slot, so a
// count above the slot count means orphans.
func CountDaemons(slotsDir string) (int, error) {
	if r, err := filepath.EvalSymlinks(slotsDir); err == nil {
		slotsDir = r
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	ps, err := exec.CommandContext(ctx, "ps", "-axo", "pid=,command=").Output()
	if err != nil {
		return 0, err
	}
	pids := ParsePSPids(string(ps))
	if len(pids) == 0 {
		return 0, nil
	}
	ids := make([]string, len(pids))
	for i, p := range pids {
		ids[i] = strconv.Itoa(p)
	}
	// lsof exits 1 when some pid vanished; the output it did produce is valid.
	out, err := exec.CommandContext(ctx, "lsof", "-a", "-d", "cwd", "-Fpn", "-p", strings.Join(ids, ",")).Output()
	if err != nil && len(out) == 0 {
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 {
			return 0, nil
		}
		return 0, err
	}
	return CountInside(string(out), slotsDir), nil
}
