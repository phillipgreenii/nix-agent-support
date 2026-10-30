package settle

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// lsofPaths are where lsof lives when the daemon's PATH (a launch agent has
// almost none) does not find it.
var lsofPaths = []string{"/usr/sbin/lsof", "/usr/bin/lsof"}

// LsofHolders lists the processes that hold path open, by asking lsof. A path no
// process holds is an empty list; an lsof that cannot be run is an error, which
// removal treats as "a holder may exist" and so restores the file.
func LsofHolders(path string) ([]int, error) {
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
	cmd := exec.Command(bin, "-t", "--", path)
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
