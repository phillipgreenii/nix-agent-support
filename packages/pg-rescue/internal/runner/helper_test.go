// helper_test.go: the fake command, handler and verify. They are one small
// program, internal/fakes/runnercmd, that TestMain builds once WITHOUT the race
// detector (testenv.BuildHelper) and the tests start as real child processes.
// Re-executing the race-built test binary instead made every supervised child
// pay the race runtime's 1.0 s exit sleep (bead pg2-cp2dfo). The behavior is
// the first argument; leading key=value arguments after it parameterize the
// behavior and anything after those is literal.
package runner_test

import "strings"

// helperBin is the path of the built runnercmd, set by TestMain.
var helperBin string

// helperArgv is the argv that runs the helper with a behavior.
func helperArgv(behavior string, args ...string) []string {
	return append([]string{helperBin, behavior}, args...)
}

// helperShell is a shell command line that runs the helper (for --verify).
func helperShell(behavior string, args ...string) string {
	parts := []string{`exec "$HELPER_BIN"`, behavior}
	for _, a := range args {
		parts = append(parts, "'"+strings.ReplaceAll(a, "'", `'\''`)+"'")
	}
	return strings.Join(parts, " ")
}
