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
	// envKey holds the ORDERED list of input-processor commands, one per line
	// (bead tc-7m85u item 1). A `:`-separated list was rejected: a processor
	// command commonly contains spaces (its own argv) and, less commonly, a
	// literal `:` (a path, a URL), either of which a `:` splitter cannot tell
	// from a list separator. Newline was chosen over a JSON array: the HM
	// module already renders every other list-valued env var here with
	// lib.concatStringsSep (see CETA_EXTRA_READWRITE_ROOTS/
	// CETA_EXTRA_READONLY_ROOTS/CETA_DENIED_ROOTS in
	// home/programs/claude-extended-tool-approver/default.nix, all `:`-joined
	// because none of THEIR elements can contain a `:`) and a processor
	// command's own argv already tolerates embedded whitespace by construction
	// (strings.Fields splits it below) — a literal newline inside one
	// processor's command string is not a realistic shape, so it does not need
	// the extra escaping machinery JSON would require on both the nix and the
	// Go side. Empty lines (a trailing separator, blank config) are skipped.
	envKey = "CETA_INPUT_PROCESSORS"

	// defaultTimeout is the budget the SHIPPED input processor gets for one
	// rewrite. It has not changed; the test suite widens the deadline it
	// installs instead of widening this (see inputproc_test.go's TestMain). It
	// is a PER-PROCESSOR budget (bead tc-7m85u item 1): a chain of N
	// processors can cost up to N times this, not one shared budget split
	// across the chain — the alternative (a shared chain-wide budget) would
	// make an early processor's slowness silently starve a later one of time
	// it never got to spend, which is a worse failure mode than a slower
	// worst-case chain.
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

// payloadEnvKeys are the four CETA_* variables ceta exports into every
// processor's environment (bead tc-7m85u item 2), taken from the hook payload
// ceta already parsed — nothing else from the payload, and no other channel:
// add a fifth only when a real consumer needs it. Listed once so
// payloadEnviron can strip any ambient value carrying one of these exact
// names before appending ceta's own, mirroring hermeticGitEnviron's GIT_
// prefix strip in internal/pathspec/worktree.go (exact-name match here, one
// narrower than a prefix family, since these four names are not a family).
var payloadEnvKeys = []string{"CETA_SESSION_ID", "CETA_AGENT_ID", "CETA_AGENT_TYPE", "CETA_CWD"}

// Payload is the subset of the hook's parsed input (hookio.HookInput) that is
// exported into a processor's environment. It is a plain struct rather than
// this package importing hookio directly: inputproc is a leaf utility with no
// other dependency on the hook wire format, and every field here is already a
// bare string on HookInput, so main.go's call site does the trivial mapping
// instead of this package reaching back up for the type.
//
// SessionID and CWD are the session's own identity and come from every hook
// payload. AgentID and AgentType are EMPTY (not merely unset — see
// payloadEnviron) for the main session; only a subagent invocation populates
// them. A processor MUST treat an empty AgentID/AgentType as "the main
// session", never as "unknown agent".
type Payload struct {
	SessionID string
	AgentID   string
	AgentType string
	CWD       string
}

// payloadEnviron builds the environment for one processor invocation: the
// ambient environment, with any existing value for one of payloadEnvKeys
// stripped, plus ceta's own four values appended. Appending unconditionally —
// rather than only when a field is non-empty — is what makes CETA_AGENT_ID and
// CETA_AGENT_TYPE PRESENT-BUT-EMPTY on a main-session payload instead of
// simply absent, which main.go's PreToolUse handler and the option docs both
// promise.
func payloadEnviron(payload Payload) []string {
	base := os.Environ()
	env := make([]string, 0, len(base)+len(payloadEnvKeys))
	for _, kv := range base {
		keep := true
		for _, k := range payloadEnvKeys {
			if strings.HasPrefix(kv, k+"=") {
				keep = false
				break
			}
		}
		if keep {
			env = append(env, kv)
		}
	}
	return append(
		env,
		"CETA_SESSION_ID="+payload.SessionID,
		"CETA_AGENT_ID="+payload.AgentID,
		"CETA_AGENT_TYPE="+payload.AgentType,
		"CETA_CWD="+payload.CWD,
	)
}

// processorList returns the configured input-processor commands in order, or
// nil if none are configured. Blank lines are dropped so a trailing separator
// or blank entry in the nix-rendered list does not become a phantom processor
// whose "command" is the empty string.
func processorList() []string {
	raw := os.Getenv(envKey)
	if raw == "" {
		return nil
	}
	var out []string
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

// Configured returns true if at least one input processor command is set.
func Configured() bool {
	return len(processorList()) > 0
}

// Process runs the configured input processors, IN ORDER, over command —
// each one receiving the PREVIOUS processor's output as its own command
// argument (bead tc-7m85u item 1) — and returns the FINAL text plus whether it
// differs from the original. Every per-processor error (a deadline kill, a
// truncated-rewrite discard, or any other exec failure) is reported on
// stderr, exactly as a single processor's error was before this list existed;
// it does not stop the chain, which continues with the prior text (see
// processChain).
func Process(command string, payload Payload) (string, bool) {
	rewritten, changed, errs := processChain(command, payload)
	for _, err := range errs {
		fmt.Fprintf(os.Stderr, "claude-extended-tool-approver: input processor: %v\n", err)
	}
	return rewritten, changed
}

// processChain is Process with every per-processor error retained, instead of
// only reported and discarded, so tests can assert on WHICH processor failed
// and WHY without scraping stderr. A processor's decline (exit 1, empty
// stdout) or its exec failing outright (deadline kill, truncated-rewrite
// discard, any other error) both leave `current` unchanged and move on to the
// next processor — the "pass the command through unchanged" behavior the
// bead's acceptance criteria describe for a decline applies identically to a
// skip. `changed` is computed once, at the end, by comparing the final text
// to the original: an intermediate processor rewriting and a LATER one
// rewriting it back to the original is therefore correctly reported as
// unchanged.
func processChain(command string, payload Payload) (string, bool, []error) {
	processors := processorList()
	if len(processors) == 0 {
		return command, false, nil
	}

	current := command
	var errs []error
	for _, procCmd := range processors {
		rewritten, changed, err := runOneProcessor(procCmd, current, payload)
		if err != nil {
			errs = append(errs, err)
		}
		if changed {
			current = rewritten
		}
	}
	return current, current != command, errs
}

// runOneProcessor calls ONE configured input processor with the given
// command. Returns the rewritten command and true if the processor rewrote
// it, or the original command and false if no rewrite occurred (a decline, or
// a failure this processor's own budget could not absorb).
//
// Every path treated as an ordinary "no rewrite" — exit 1, empty stdout —
// returns a nil error; every path reported on stderr returns that error. A
// deadline kill is wrapped so callers can match it with
// errors.Is(err, context.DeadlineExceeded), which the (string, bool) contract
// cannot express on its own: there, "the machine was too slow to spawn the
// processor" and "the processor declined to rewrite" are the same value. This
// was internal/inputproc's whole `process` function before the ordered list
// (bead tc-7m85u item 1) required it to run once per configured processor
// instead of once per hook invocation; its per-invocation behavior —
// including the process-group isolation and the WaitDelay backstop — is
// unchanged.
func runOneProcessor(procCmd, command string, payload Payload) (string, bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	parts := strings.Fields(procCmd)
	if len(parts) == 0 {
		// processorList already trims/drops blank entries, so this is
		// defensive rather than reachable — but a command that is all
		// whitespace inside a non-blank line (unlikely, still possible)
		// must decline rather than panic on parts[0] below.
		return command, false, nil
	}
	args := append(parts[1:], command)
	cmd := exec.CommandContext(ctx, parts[0], args...)
	cmd.Env = payloadEnviron(payload)

	// The deadline alone bounds only the direct child, not this function: a
	// process the processor forks inherits the stdout write end, and cmd.Output()
	// reads to an EOF the killed child cannot deliver on its own. Measured 30.25s
	// against a 300ms deadline before these two lines (pg2-15uhy). Together they
	// make the wall clock this call spends at most timeout+waitGrace.
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
		// the prior text, exactly as a deadline kill does. The forked holder is
		// killed rather than left behind — a leak per gated Bash tool call is
		// not an acceptable price for a bounded read.
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
