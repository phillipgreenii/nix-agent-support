package view

import "github.com/phillipgreenii/x/cmdrun"

// The subprocess seam is the shared x command runner (cmdrun); view names its
// types locally so call sites read as before. Stdin and Stdout/Stderr are
// bytes, a signal is Result.Signal, and Call.Dir and Call.Env/EnvMode are
// carried by the shared Call.
type (
	// Call is one subprocess view runs.
	Call = cmdrun.Call
	// CallResult is what a Call did.
	CallResult = cmdrun.Result
	// Runner runs a Call. Production uses ExecRunner; tests answer in process
	// (a double), so no test spawns a child.
	Runner = cmdrun.Runner
)

// ExecRunner runs the call as a real process.
var ExecRunner Runner = cmdrun.Exec
