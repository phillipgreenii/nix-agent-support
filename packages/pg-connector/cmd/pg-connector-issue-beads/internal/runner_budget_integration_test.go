//go:build integration

// Cross-process test of the bd budget through CLIRunner.Run: the subject is a
// real backend process that is SIGKILLed while its bd child (a fake bd shell
// script) is still running. It re-executes the test binary and runs an external
// program, so it is integration tier (run with -tags integration).

package internal

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-beads/internal/budget"
)

// budgetHelperEnv marks the re-executed test binary that stands in for the
// backend process: it runs one bd call through a budgeted CLIRunner.
const budgetHelperEnv = "PG_ISSUE_BEADS_BUDGET_HELPER_DIR"

// TestHelperBudgetedRunnerProcess is not a test: it is the backend stand-in.
func TestHelperBudgetedRunnerProcess(t *testing.T) {
	stateDir := os.Getenv(budgetHelperEnv)
	if stateDir == "" {
		t.Skip("helper process only")
	}
	b, err := budget.New(budget.Config{MaxConcurrent: 1, StateDir: stateDir, WaitBound: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	r := &CLIRunner{Dir: os.Getenv("FAKE_BD_STARTED"), Budget: b}
	// Blocks until the fake bd exits; the parent kills this process first.
	_, _ = r.Run(context.Background(), "show", "--json")
}

// Case 3 (end to end): SIGKILL of the backend process leaves its slot held
// until the fake bd it started exits.
func TestCLIRunner_Budget_SlotHeldAfterBackendSIGKILL(t *testing.T) {
	started := fakeBD(t)
	t.Setenv("FAKE_BD_SLEEP", "3")
	stateDir := t.TempDir()

	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	helper := exec.Command(exe, "-test.run=^TestHelperBudgetedRunnerProcess$")
	helper.Env = append(os.Environ(), budgetHelperEnv+"="+stateDir)
	if err := helper.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = helper.Process.Kill(); _ = helper.Wait() })
	waitStarted(t, started, 1)
	begin := time.Now()
	if err := helper.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = helper.Wait()

	// The backend is dead but its fake bd is still sleeping: the slot is held.
	// Observe it with a second Acquire that waits only briefly.
	probe, err := budget.New(budget.Config{MaxConcurrent: 1, StateDir: stateDir, WaitBound: 200 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := probe.Acquire(context.Background(), false); !errors.Is(err, budget.ErrDenied) {
		t.Fatalf("after SIGKILL with bd alive: err = %v, want ErrDenied", err)
	}

	waiter, err := budget.New(budget.Config{MaxConcurrent: 1, StateDir: stateDir, WaitBound: 15 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	slot, err := waiter.Acquire(context.Background(), false)
	if err != nil {
		t.Fatalf("after the fake bd exited: %v", err)
	}
	defer slot.Release()
	if slot.File() == nil {
		t.Fatalf("slot failed open: %v", slot.FailOpenErr())
	}
	if time.Since(begin) < 2*time.Second {
		t.Errorf("slot freed after %v, before the 3s fake bd could have exited", time.Since(begin))
	}
}
