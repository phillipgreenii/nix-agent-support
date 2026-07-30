package inputproc

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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
func failIfDeadlineKill(t *testing.T, err error) {
	t.Helper()
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("input processor exec was killed by the %s test deadline: the environment could not spawn the mock in time, which is NOT a logic failure: %v", timeout, err)
	}
}

// runProcess is how every test below calls the package, including the ones that
// never spawn anything, so the guard is the default rather than something each
// new test must remember to opt into.
func runProcess(t *testing.T, command string) (string, bool) {
	t.Helper()
	rewritten, changed, err := process(command)
	failIfDeadlineKill(t, err)
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
	t.Setenv(envKey, "/usr/bin/true")
	if !Configured() {
		t.Error("Configured() = false, want true when env var is set")
	}
}

func TestProcess_Exit0_Rewrites(t *testing.T) {
	script := writeMockProcessor(t, "rewriter", `echo "wrapped $1"`)
	t.Setenv(envKey, script)

	rewritten, changed := runProcess(t, "git status")
	if !changed {
		t.Fatal("Process() changed = false, want true")
	}
	if rewritten != "wrapped git status" {
		t.Errorf("Process() = %q, want %q", rewritten, "wrapped git status")
	}
}

func TestProcess_Exit1_NoRewrite(t *testing.T) {
	script := writeMockProcessor(t, "noop", "exit 1")
	t.Setenv(envKey, script)

	rewritten, changed := runProcess(t, "git status")
	if changed {
		t.Error("Process() changed = true, want false for exit 1")
	}
	if rewritten != "git status" {
		t.Errorf("Process() = %q, want %q", rewritten, "git status")
	}
}

func TestProcess_Exit2_NoRewrite(t *testing.T) {
	script := writeMockProcessor(t, "error", "exit 2")
	t.Setenv(envKey, script)

	rewritten, changed := runProcess(t, "git status")
	if changed {
		t.Error("Process() changed = true, want false for exit 2")
	}
	if rewritten != "git status" {
		t.Errorf("Process() = %q, want %q", rewritten, "git status")
	}
}

func TestProcess_EmptyStdout_NoRewrite(t *testing.T) {
	script := writeMockProcessor(t, "empty", `echo ""`)
	t.Setenv(envKey, script)

	rewritten, changed := runProcess(t, "git status")
	if changed {
		t.Error("Process() changed = true, want false for empty stdout")
	}
	if rewritten != "git status" {
		t.Errorf("Process() = %q, want %q", rewritten, "git status")
	}
}

func TestProcess_CommandNotFound_NoRewrite(t *testing.T) {
	t.Setenv(envKey, "/nonexistent/binary")

	rewritten, changed := runProcess(t, "git status")
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
	t.Setenv(envKey, script+" rewrite")

	rewritten, changed := runProcess(t, "git status")
	if !changed {
		t.Fatal("Process() changed = false, want true for multi-word command")
	}
	if rewritten != "wrapped git status" {
		t.Errorf("Process() = %q, want %q", rewritten, "wrapped git status")
	}
}

func TestProcess_NotConfigured_NoRewrite(t *testing.T) {
	t.Setenv(envKey, "")

	rewritten, changed := runProcess(t, "git status")
	if changed {
		t.Error("Process() changed = true, want false when not configured")
	}
	if rewritten != "git status" {
		t.Errorf("Process() = %q, want %q", rewritten, "git status")
	}
}

// TestProcess_DeadlineKill_IsDistinguishable is the other half of the fix: it
// asserts a killed exec is REPORTABLE as a deadline, not just as changed=false.
// It is load-proof — whether the deadline elapses during the spawn or during the
// sleep, the observable outcome is the same — so it cannot become the next flake.
func TestProcess_DeadlineKill_IsDistinguishable(t *testing.T) {
	withTimeout(t, 50*time.Millisecond)
	script := writeMockProcessor(t, "slow", `sleep 30; echo "wrapped $1"`)
	t.Setenv(envKey, script)

	rewritten, changed, err := process("git status")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("process() err = %v, want an error matching context.DeadlineExceeded", err)
	}
	if changed {
		t.Error("process() changed = true, want false when the exec is killed")
	}
	if rewritten != "git status" {
		t.Errorf("process() = %q, want the original %q", rewritten, "git status")
	}
}

// TestProcess_PublicContract_Unchanged covers the exported wrapper itself: the
// tests above call process, so without this the (string, bool) shape main.go
// depends on would be untested. Each subtest classifies the same input through
// process as well, because Process alone cannot say whether an unexpected
// changed=false was the contract breaking or the exec being killed.
func TestProcess_PublicContract_Unchanged(t *testing.T) {
	t.Run("rewrite", func(t *testing.T) {
		script := writeMockProcessor(t, "rewriter", `echo "wrapped $1"`)
		t.Setenv(envKey, script)

		rewritten, changed := Process("git status")
		_, _, err := process("git status")
		failIfDeadlineKill(t, err)
		if !changed || rewritten != "wrapped git status" {
			t.Errorf("Process() = (%q, %v), want (%q, true)", rewritten, changed, "wrapped git status")
		}
	})

	t.Run("decline", func(t *testing.T) {
		script := writeMockProcessor(t, "noop", "exit 1")
		t.Setenv(envKey, script)

		rewritten, changed := Process("git status")
		_, _, err := process("git status")
		failIfDeadlineKill(t, err)
		if changed || rewritten != "git status" {
			t.Errorf("Process() = (%q, %v), want (%q, false)", rewritten, changed, "git status")
		}
	})
}
