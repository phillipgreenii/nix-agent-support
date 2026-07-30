package inputproc

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

const (
	envKey = "CETA_INPUT_PROCESSOR"

	// defaultTimeout is the budget the SHIPPED input processor gets for one
	// rewrite. It has not changed; the test suite widens the deadline it
	// installs instead of widening this (see inputproc_test.go's TestMain).
	defaultTimeout = 3 * time.Second
)

// timeout is the exec deadline Process actually applies. It is a var, separate
// from defaultTimeout, solely so this package's tests can install a generous
// value: their mock processor is a freshly written /bin/sh script, and a
// fork+exec that loses the CPU for seconds under nix-sandbox load is killed by
// the deadline — which Process can only report as "no rewrite", making a slow
// machine indistinguishable from a processor that declined. That is what made
// the go-tests gate nondeterministic: the same derivation hash failed once and
// passed on rebuild. Production never reassigns this, so the shipped budget is
// still defaultTimeout.
var timeout = defaultTimeout

// Configured returns true if an input processor command is set.
func Configured() bool {
	return os.Getenv(envKey) != ""
}

// Process calls the configured input processor with the given command.
// Returns the rewritten command and true if the processor rewrote it,
// or the original command and false if no rewrite occurred.
func Process(command string) (string, bool) {
	rewritten, changed, err := process(command)
	if err != nil {
		fmt.Fprintf(os.Stderr, "claude-extended-tool-approver: input processor: %v\n", err)
	}
	return rewritten, changed
}

// process is Process with the REASON for a non-rewrite retained. Every path
// Process treats as an ordinary "no rewrite" — env unset, exit 1, empty stdout —
// returns a nil error; every path Process reports on stderr returns that error.
// A deadline kill is wrapped so callers can match it with
// errors.Is(err, context.DeadlineExceeded), which the exported (string, bool)
// contract cannot express: there, "the machine was too slow to spawn the
// processor" and "the processor declined to rewrite" are the same value.
func process(command string) (string, bool, error) {
	procCmd := os.Getenv(envKey)
	if procCmd == "" {
		return command, false, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	parts := strings.Fields(procCmd)
	args := append(parts[1:], command)
	cmd := exec.CommandContext(ctx, parts[0], args...)

	out, err := cmd.Output()
	if err != nil {
		// Checked before the exit-code branch: a killed process reports exit
		// code -1, but ctx.Err() is the only signal that names the CAUSE. The
		// bare error reads "signal: killed", which says nothing about why.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return command, false, fmt.Errorf("%w after %s: %w", ctxErr, timeout, err)
		}
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 {
			return command, false, nil
		}
		return command, false, err
	}

	rewritten := strings.TrimSpace(string(out))
	if rewritten == "" {
		return command, false, nil
	}

	return rewritten, true, nil
}
