package main

// Process exit codes. The contract follows the workspace code-file-standards
// "Exit Codes" rule: 1 is the generic / unplanned error and MUST NOT carry a
// branchable meaning, 2 is a usage error, and each specific, callable-branchable
// condition has its own distinct code >= 3. Callers (shell `if`, the
// wait-for-agents-to-finish wrapper, docs) MUST branch only on the codes below.
//
//	0 success
//	1 generic / unplanned error (exitGeneric)
//	2 usage error: bad flag, missing or malformed argument (exitUsage)
//	3 agents-busy-check: daemon up, no agent `working` ("all idle") (exitNotBusy)
//	4 wait-until-agents-finished: --maximum-wait elapsed (exitTimeout)
//	5 info: the selector matched nothing (exitNoMatch)
//	6 the daemon is unreachable or failed an RPC (exitDaemonUnavailable)
//
// Bead pg2-xrmfng (operator ruling on pg2-9jk3g5, 2026-10-28).
const (
	exitGeneric           = 1
	exitUsage             = 2
	exitNotBusy           = 3
	exitTimeout           = 4
	exitNoMatch           = 5
	exitDaemonUnavailable = 6
)
