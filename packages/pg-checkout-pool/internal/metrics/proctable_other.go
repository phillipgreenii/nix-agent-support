//go:build !darwin

package metrics

import (
	"bufio"
	"context"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// ListProcs reads the process table with ps (the non-darwin fallback). ps
// reports no start time here, so the daemon counter does not cache.
func ListProcs() ([]Proc, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ps", "-axo", "pid=,command=").Output()
	if err != nil {
		return nil, err
	}
	var procs []Proc
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 2 {
			continue
		}
		if pid, err := strconv.Atoi(f[0]); err == nil {
			procs = append(procs, Proc{Pid: pid, Args: strings.Join(f[1:], " ")})
		}
	}
	return procs, nil
}
