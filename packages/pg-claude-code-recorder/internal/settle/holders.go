package settle

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// lsofTimeout bounds one lsof run; lsofWaitDelay is how long after the kill the
// pipes are given up on (a child that kept them open would otherwise hold Wait).
const (
	lsofTimeout   = 30 * time.Second
	lsofWaitDelay = time.Second
)

// lsofPaths are where lsof lives when the daemon's PATH (a launch agent has
// almost none) does not find it.
var lsofPaths = []string{"/usr/sbin/lsof", "/usr/bin/lsof"}

// LsofHolders lists the processes that hold path open, by asking lsof. A path no
// process holds is an empty list; an lsof that cannot be run is an error, which
// removal treats as "a holder may exist" and so restores the file.
func LsofHolders(path string) ([]int, error) { return LsofHoldersCtx(context.Background(), path) }

// LsofHoldersCtx is LsofHolders under ctx and a 30 s limit: lsof is killed when
// either ends, so shutdown does not wait on a stuck lsof. The error it returns
// then is the "a holder may exist" kind, so removal restores the file.
func LsofHoldersCtx(ctx context.Context, path string) ([]int, error) {
	bin, err := exec.LookPath("lsof")
	if err != nil {
		bin = ""
		for _, p := range lsofPaths {
			if _, serr := os.Stat(p); serr == nil {
				bin = p
				break
			}
		}
		if bin == "" {
			return nil, errors.New("lsof not found")
		}
	}
	ctx, cancel := context.WithTimeout(ctx, lsofTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "-t", "--", path)
	cmd.WaitDelay = lsofWaitDelay
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() == 1 && out.Len() == 0 {
			return nil, nil // lsof exits 1 when nothing holds the file
		}
		return nil, err
	}
	var pids []int
	for _, l := range strings.Fields(out.String()) {
		if n, err := strconv.Atoi(l); err == nil {
			pids = append(pids, n)
		}
	}
	return pids, nil
}
