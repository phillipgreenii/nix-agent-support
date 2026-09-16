package inputproc

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// testTimeout is the exec deadline the whole package runs under, in place of the
// shipped defaultTimeout. The mock processor spawns in single-digit milliseconds
// on an idle machine, so this is a ~10,000x margin: it is here to absorb a
// fork+exec that loses the CPU for seconds inside the nix build sandbox, not to
// let the processor do slow work. It stays FINITE so a processor that genuinely
// never returns still fails here, rather than wedging the package until `go
// test`'s 10-minute panic.
const testTimeout = 60 * time.Second

// forkStallBound is the wall clock the forking-mock tests below must return
// within. It sits deliberately far from BOTH outcomes it discriminates, so it is
// a verdict about the code and never about the machine: with the defect present
// Process() parked for the mock's whole `sleep 30` (measured 30.25s against a
// 300ms deadline), while the bounded path costs the deadline plus waitGrace —
// under 600ms for every mock here. Scheduling latency cannot stretch 600ms to 5s,
// and no machine is fast enough to bring 30s under it.
const forkStallBound = 5 * time.Second

// mainSessionPayload is the Payload every test below uses unless it is
// specifically exercising the payload-env contract: a plausible session/cwd
// with agent fields left at their MAIN-SESSION zero value (empty string, not
// simply "not passed"), matching what main.go builds for a top-level
// HookInput.
var mainSessionPayload = Payload{
	SessionID: "sess-1",
	CWD:       "/work",
}

// TestMain installs testTimeout for the WHOLE package rather than per test, for
// the same reason cmd/claude-extended-tool-approver's TestMain isolates
// XDG_DATA_HOME package-wide: making it the default means a newly added test
// that spawns the mock cannot reintroduce the flake by forgetting to opt in.
// Mutating a package var is safe here because these tests call t.Setenv, which
// already forbids t.Parallel.
func TestMain(m *testing.M) {
	timeout = testTimeout
	os.Exit(m.Run())
}

// withTimeout narrows the deadline for one test and restores it afterwards.
func withTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	prev := timeout
	timeout = d
	t.Cleanup(func() { timeout = prev })
}

// failIfDeadlineKill turns a killed exec into a diagnosis instead of a verdict
// about the code. Without it a deadline kill is read as whichever boolean the
// test was checking: the tests wanting changed=true fail with a message blaming
// the LOGIC, and — worse — the tests wanting changed=false PASS for the wrong
// reason, so a broken processor could go unnoticed. It Fatals rather than Skips
// so a processor that hangs forever is still a failure.
func failIfDeadlineKill(t *testing.T, errs []error) {
	t.Helper()
	for _, err := range errs {
		if errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("input processor exec was killed by the %s test deadline: the environment could not spawn the mock in time, which is NOT a logic failure: %v", timeout, err)
		}
	}
}

// setProcessors installs the ordered processor list for one test, joining the
// given commands with the newline separator processorList expects.
func setProcessors(t *testing.T, commands ...string) {
	t.Helper()
	t.Setenv(envKey, strings.Join(commands, "\n"))
}

// runChain is how every test below calls the package's chain entry point,
// including the ones that never spawn anything, so the deadline-kill guard is
// the default rather than something each new test must remember to opt into.
func runChain(t *testing.T, command string, payload Payload) (string, bool) {
	t.Helper()
	rewritten, changed, errs := processChain(command, payload)
	failIfDeadlineKill(t, errs)
	return rewritten, changed
}

func writeMockProcessor(t *testing.T, name, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestDefaultTimeout_Unchanged pins the SHIPPED budget. The suite runs with a
// widened deadline, so nothing else here would notice defaultTimeout drifting;
// this is the tripwire that keeps "make the flaky test pass" from silently
// becoming "give production a bigger budget". Changing the shipped value is
// allowed — it just has to be deliberate enough to edit this test.
func TestDefaultTimeout_Unchanged(t *testing.T) {
	if defaultTimeout != 3*time.Second {
		t.Errorf("defaultTimeout = %v, want 3s: the shipped input-processor budget must not be widened to accommodate a slow test environment", defaultTimeout)
	}
}

func TestConfigured_Unset(t *testing.T) {
	t.Setenv(envKey, "")
	if Configured() {
		t.Error("Configured() = true, want false when env var is empty")
	}
}

func TestConfigured_Set(t *testing.T) {
	setProcessors(t, "/usr/bin/true")
	if !Configured() {
		t.Error("Configured() = false, want true when env var is set")
	}
}

// TestConfigured_BlankLinesOnly proves a list of nothing but whitespace/blank
// lines (a stray trailing separator, say) is indistinguishable from unset —
// processorList must drop them rather than manufacture phantom entries.
func TestConfigured_BlankLinesOnly(t *testing.T) {
	t.Setenv(envKey, "\n  \n\n")
	if Configured() {
		t.Error("Configured() = true, want false when the list contains only blank lines")
	}
}

func TestProcess_Exit0_Rewrites(t *testing.T) {
	script := writeMockProcessor(t, "rewriter", `echo "wrapped $1"`)
	setProcessors(t, script)

	rewritten, changed := runChain(t, "git status", mainSessionPayload)
	if !changed {
		t.Fatal("Process() changed = false, want true")
	}
	if rewritten != "wrapped git status" {
		t.Errorf("Process() = %q, want %q", rewritten, "wrapped git status")
	}
}

func TestProcess_Exit1_NoRewrite(t *testing.T) {
	script := writeMockProcessor(t, "noop", "exit 1")
	setProcessors(t, script)

	rewritten, changed := runChain(t, "git status", mainSessionPayload)
	if changed {
		t.Error("Process() changed = true, want false for exit 1")
	}
	if rewritten != "git status" {
		t.Errorf("Process() = %q, want %q", rewritten, "git status")
	}
}

func TestProcess_Exit2_NoRewrite(t *testing.T) {
	script := writeMockProcessor(t, "error", "exit 2")
	setProcessors(t, script)

	rewritten, changed := runChain(t, "git status", mainSessionPayload)
	if changed {
		t.Error("Process() changed = true, want false for exit 2")
	}
	if rewritten != "git status" {
		t.Errorf("Process() = %q, want %q", rewritten, "git status")
	}
}

func TestProcess_EmptyStdout_NoRewrite(t *testing.T) {
	script := writeMockProcessor(t, "empty", `echo ""`)
	setProcessors(t, script)

	rewritten, changed := runChain(t, "git status", mainSessionPayload)
	if changed {
		t.Error("Process() changed = true, want false for empty stdout")
	}
	if rewritten != "git status" {
		t.Errorf("Process() = %q, want %q", rewritten, "git status")
	}
}

func TestProcess_CommandNotFound_NoRewrite(t *testing.T) {
	setProcessors(t, "/nonexistent/binary")

	rewritten, changed := runChain(t, "git status", mainSessionPayload)
	if changed {
		t.Error("Process() changed = true, want false for missing command")
	}
	if rewritten != "git status" {
		t.Errorf("Process() = %q, want %q", rewritten, "git status")
	}
}

func TestProcess_MultiWordCommand(t *testing.T) {
	script := writeMockProcessor(t, "multi", `
if [ "$1" = "rewrite" ]; then
    echo "wrapped $2"
else
    exit 1
fi`)
	setProcessors(t, script+" rewrite")

	rewritten, changed := runChain(t, "git status", mainSessionPayload)
	if !changed {
		t.Fatal("Process() changed = false, want true for multi-word command")
	}
	if rewritten != "wrapped git status" {
		t.Errorf("Process() = %q, want %q", rewritten, "wrapped git status")
	}
}

func TestProcess_NotConfigured_NoRewrite(t *testing.T) {
	t.Setenv(envKey, "")

	rewritten, changed := runChain(t, "git status", mainSessionPayload)
	if changed {
		t.Error("Process() changed = true, want false when not configured")
	}
	if rewritten != "git status" {
		t.Errorf("Process() = %q, want %q", rewritten, "git status")
	}
}

// TestChain_TwoProcessors_OrderAndComposition is the bead's headline acceptance
// criterion: two processors run in order and the second sees the first's
// output, not the original command.
func TestChain_TwoProcessors_OrderAndComposition(t *testing.T) {
	first := writeMockProcessor(t, "first", `echo "A($1)"`)
	second := writeMockProcessor(t, "second", `echo "B($1)"`)
	setProcessors(t, first, second)

	rewritten, changed := runChain(t, "git status", mainSessionPayload)
	if !changed {
		t.Fatal("Process() changed = false, want true")
	}
	want := "B(A(git status))"
	if rewritten != want {
		t.Errorf("Process() = %q, want %q — the second processor must see the first's output, not the original command", rewritten, want)
	}
}

// TestChain_DecliningMiddlePassesThrough puts a declining processor between
// two rewriting ones: the middle processor's exit 1 must not clear the first
// processor's rewrite, and the third processor must see that rewrite (proof
// the pass-through carries the PRIOR text forward, not the original).
func TestChain_DecliningMiddlePassesThrough(t *testing.T) {
	first := writeMockProcessor(t, "first", `echo "wrapped $1"`)
	middle := writeMockProcessor(t, "middle", "exit 1")
	third := writeMockProcessor(t, "third", `echo "[$1]"`)
	setProcessors(t, first, middle, third)

	rewritten, changed := runChain(t, "git status", mainSessionPayload)
	if !changed {
		t.Fatal("Process() changed = false, want true")
	}
	want := "[wrapped git status]"
	if rewritten != want {
		t.Errorf("Process() = %q, want %q — a declining processor must pass the PRIOR text through unchanged to the next processor", rewritten, want)
	}
}

// TestChain_TimedOutProcessorSkipped_ContinuesWithPriorText is the bead's other
// headline criterion: a processor whose exec is killed by the deadline does
// not abort the chain — it is skipped (treated as a decline) and the chain
// continues with the text as it stood before that processor ran.
func TestChain_TimedOutProcessorSkipped_ContinuesWithPriorText(t *testing.T) {
	withTimeout(t, 100*time.Millisecond)
	slow := writeMockProcessor(t, "slow", `sleep 30; echo "should never appear $1"`)
	second := writeMockProcessor(t, "second", `echo "wrapped $1"`)
	setProcessors(t, slow, second)

	rewritten, changed, errs := processChain("git status", mainSessionPayload)
	if len(errs) != 1 {
		t.Fatalf("processChain() errs = %v, want exactly 1 (the timed-out processor)", errs)
	}
	if !errors.Is(errs[0], context.DeadlineExceeded) {
		t.Errorf("processChain() errs[0] = %v, want a context.DeadlineExceeded match", errs[0])
	}
	if !changed {
		t.Fatal("Process() changed = false, want true: the second processor still ran and rewrote the prior (original) text")
	}
	want := "wrapped git status"
	if rewritten != want {
		t.Errorf("Process() = %q, want %q — the chain must continue with the PRIOR text (the original command, since the first processor never rewrote it) rather than stopping at the timeout", rewritten, want)
	}
}

// TestPayloadEnv_PresentForMainSession_AgentFieldsEmptyNotUnset is the bead's
// third headline criterion: all four CETA_* vars are present in a processor's
// environment with the payload's values, and — critically — AgentID/AgentType
// are EMPTY STRINGS rather than absent for a main-session payload. The mock
// distinguishes "empty" from "unset" with `${VAR+set}`: a set-but-empty var
// expands to "set", an unset one to "".
func TestPayloadEnv_PresentForMainSession_AgentFieldsEmptyNotUnset(t *testing.T) {
	script := writeMockProcessor(t, "envdump", `printf '%s|%s|%s|%s|%s|%s\n' "$CETA_SESSION_ID" "${CETA_AGENT_ID+set}" "$CETA_AGENT_ID" "${CETA_AGENT_TYPE+set}" "$CETA_AGENT_TYPE" "$CETA_CWD"`)
	setProcessors(t, script)

	payload := Payload{SessionID: "sess-42", CWD: "/home/tcadmin/work"}
	rewritten, changed := runChain(t, "git status", payload)
	if !changed {
		t.Fatalf("Process() changed = false, want true; the mock always emits a non-empty rewrite")
	}
	want := "sess-42|set||set||/home/tcadmin/work"
	if rewritten != want {
		t.Errorf("processor environment = %q, want %q (session_id|agent_id-is-set|agent_id|agent_type-is-set|agent_type|cwd)", rewritten, want)
	}
}

// TestPayloadEnv_AgentFieldsPopulated_ForSubagentPayload is
// TestPayloadEnv_PresentForMainSession_AgentFieldsEmptyNotUnset's mirror: a
// subagent payload's non-empty AgentID/AgentType values must reach the
// processor's environment verbatim.
func TestPayloadEnv_AgentFieldsPopulated_ForSubagentPayload(t *testing.T) {
	script := writeMockProcessor(t, "envdump", `printf '%s|%s|%s|%s\n' "$CETA_SESSION_ID" "$CETA_AGENT_ID" "$CETA_AGENT_TYPE" "$CETA_CWD"`)
	setProcessors(t, script)

	payload := Payload{SessionID: "sess-42", AgentID: "agent-7", AgentType: "general-purpose", CWD: "/home/tcadmin/work"}
	rewritten, changed := runChain(t, "git status", payload)
	if !changed {
		t.Fatalf("Process() changed = false, want true; the mock always emits a non-empty rewrite")
	}
	want := "sess-42|agent-7|general-purpose|/home/tcadmin/work"
	if rewritten != want {
		t.Errorf("processor environment = %q, want %q", rewritten, want)
	}
}

// TestProcess_DeadlineKill_IsDistinguishable is the other half of the fix: it
// asserts a killed exec is REPORTABLE as a deadline, not just as changed=false.
// It is load-proof — whether the deadline elapses during the spawn or during the
// sleep, the observable outcome is the same — so it cannot become the next flake.
// It says nothing about WHEN process() returns, and at 50ms it structurally
// cannot: the kill lands before /bin/sh can fork, which is why the wall clock is
// pinned by the two forking tests below and not here.
func TestProcess_DeadlineKill_IsDistinguishable(t *testing.T) {
	withTimeout(t, 50*time.Millisecond)
	script := writeMockProcessor(t, "slow", `sleep 30; echo "wrapped $1"`)
	setProcessors(t, script)

	rewritten, changed, errs := processChain("git status", mainSessionPayload)
	if len(errs) != 1 || !errors.Is(errs[0], context.DeadlineExceeded) {
		t.Fatalf("processChain() errs = %v, want exactly 1 error matching context.DeadlineExceeded", errs)
	}
	if changed {
		t.Error("processChain() changed = true, want false when the exec is killed")
	}
	if rewritten != "git status" {
		t.Errorf("processChain() = %q, want the original %q", rewritten, "git status")
	}
}

// TestProcess_ForkedGrandchild_DeadlineBoundsWallClock is the regression test for
// the defect the deadline did NOT buy (pg2-15uhy): the mock FORKS, so killing the
// direct child leaves a grandchild holding the inherited stdout write end, and
// cmd.Output() reads to an EOF that cannot arrive until that grandchild exits.
// Measured before the fix: 30.25s against a 300ms deadline, ~100x the budget.
//
// The 300ms deadline is chosen, not inherited: /bin/sh needs 20-180ms here to
// start and fork, so at the 50ms TestProcess_DeadlineKill_IsDistinguishable uses
// the kill lands BEFORE the fork and the stall cannot be reproduced at all
// (measured: 51ms, indistinguishable from a pass). A mock that forks after the
// deadline makes this test vacuous rather than flaky, which is why the
// deterministic half of the reproduction is the test below.
func TestProcess_ForkedGrandchild_DeadlineBoundsWallClock(t *testing.T) {
	withTimeout(t, 300*time.Millisecond)
	script := writeMockProcessor(t, "forker", "sleep 30 &\nwait")
	setProcessors(t, script)

	start := time.Now()
	rewritten, changed, errs := processChain("git status", mainSessionPayload)
	elapsed := time.Since(start)

	if elapsed > forkStallBound {
		t.Errorf("process() returned after %v, want under %v: the %v deadline killed the processor but a process it forked kept the output pipe open, so the budget bounded nothing", elapsed, forkStallBound, timeout)
	}
	if len(errs) != 1 || !errors.Is(errs[0], context.DeadlineExceeded) {
		t.Fatalf("processChain() errs = %v, want exactly 1 error matching context.DeadlineExceeded", errs)
	}
	if changed {
		t.Error("processChain() changed = true, want false when the exec is killed")
	}
	if rewritten != "git status" {
		t.Errorf("processChain() = %q, want the original %q", rewritten, "git status")
	}
}

// TestProcess_ForkedGrandchild_IsBoundedAndReaped is the deterministic half: the
// processor exits 0 WITHIN its budget and leaves a background process holding the
// output pipe, so the deadline is not what has to save us and there is no race
// with the fork — the mock's `echo` cannot run until after the `&`, and
// cmd.Output() cannot return until the direct child has exited. Measured before
// the fix: 30.21s under a 60s deadline that never fired, with the rewrite applied
// at the end.
//
// It pins both halves of the decision:
//   - BOUNDED, and fail-safe rather than best-effort — the rewrite is DISCARDED.
//     Whatever arrived before the pipe was force-closed may be a PREFIX of what
//     the processor meant to say, and a truncated rewrite is a different command
//     than the one it approved. Declining degrades to running the original, which
//     is the same degradation a deadline kill already has.
//   - REAPED: the forked process is killed, not abandoned. Asserted on the pid the
//     mock records, because "no leak" is otherwise invisible — the stall it caused
//     is gone either way.
func TestProcess_ForkedGrandchild_IsBoundedAndReaped(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "grandchild.pid")
	script := writeMockProcessor(t, "backgrounder", fmt.Sprintf("sleep 30 &\necho $! > %s\necho \"wrapped $1\"", pidFile))
	setProcessors(t, script)

	start := time.Now()
	rewritten, changed, errs := processChain("git status", mainSessionPayload)
	elapsed := time.Since(start)

	if elapsed > forkStallBound {
		t.Fatalf("process() returned after %v, want under %v: the processor exited within its budget but a process it forked kept the output pipe open, so nothing bounded the read", elapsed, forkStallBound)
	}
	if len(errs) != 1 {
		t.Fatalf("processChain() errs = %v, want exactly 1: a discarded rewrite must not be silent", errs)
	}
	if errors.Is(errs[0], context.DeadlineExceeded) {
		t.Errorf("processChain() errs[0] = %v, want an error that does NOT match context.DeadlineExceeded: the deadline never fired here, and cmd/claude-extended-tool-approver's runHook retries on that signature", errs[0])
	}
	if changed {
		t.Error("processChain() changed = true, want false: output read from a pipe closed under a forked holder may be truncated, and a truncated rewrite is a different command")
	}
	if rewritten != "git status" {
		t.Errorf("processChain() = %q, want the original %q", rewritten, "git status")
	}

	requireProcessGone(t, readPID(t, pidFile))
}

// readPID reads the pid the mock recorded. It is unconditional, not best-effort:
// the mock writes the file BEFORE the line that ends it, and process() cannot
// return until the direct child has exited, so an absent file means the mock did
// not run as written rather than that the test was unlucky.
func readPID(t *testing.T, path string) int {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("mock processor did not record the pid it forked, so nothing can be asserted about it: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatalf("mock processor recorded %q, want a pid: %v", raw, err)
	}
	return pid
}

// requireProcessGone asserts pid is neither alive nor a zombie, using kill(pid, 0)
// semantics: no signal is delivered, and ESRCH means the process is both dead and
// reaped. It POLLS because SIGKILL delivery and the reap of an orphan reparented
// to init are both asynchronous — so a single check would race the kill it is
// verifying. Cost is milliseconds when the process was killed and the full bound
// only when the assertion is about to fail anyway.
func requireProcessGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(forkStallBound)
	for {
		if errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("pid %d the processor forked is still present after %v: it was abandoned rather than killed, so every gated Bash tool call can leak one", pid, forkStallBound)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestProcess_PublicContract_Unchanged covers the exported wrapper itself: the
// tests above call processChain, so without this the (string, bool) shape
// main.go depends on would be untested. Each subtest classifies the same input
// through processChain as well, because Process alone cannot say whether an
// unexpected changed=false was the contract breaking or the exec being killed.
func TestProcess_PublicContract_Unchanged(t *testing.T) {
	t.Run("rewrite", func(t *testing.T) {
		script := writeMockProcessor(t, "rewriter", `echo "wrapped $1"`)
		setProcessors(t, script)

		rewritten, changed := Process("git status", mainSessionPayload)
		_, _, errs := processChain("git status", mainSessionPayload)
		failIfDeadlineKill(t, errs)
		if !changed || rewritten != "wrapped git status" {
			t.Errorf("Process() = (%q, %v), want (%q, true)", rewritten, changed, "wrapped git status")
		}
	})

	t.Run("decline", func(t *testing.T) {
		script := writeMockProcessor(t, "noop", "exit 1")
		setProcessors(t, script)

		rewritten, changed := Process("git status", mainSessionPayload)
		_, _, errs := processChain("git status", mainSessionPayload)
		failIfDeadlineKill(t, errs)
		if changed || rewritten != "git status" {
			t.Errorf("Process() = (%q, %v), want (%q, false)", rewritten, changed, "git status")
		}
	})
}
