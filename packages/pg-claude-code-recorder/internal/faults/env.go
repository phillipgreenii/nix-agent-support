//go:build faults

package faults

import "os"

// The integration binary is built with the faults tag so that
// PG_CLAUDE_CODE_RECORDER_FAULT=<name> kills the process (as a crash would) at
// the named point.
func init() {
	if name := os.Getenv("PG_CLAUDE_CODE_RECORDER_FAULT"); name != "" {
		Arm(name, func() { os.Exit(137) })
	}
}
