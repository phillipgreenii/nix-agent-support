// runner_test.go: the real exec path (ExecRunner) against real processes,
// the V-3 regression guard behind the in-process doubles of testmain_test.go.
// Each test starts one /bin/sh (or a missing binary), never the race-built
// test binary, so none pays the race runtime's 1 s exit sleep.
package main

import (
	"context"
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
	t.Setenv("CCPOOL_PROBE_RUNNER_AMBIENT", "ambient")
	res := ExecRunner(context.Background(), Call{
		Name:  "sh",
		Args:  []string{"-c", `printf '%s|%s|' "$CCPOOL_PROBE_RUNNER_AMBIENT" "$CCPOOL_POOL"; cat`},
		Env:   []string{"CCPOOL_POOL=/pool/x"},
		Stdin: "in",
	})
	if res.Stdout != "ambient|/pool/x|in" {
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
	res := ExecRunner(context.Background(), Call{Name: "ccpool-probe-no-such-binary-xyz"})
	if res.StartErr == nil || res.Code != -1 {
		t.Fatalf("got %+v, want a start error and code -1", res)
	}
}

func TestRunnerNilMeansExecRunner(t *testing.T) {
	var r Runner
	res := r.do(context.Background(), Call{Name: "sh", Args: []string{"-c", "echo nil-runner"}})
	if !strings.Contains(res.Stdout, "nil-runner") {
		t.Fatalf("a nil Runner must run through ExecRunner, got %+v", res)
	}
}
