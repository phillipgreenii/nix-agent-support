package inputproc

import (
	"context"
	"errors"
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

	// waitGrace bounds the one delay the deadline cannot reach: a process the
	// processor forked that OUTLIVES it still holding the output pipe, and that
	// has also left the process group isolateProcessGroup kills (only a
	// deliberate setsid/setpgid does that). Without it cmd.Output() reads to an
	// EOF that arrives when the last holder of the write end exits, which is not
	// a bounded event. Small on purpose: the wall clock Process guarantees is
	// timeout+waitGrace, so this is what the shipped 3s promise is rounded up by,
	// and a rewrite is one line of text — a pipe already at EOF drains in
	// microseconds, so there is nothing here for a longer grace to buy.
	waitGrace = 250 * time.Millisecond
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

	// The deadline alone bounds only the direct child, not this function: a
	// process the processor forks inherits the stdout write end, and cmd.Output()
	// reads to an EOF the killed child cannot deliver on its own. Measured 30.25s
	// against a 300ms deadline before these two lines (pg2-15uhy). Together they
	// make the wall clock Process spends at most timeout+waitGrace.
	isolateProcessGroup(cmd)
	cmd.WaitDelay = waitGrace

	out, err := cmd.Output()
	if err != nil {
		// Checked before the exit-code branch: a killed process reports exit
		// code -1, but ctx.Err() is the only signal that names the CAUSE. The
		// bare error reads "signal: killed", which says nothing about why.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return command, false, fmt.Errorf("%w after %s: %w", ctxErr, timeout, err)
		}
		// The processor finished inside its budget and something it forked was
		// still holding the pipe when waitGrace expired, so the read was cut
		// short. Deliberately NOT treated as a rewrite: what arrived may be a
		// PREFIX of what the processor meant to say, and running a truncated
		// rewrite is running a command it never approved. Declining degrades to
		// the original command, exactly as a deadline kill does. The forked
		// holder is killed rather than left behind — a leak per gated Bash tool
		// call is not an acceptable price for a bounded read.
		if errors.Is(err, exec.ErrWaitDelay) {
			reapProcessGroup(cmd)
			return command, false, fmt.Errorf("a process the input processor forked still held its output pipe %s after it exited; rewrite discarded as possibly truncated: %w", waitGrace, err)
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
