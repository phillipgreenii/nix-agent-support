//go:build faults

package faults

import "os"

// The integration binary is built with the faults tag so that
// PG_CLAUDE_CODE_RECORDER_FAULT=<name> kills the process (as a crash would) at
// the named point, and PG_CLAUDE_CODE_RECORDER_FAULT_BLOCK=<name> makes the
// goroutine that reaches the point block forever (a stuck step that ignores the
// stop signal, so the shutdown deadline watchdog has something to cut off).
func init() {
	if name := os.Getenv("PG_CLAUDE_CODE_RECORDER_FAULT"); name != "" {
		Arm(name, func() { os.Exit(137) })
	}
	if name := os.Getenv("PG_CLAUDE_CODE_RECORDER_FAULT_BLOCK"); name != "" {
		Arm(name, func() { select {} })
	}
}
