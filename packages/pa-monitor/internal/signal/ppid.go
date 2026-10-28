package signal

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// ParentPID returns pid's parent via one `ps -o ppid= -p <pid>` exec. ok is
// false when the lookup fails or the pid has no parent. It backs the provider
// cache's unknown-host backoff reset (a changed parent re-probes at once).
func ParentPID(pid int) (ppid int, ok bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ps", "-o", "ppid=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return 0, false
	}
	ppid, err = strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil || ppid < 1 {
		return 0, false
	}
	return ppid, true
}
