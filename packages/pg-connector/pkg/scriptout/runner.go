// runner.go: the one process-spawn seam for backend invocations. Production
// runs every backend through x/cmdrun.Exec (own process group, SIGTERM grace
// before SIGKILL, per-call Timeout), so a cancelled or timed-out umbrella call
// ends the backend's whole process tree rather than leaving its gh/bd/pjira/
// claude children running to their own deadline. Tests answer the seam in
// process (cmdruntest.Fake or a cmdrun.Runner closure) instead of
// re-executing the test binary (a race-built child sleeps 1 s at exit).
package scriptout

import "github.com/phillipgreenii/x/cmdrun"

// execRunner runs the backend binary. nil means cmdrun.Exec; tests swap it for
// an in-process double.
var execRunner cmdrun.Runner

// runner returns the active runner.
func runner() cmdrun.Runner {
	if execRunner == nil {
		return cmdrun.Exec
	}
	return execRunner
}

// SwapRunner replaces the runner that every backend invocation uses and returns
// a function that restores the previous one. It exists so the umbrella's
// tests can answer a backend call in process (a fake backend registered by
// name) instead of writing, and then exec'ing, a script per test. It MUST be
// called before any goroutine invokes a backend (a test package's TestMain),
// because the runner variable is not synchronised.
func SwapRunner(r cmdrun.Runner) (restore func()) {
	prev := execRunner
	execRunner = r
	return func() { execRunner = prev }
}

// ChildTimeout is the backstop Timeout every backend-side child Call carries
// (a backend's own gh/git/bd/pjira/claude exec). The handler's context
// already carries the op's real deadline (30s to a configured 300s), so the
// per-call bound never fires before it; it only ends a child that a context
// without a deadline would otherwise leave running forever. It is the largest
// configurable op budget, so it can never cut a call shorter than its op's own
// deadline.
const ChildTimeout = MaxConfiguredTimeout

// BoundChild fills the process-ending fields every backend-side child Call
// needs besides Timeout: DefaultWaitDelay for a grandchild that holds a pipe
// open, and its own process group with a SIGTERM grace before SIGKILL so the
// child's whole tree ends with it. Timeout stays a literal field of the
// caller's Call (set to ChildTimeout) because the bounded-call guard scans for
// it. Callers set Name, Args, Dir, Env and Stdin.
func BoundChild(c cmdrun.Call) cmdrun.Call {
	c.WaitDelay = DefaultWaitDelay
	c.OwnProcGroup = true
	c.TermGrace = DefaultTermGrace
	return c
}
