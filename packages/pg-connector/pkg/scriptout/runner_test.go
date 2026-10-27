// runner_test.go: the real exec path (ExecRunner, and runInvoke through it)
// against real processes, the V-3 regression guard behind the in-process
// double of exec_test.go. Each test starts one /bin/sh (or a missing
// binary), never the race-built test binary, so none pays the race
// runtime's 1 s exit sleep.
package scriptout

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestExecRunnerMapsExitCode(t *testing.T) {
	res := ExecRunner(context.Background(), Call{Name: "sh", Args: []string{"-c", "exit 3"}})
	if res.Code != 3 || res.Signaled || res.StartErr != nil {
		t.Fatalf("got %+v, want code 3, not signaled, no start error", res)
	}
}

func TestExecRunnerCapturesStdoutAndStderrSeparately(t *testing.T) {
	res := ExecRunner(context.Background(), Call{Name: "sh", Args: []string{"-c", "echo out; echo err >&2"}})
	if res.Code != 0 || res.Stdout != "out\n" || res.Stderr != "err\n" {
		t.Fatalf("got %+v, want stdout %q stderr %q", res, "out\n", "err\n")
	}
}

func TestExecRunnerPassesEnvAndStdin(t *testing.T) {
	t.Setenv("SCRIPTOUT_RUNNER_AMBIENT", "ambient")
	res := ExecRunner(context.Background(), Call{
		Name:  "sh",
		Args:  []string{"-c", `printf '%s|%s|' "$SCRIPTOUT_RUNNER_AMBIENT" "$SCRIPTOUT_RUNNER_EXTRA"; cat`},
		Env:   []string{"SCRIPTOUT_RUNNER_EXTRA=extra"},
		Stdin: "in",
	})
	if res.Stdout != "ambient|extra|in" {
		t.Fatalf("got %+v: Env must be appended to the inherited environment and Stdin delivered", res)
	}
}

func TestExecRunnerReportsSignal(t *testing.T) {
	res := ExecRunner(context.Background(), Call{Name: "sh", Args: []string{"-c", "kill -KILL $$"}})
	if res.Code != -1 || !res.Signaled {
		t.Fatalf("got %+v, want code -1 and Signaled", res)
	}
}

func TestExecRunnerContextDeadlineKillsChild(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	res := ExecRunner(ctx, Call{Name: "sh", Args: []string{"-c", "sleep 30"}})
	if res.Code != -1 || !res.Signaled {
		t.Fatalf("got %+v, want code -1 and Signaled after the deadline", res)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("deadline not honored: took %v", elapsed)
	}
}

func TestExecRunnerMissingBinaryIsStartErr(t *testing.T) {
	res := ExecRunner(context.Background(), Call{Name: "scriptout-no-such-binary-xyz"})
	if res.StartErr == nil || res.Code != -1 {
		t.Fatalf("got %+v, want a start error and code -1", res)
	}
}

// TestExecRunnerWaitDelayBoundsAGrandchildHoldingStdout is the real-process
// guard for Call.WaitDelay (bead #13): the killed child's own grandchild
// (the background sleep) inherits the stdout pipe, so without WaitDelay the
// call would block until that sleep ends.
func TestExecRunnerWaitDelayBoundsAGrandchildHoldingStdout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	res := ExecRunner(ctx, Call{
		Name:      "sh",
		Args:      []string{"-c", "sleep 20 & wait"},
		WaitDelay: 200 * time.Millisecond,
	})
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("WaitDelay did not bound the call: took %v (result %+v)", elapsed, res)
	}
	if !res.Signaled {
		t.Fatalf("got %+v, want Signaled", res)
	}
}

// TestRunInvokeRealProcessRoundTrip runs the whole runInvoke path (request on
// stdin, response on stdout, nil runner meaning ExecRunner) against a real
// shell standing in for a backend binary.
func TestRunInvokeRealProcessRoundTrip(t *testing.T) {
	prev := execRunner
	execRunner = nil
	t.Cleanup(func() { execRunner = prev })

	tgt := Target{Name: "real-sh", Command: []string{"sh", "-c", `cat >/dev/null; echo '{"protocolVersion":` + protocolVersionText() + `,"result":{"state":"OK"}}'`}}
	resp, err := InvokeTarget(context.Background(), tgt, OpAuthStatus, nil, nil)
	if err != nil {
		t.Fatalf("InvokeTarget: %v", err)
	}
	if !strings.Contains(string(resp.Result), `"OK"`) {
		t.Fatalf("result = %s", resp.Result)
	}
}

// TestRunInvokeRealProcessFailureNamesExitStatusAndStderr pins the error
// text a real failing backend produces: exit status plus folded stderr.
func TestRunInvokeRealProcessFailureNamesExitStatusAndStderr(t *testing.T) {
	prev := execRunner
	execRunner = nil
	t.Cleanup(func() { execRunner = prev })

	tgt := Target{Name: "real-sh", Command: []string{"sh", "-c", "echo boom >&2; exit 2"}}
	_, err := InvokeTarget(context.Background(), tgt, "x", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "exit status 2") || !strings.Contains(err.Error(), "stderr: boom") {
		t.Fatalf("got %v, want the exit status and folded stderr", err)
	}
}

func protocolVersionText() string {
	b, _ := json.Marshal(ProtocolVersion)
	return string(b)
}
