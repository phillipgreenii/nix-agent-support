package bd

import "time"

// Flags appended to every bd invocation. --readonly and --sandbox are global
// flags; --json selects the envelope output.
var commonFlags = []string{"--json", "--readonly", "--sandbox"}

// timeLayout is the RFC3339 form passed to the date filters.
const timeLayout = time.RFC3339

func withCommon(args []string) []string {
	out := make([]string, 0, len(args)+len(commonFlags))
	out = append(out, args...)
	return append(out, commonFlags...)
}

// ListArgv builds the argument vector of a bd list call. -n 0 is always
// passed: the default limit of 50 silently truncates.
func ListArgv(o ListOpts) []string {
	args := []string{"list", "-n", "0"}
	if o.All {
		args = append(args, "--all")
	}
	if !o.CreatedAfter.IsZero() {
		args = append(args, "--created-after", o.CreatedAfter.UTC().Format(timeLayout))
	}
	if !o.ClosedAfter.IsZero() {
		args = append(args, "--closed-after", o.ClosedAfter.UTC().Format(timeLayout))
	}
	return withCommon(args)
}

// ReadyArgv builds the argument vector of a bd ready call. extra holds queue
// flags; -n 0 is always passed because the default limit of 100 silently
// truncates.
func ReadyArgv(extra []string) []string {
	args := []string{"ready"}
	args = append(args, extra...)
	args = append(args, "-n", "0")
	return withCommon(args)
}

// BlockedArgv builds the argument vector of a bd blocked call. blocked
// rejects -n.
func BlockedArgv() []string { return withCommon([]string{"blocked"}) }

// CountByStatusArgv builds the argument vector of a bd count --by-status
// call. count rejects -n.
func CountByStatusArgv() []string { return withCommon([]string{"count", "--by-status"}) }

// StatusesArgv builds the argument vector of a bd statuses call. statuses
// rejects -n.
func StatusesArgv() []string { return withCommon([]string{"statuses"}) }
