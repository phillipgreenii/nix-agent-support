package inputproc

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

const envKey = "CETA_INPUT_PROCESSOR"
const timeout = 3 * time.Second

// Configured returns true if an input processor command is set.
func Configured() bool {
	return os.Getenv(envKey) != ""
}

// Process calls the configured input processor with the given command.
// Returns the rewritten command and true if the processor rewrote it,
// or the original command and false if no rewrite occurred.
func Process(command string) (string, bool) {
	procCmd := os.Getenv(envKey)
	if procCmd == "" {
		return command, false
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	parts := strings.Fields(procCmd)
	args := append(parts[1:], command)
	cmd := exec.CommandContext(ctx, parts[0], args...)

	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 {
			return command, false
		}
		fmt.Fprintf(os.Stderr, "claude-extended-tool-approver: input processor: %v\n", err)
		return command, false
	}

	rewritten := strings.TrimSpace(string(out))
	if rewritten == "" {
		return command, false
	}

	return rewritten, true
}
