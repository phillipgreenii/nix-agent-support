package proc

import "strings"

// SupervisorState is what SupervisorProbe learned about the process supervisor
// of the daemon.
type SupervisorState int

const (
	// SupervisorNone: this platform has no supervisor integration (Linux
	// until pg2-opk5g), so there is nothing to probe and nothing to restart
	// the daemon for the operator.
	SupervisorNone SupervisorState = iota
	// SupervisorNotRegistered: the supervisor is there but knows no job with
	// the daemon's label (the home-manager entry is not applied).
	SupervisorNotRegistered
	// SupervisorRegistered: the job is registered but the daemon is not
	// running under it right now.
	SupervisorRegistered
	// SupervisorRunning: the job is registered and the daemon is running.
	SupervisorRunning
	// SupervisorUnknown: the probe itself failed (the supervisor tool is
	// missing or answered something unreadable); nothing is known.
	SupervisorUnknown
)

// String names the state for status output and logs.
func (s SupervisorState) String() string {
	switch s {
	case SupervisorNone:
		return "none"
	case SupervisorNotRegistered:
		return "not_registered"
	case SupervisorRegistered:
		return "registered"
	case SupervisorRunning:
		return "running"
	default:
		return "unknown"
	}
}

// shellQuote single-quotes s when it holds anything a shell would interpret,
// so a restart hint pasted into a shell runs the intended command.
func shellQuote(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\n'\"\\$`;&|<>()*?[]#~=%!{}") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
