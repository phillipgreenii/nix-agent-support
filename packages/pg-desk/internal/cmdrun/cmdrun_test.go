// cmdrun_test.go: real-process tests of ExecRunner (V-3 regression guard for
// the real exec path; every other test in the module answers a Runner in
// process). They run sh -c, never the test binary.
package cmdrun

import (
	"context"
	"errors"
	"os/exec"
	"testing"
	"time"
)

func TestExecRunnerMapsExitCodeAndCapturesStdoutAndStderrSeparately(t *testing.T) {
	r := ExecRunner(context.Background(), Call{Name: "sh", Args: []string{"-c", "printf out; printf err >&2; exit 3"}})
	if r.Code != 3 || r.Signaled || r.StartErr != nil {
		t.Fatalf("Code=%d Signaled=%v StartErr=%v, want 3 false nil", r.Code, r.Signaled, r.StartErr)
	}
	if r.Stdout != "out" || r.Stderr != "err" {
		t.Errorf("stdout=%q stderr=%q, want out / err", r.Stdout, r.Stderr)
	}
}

func TestExecRunnerPassesEnvAppendedToTheProcessEnvironmentAndStdin(t *testing.T) {
	t.Setenv("CMDRUN_INHERITED", "inh")
	r := ExecRunner(context.Background(), Call{
		Name:  "sh",
		Args:  []string{"-c", `printf '%s,%s|' "$CMDRUN_PROBE" "$CMDRUN_INHERITED"; cat`},
		Stdin: "in",
		Env:   []string{"CMDRUN_PROBE=v"},
	})
	if r.Code != 0 || r.Stdout != "v,inh|in" {
		t.Errorf("Code=%d stdout=%q, want 0 and v,inh|in", r.Code, r.Stdout)
	}
}

func TestExecRunnerReportsASignalledChild(t *testing.T) {
	r := ExecRunner(context.Background(), Call{Name: "sh", Args: []string{"-c", "kill -9 $$"}})
	if !r.Signaled || r.Code != -1 || r.StartErr != nil {
		t.Errorf("Code=%d Signaled=%v StartErr=%v, want -1 true nil", r.Code, r.Signaled, r.StartErr)
	}
}

func TestExecRunnerReportsATimedOutChildAsSignaled(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	r := ExecRunner(ctx, Call{Name: "sh", Args: []string{"-c", "sleep 30"}})
	if !r.Signaled || r.Code != -1 {
		t.Errorf("Code=%d Signaled=%v, want -1 true", r.Code, r.Signaled)
	}
}

func TestExecRunnerMissingBinaryIsAStartError(t *testing.T) {
	r := ExecRunner(context.Background(), Call{Name: "pg-desk-no-such-binary-for-test"})
	var pe *exec.Error
	if r.StartErr == nil || !errors.As(r.StartErr, &pe) || r.Code != -1 {
		t.Errorf("Code=%d StartErr=%v, want -1 and an *exec.Error", r.Code, r.StartErr)
	}
}

func TestOrDefaultsToExecRunner(t *testing.T) {
	called := false
	custom := Runner(func(context.Context, Call) Result { called = true; return Result{} })
	Or(custom)(context.Background(), Call{})
	if !called {
		t.Error("Or(custom) did not return custom")
	}
	r := Or(nil)(context.Background(), Call{Name: "sh", Args: []string{"-c", "exit 5"}})
	if r.Code != 5 {
		t.Errorf("Or(nil) Code=%d, want 5 (ExecRunner)", r.Code)
	}
}
