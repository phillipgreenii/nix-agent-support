//go:build darwin

package proc

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"time"
)

// launchctlRun runs launchctl with args and returns its combined output and
// whether it ran and exited 0. It is a variable so tests can stand in for the
// real launchctl.
var launchctlRun = func(ctx context.Context, args ...string) (out string, ran bool, ok bool) {
	b, err := exec.CommandContext(ctx, "launchctl", args...).CombinedOutput()
	if err == nil {
		return string(b), true, true
	}
	if _, isExit := err.(*exec.ExitError); isExit {
		return string(b), true, false
	}
	return string(b), false, false
}

// probeTimeout bounds one launchctl call: a status report must not hang on a
// stuck launchd.
const probeTimeout = 5 * time.Second

var launchctlRunning = regexp.MustCompile(`(?m)^\s*state = running\s*$`)

// domainTarget is the launchd service target of the daemon's job: the per-user
// GUI domain of the invoking uid.
func domainTarget(label string) string {
	return fmt.Sprintf("gui/%d/%s", os.Getuid(), label)
}

// SupervisorProbe asks launchd about the daemon's job:
// launchctl print gui/<uid>/<label>, with the uid from os.Getuid(). A zero
// exit means the job is registered (and "state = running" in the output that
// it runs); a non-zero exit means launchd has no such job; launchctl itself
// failing to run is SupervisorUnknown.
func SupervisorProbe(label string) SupervisorState {
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	out, ran, ok := launchctlRun(ctx, "print", domainTarget(label))
	switch {
	case !ran:
		return SupervisorUnknown
	case !ok:
		return SupervisorNotRegistered
	case launchctlRunning.MatchString(out):
		return SupervisorRunning
	default:
		return SupervisorRegistered
	}
}

// RestartHint is the text an unavailable answer carries so a caller can bring
// the daemon back: launchctl kickstart -k gui/<uid>/<label> (-k restarts a
// running job; the config path is not needed because launchd already holds the
// command line; binary is not needed either).
func RestartHint(label, configPath, binary string) string {
	return "restart it with: launchctl kickstart -k " + domainTarget(label)
}
