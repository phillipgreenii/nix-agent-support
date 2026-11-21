package client

import "github.com/phillipgreenii/x/exitcode"

// The exit codes of pg-connector verbs are the shared workspace codes of
// x/exitcode, re-exported so a consumer needs no second import. This package
// MUST NOT define a new code.
const (
	ExitOK          = exitcode.OK
	ExitError       = exitcode.Error
	ExitUsage       = exitcode.Usage
	ExitUnavailable = exitcode.Unavailable
	ExitNotFound    = exitcode.NotFound
	ExitDegraded    = exitcode.Degraded
	ExitConfig      = exitcode.Config
	ExitDenied      = exitcode.Denied
)
