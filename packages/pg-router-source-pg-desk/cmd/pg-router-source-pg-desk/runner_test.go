// runner_test.go: real-process tests of ExecRunner (V-3 regression guard for
// the real exec path; everything else answers the Runner seam in process).
// They run sh -c, not the test binary.
package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
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

func TestExecRunnerPassesEnvAndStdin(t *testing.T) {
	r := ExecRunner(context.Background(), Call{
		Name:  "sh",
		Args:  []string{"-c", `printf '%s|' "$PGD_PROBE"; cat`},
		Stdin: "in",
		Env:   []string{"PGD_PROBE=v"},
	})
	if r.Code != 0 || r.Stdout != "v|in" {
		t.Errorf("Code=%d stdout=%q, want 0 and v|in", r.Code, r.Stdout)
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

// The seam end to end with the real runner: a real child that exits 2 with a
// body on stdout is classified like pg-desk's partial result.
func TestRunPgDeskThroughExecRunnerUsesTheRealPath(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/pg-desk", []byte("#!/bin/sh\nprintf '%s' \"$*\"\nexit 2\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":/usr/bin:/bin")
	res, err := runPgDesk(context.Background(), []string{"widget", "changes"})
	if err != nil {
		t.Fatal(err)
	}
	if res.exitCode != 2 || !classifyExit(res.exitCode) || !strings.Contains(string(res.stdout), "widget changes") {
		t.Errorf("exit=%d stdout=%q", res.exitCode, res.stdout)
	}
}
