package executor

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"testing"

	"github.com/phillipgreenii/pr-pool/internal/discover"
	"github.com/phillipgreenii/pr-pool/internal/item"
	"github.com/phillipgreenii/pr-pool/internal/roles"
)

// fakeExitCmd is a query.Commander test double (Deps.Cmd) that returns a
// fixed error from every call — used here to hand commandRun.run a genuine
// *exec.ExitError, per the design's "test doubles fabricate an *exec.ExitError"
// (Task 2.3, pg2-84o3m.22 Step 2.3.1).
type fakeExitCmd struct{ err error }

func (f fakeExitCmd) Run(_ context.Context, _ []string) ([]byte, error) { return nil, f.err }

// fabricateExitError runs a trivial subprocess that exits with code so the
// test gets back a REAL *exec.ExitError. os/exec.ExitError has no exported
// constructor and no exported exit-status field, so a genuine short-lived
// process is the only portable way to produce one in a test (as opposed to a
// hand-rolled type merely satisfying an ExitCode() interface).
func fabricateExitError(t *testing.T, code int) *exec.ExitError {
	t.Helper()
	err := exec.Command("/bin/sh", "-c", fmt.Sprintf("exit %d", code)).Run()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("fabricateExitError(%d): sh -c did not produce *exec.ExitError; err=%v", code, err)
	}
	return exitErr
}

func commandRole() roles.Role {
	return roles.Role{Name: "cmdrole", Type: "command", Command: &roles.CommandConfig{Argv: []string{"noop"}}}
}

// TestCommandDispatch_ExitCode9MapsToErrBusy is Task 2.3's required RED test
// (Step 2.3.1): a command role whose backing command exits 9 must surface
// ErrBusy through Executor.Dispatch, resolvable by errors.Is/errors.As
// through the existing %w chain, while the original *exec.ExitError (code 9)
// stays reachable too.
func TestCommandDispatch_ExitCode9MapsToErrBusy(t *testing.T) {
	exitErr := fabricateExitError(t, 9)
	d := discover.DispatchContext{Role: commandRole(), Item: item.Item{ID: "b1"}}
	deps := Deps{Cmd: fakeExitCmd{err: exitErr}}

	_, err := commandExecutor{}.Dispatch(context.Background(), d, deps)
	if err == nil {
		t.Fatal("exit code 9 must produce an error")
	}
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("errors.Is(err, ErrBusy) = false through Executor.Dispatch; err = %v", err)
	}
	var gotExit *exec.ExitError
	if !errors.As(err, &gotExit) || gotExit.ExitCode() != 9 {
		t.Fatalf("errors.As did not resolve the original *exec.ExitError (code 9); err = %v", err)
	}
}

// TestCommandDispatch_OtherExitCodeDoesNotMapToErrBusy guards against a
// classifier that maps EVERY non-zero exit to ErrBusy rather than only code 9
// — a plain failing command role must still surface as an ordinary error.
func TestCommandDispatch_OtherExitCodeDoesNotMapToErrBusy(t *testing.T) {
	exitErr := fabricateExitError(t, 1)
	d := discover.DispatchContext{Role: commandRole(), Item: item.Item{ID: "b1"}}
	deps := Deps{Cmd: fakeExitCmd{err: exitErr}}

	_, err := commandExecutor{}.Dispatch(context.Background(), d, deps)
	if err == nil {
		t.Fatal("exit code 1 must still produce an error")
	}
	if errors.Is(err, ErrBusy) {
		t.Fatalf("errors.Is(err, ErrBusy) = true for a non-9 exit code; err = %v", err)
	}
}
