package main

import "github.com/phillipgreenii/x/exitcode"

// Process exit codes. The contract follows the shared x/exitcode table and the
// workspace code-file-standards "Exit Codes" rule: 1 is the generic /
// unplanned error and MUST NOT carry a branchable meaning. Callers (shell
// `if`, docs) MUST branch only on the codes below.
//
//	0  success                                                   (exitcode.OK)
//	1  generic / unplanned error                                 (exitGeneric)
//	2  usage error: bad flag, missing or malformed argument      (exitUsage)
//	3  the daemon is unreachable or failed an RPC                (exitDaemonUnavailable)
//	4  info: the selector matched nothing                        (exitNoMatch)
//	20 agents-busy-check: daemon up, no agent `working`          (exitNotBusy)
//
// Before the shared codes: 3 meant "all idle", 4 was retired, 5 meant no
// match and 6 meant daemon unreachable.
const (
	exitGeneric           = exitcode.Error
	exitUsage             = exitcode.Usage
	exitDaemonUnavailable = exitcode.Unavailable
	exitNoMatch           = exitcode.NotFound

	// exitNotBusy is the app-specific "all idle" answer of agents-busy-check:
	// a result, not a failure. It is the first code of the app range 20..63.
	exitNotBusy = 20
)
