// runner.go: the command-runner seam. The Call/Result/Runner shape is the
// shared one in github.com/phillipgreenii/x/cmdrun; tests answer the Runner
// in process with the cmdruntest fake instead of re-executing the test binary
// (a race-built child sleeps 1 s at exit). The real exec path is cmdrun.Exec,
// covered by the shared cmdruntest.RunContract suite in x.
package main

import "github.com/phillipgreenii/x/cmdrun"

// runner is the seam: nil means cmdrun.Exec. Tests swap it.
var runner cmdrun.Runner

func runnerOrExec() cmdrun.Runner {
	if runner == nil {
		return cmdrun.Exec
	}
	return runner
}
