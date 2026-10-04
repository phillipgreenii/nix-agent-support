package executor

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/item"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/query"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/report"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/roles"
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
	d := DispatchContext{Role: commandRole(), Item: item.Item{ID: "b1"}}
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
	d := DispatchContext{Role: commandRole(), Item: item.Item{ID: "b1"}}
	deps := Deps{Cmd: fakeExitCmd{err: exitErr}}

	_, err := commandExecutor{}.Dispatch(context.Background(), d, deps)
	if err == nil {
		t.Fatal("exit code 1 must still produce an error")
	}
	if errors.Is(err, ErrBusy) {
		t.Fatalf("errors.Is(err, ErrBusy) = true for a non-9 exit code; err = %v", err)
	}
}

// shCommandRole returns a command role whose argv is a /bin/sh script, so
// the test exercises the REAL default Commander (query.OSCommander) and the
// real *exec.ExitError.Stderr population, not a fabricated error.
func shCommandRole(script string) roles.Role {
	return roles.Role{Name: "cmdrole", Type: "command", Command: &roles.CommandConfig{Argv: []string{"/bin/sh", "-c", script}}}
}

// TestCommandDispatch_FailureIncludesStderrTail is pg2-rzcfr's acceptance: a
// command role that writes to stderr and exits non-zero surfaces that stderr
// in the returned error (and so the dispatch WARN line), while the exit code
// stays reachable through errors.As.
func TestCommandDispatch_FailureIncludesStderrTail(t *testing.T) {
	d := DispatchContext{Role: shCommandRole(`printf 'boom: stage=fetch\nsecond line\n' >&2; echo stdout-noise; exit 3`), Item: item.Item{ID: "b1"}}
	deps := Deps{Cmd: query.OSCommander{}}

	_, err := commandExecutor{}.Dispatch(context.Background(), d, deps)
	if err == nil {
		t.Fatal("exit 3 must produce an error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "boom: stage=fetch | second line") {
		t.Fatalf("error lacks the single-line stderr tail; err = %q", msg)
	}
	if !strings.Contains(msg, "command role \"cmdrole\" item b1: exit status 3") {
		t.Fatalf("error lost the existing prefix / exit status; err = %q", msg)
	}
	if strings.ContainsAny(msg, "\n\r") {
		t.Fatalf("error must stay single-line; err = %q", msg)
	}
	if strings.Contains(msg, "stdout-noise") {
		t.Fatalf("stdout must not leak into the error; err = %q", msg)
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 3 {
		t.Fatalf("errors.As did not resolve the exit code 3; err = %v", err)
	}
}

// TestCommandDispatch_StderrTailIsBounded feeds far more stderr than any bound
// (well over exec.Output's own 32 KiB retention) and requires the error to carry
// only the LAST commandStderrTailMax bytes.
func TestCommandDispatch_StderrTailIsBounded(t *testing.T) {
	script := `i=0; while [ $i -lt 4000 ]; do echo "noise-line-$i-xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx" >&2; i=$((i+1)); done; echo FINAL-REASON >&2; exit 1`
	d := DispatchContext{Role: shCommandRole(script), Item: item.Item{ID: "b1"}}

	_, err := commandExecutor{}.Dispatch(context.Background(), d, Deps{Cmd: query.OSCommander{}})
	if err == nil {
		t.Fatal("exit 1 must produce an error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "FINAL-REASON") {
		t.Fatalf("tail must keep the LAST stderr line; err tail = %q", msg[max(0, len(msg)-200):])
	}
	if strings.Contains(msg, "noise-line-0-") || strings.Contains(msg, "noise-line-100-") {
		t.Fatalf("early stderr must be cut off; len(err) = %d", len(msg))
	}
	// prefix + suffix label + the bound (the "..." marker is the only addition).
	if limit := commandStderrTailMax + 200; len(msg) > limit {
		t.Fatalf("error is %d bytes, want <= %d", len(msg), limit)
	}
	if !strings.Contains(msg, "stderr tail: ...") {
		t.Fatalf("truncated tail must be marked with an ellipsis; err prefix = %q", msg[:min(len(msg), 120)])
	}
}

// TestCommandDispatch_SuccessStderrNotLogged: a successful run's stderr is
// never read into anything — Dispatch returns a nil error and an empty Result.
func TestCommandDispatch_SuccessStderrNotLogged(t *testing.T) {
	d := DispatchContext{Role: shCommandRole(`echo 'warning: noisy but fine' >&2; exit 0`), Item: item.Item{ID: "b1"}}

	res, err := commandExecutor{}.Dispatch(context.Background(), d, Deps{Cmd: query.OSCommander{}})
	if err != nil {
		t.Fatalf("exit 0 must succeed, got %v", err)
	}
	if fmt.Sprintf("%+v", res) != fmt.Sprintf("%+v", report.Result{}) {
		t.Fatalf("success must return an empty Result, got %+v", res)
	}
}

// TestCommandDispatch_BusyExitStillErrBusyWithStderr: exit 9 stays a clean
// decline; the tail is only for failures.
func TestCommandDispatch_BusyExitStillErrBusyWithStderr(t *testing.T) {
	d := DispatchContext{Role: shCommandRole(`echo 'at capacity' >&2; exit 9`), Item: item.Item{ID: "b1"}}

	_, err := commandExecutor{}.Dispatch(context.Background(), d, Deps{Cmd: query.OSCommander{}})
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("exit 9 must still map to ErrBusy; err = %v", err)
	}
	// The busy path must stay free of the failure-only stderr tail: the
	// command's stderr is not carried into the error at all.
	for _, leak := range []string{"stderr tail", "at capacity"} {
		if strings.Contains(err.Error(), leak) {
			t.Fatalf("busy error must not carry %q; err = %v", leak, err)
		}
	}
}

func TestStderrTail_SanitizesAndRedacts(t *testing.T) {
	got := stderrTail([]byte("\x1b[31mred\x1b[0m\r\n\n  tab\there  \nBearer abcdefghijklmnopqrstuvwxyz0123456789ABCDEF\n"))
	if strings.ContainsAny(got, "\x1b\r\n\t") {
		t.Fatalf("control characters survived: %q", got)
	}
	if strings.Contains(got, "abcdefghijklmnopqrstuvwxyz0123456789ABCDEF") {
		t.Fatalf("bearer token not redacted: %q", got)
	}
	if got == "" || stderrTail(nil) != "" || stderrTail([]byte(" \n\r\n")) != "" {
		t.Fatalf("empty/whitespace-only stderr must yield no tail (got %q)", got)
	}
}
