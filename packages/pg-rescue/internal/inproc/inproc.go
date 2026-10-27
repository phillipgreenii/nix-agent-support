// Package inproc runs a pg-rescue command's run function in the test process
// against a canned failure report. It is contracttest.Run without the child
// process: the same working directory, the same PG_RESCUE_* environment, the
// same contract.Classify check, the same Result.
//
// Why it exists: a race-built test binary sleeps a flat 1.0 s at every
// process exit (the race runtime's atexit_sleep_ms), so a test that re-executes
// the test binary as the command pays about a second per run under -race. The
// command packages' tests call the command's run function here instead and
// keep a handful of real-process tests (internal/contracttest and a re-executed
// main) for what only a process can show: its exit status through os.Exit, its
// umask, signals and orphaning.
//
// What it does not model: a process group (the command is not started in its
// own group), stdin (always empty, as /dev/null would be) and the command's
// own signal handling beyond what the command installs itself.
package inproc

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-rescue/internal/contract"
	"github.com/phillipgreenii/pg-rescue/internal/contracttest"
	"github.com/phillipgreenii/pg-rescue/internal/report"
)

// Entry is a command's run function: its arguments, its environment as a
// KEY=VALUE list, its standard streams, and the exit code it would exit with.
type Entry func(args, env []string, stdin io.Reader, stdout, stderr io.Writer) int

// Run executes entry the way contracttest.Run executes a handler binary. The
// process environment and working directory are set for the call and restored
// before Run returns, so one test can call it again with different inputs, as
// a fresh process would see them: the working directory is the report's
// command cwd (else a scratch dir), and the environment is the process's own,
// then the PG_RESCUE_* variables describing the report, then env (KEY=VALUE
// entries, last wins).
//
// Like contracttest.Run it fails t when the handler exits with a code in the
// outcome table but contract.Classify does not accept its stdout.
//
// The process environment is set (and not only handed to entry) because
// commands look their own children up on the process PATH (exec.LookPath), as
// the real binary does. Run changes process-wide state, so a test that calls
// it MUST NOT be parallel; none in this module is.
func Run(t testing.TB, entry Entry, args []string, reportFixture string, env []string) contracttest.Result {
	t.Helper()
	raw, err := os.ReadFile(reportFixture)
	if err != nil {
		t.Fatalf("inproc: cannot read the report fixture: %v", err)
	}
	dir := t.TempDir()
	reportPath := filepath.Join(dir, "report.json")
	outputPath := filepath.Join(dir, "output.log")
	var r report.Report
	_ = json.Unmarshal(raw, &r) // an unparseable fixture is still delivered as-is
	if err := os.WriteFile(reportPath, raw, 0o600); err != nil {
		t.Fatalf("inproc: %v", err)
	}
	if err := os.WriteFile(outputPath, []byte(r.OutputTail), 0o600); err != nil {
		t.Fatalf("inproc: %v", err)
	}

	cwd := dir
	if r.Command != nil {
		if st, err := os.Stat(r.Command.Cwd); err == nil && st.IsDir() {
			cwd = r.Command.Cwd
		}
	}

	stdout := contract.NewResultBuffer()
	var stderr bytes.Buffer
	exit := Call(t, entry, args, cwd, append(handlerEnv(&r, reportPath, outputPath, dir), env...), nil, stdout, &stderr)

	res := contracttest.Result{Exit: exit, Stdout: string(stdout.Bytes()), Stderr: stderr.String(), Truncated: stdout.Truncated(), ReportPath: reportPath}
	res.Outcome, res.Reason, res.Reported = contract.Classify(exit, stdout.Bytes(), res.Truncated)
	if want := contract.OutcomeForExit(exit); want != contract.Failed && res.Outcome != want {
		t.Errorf("inproc: handler %q exited %d (%s) but contract.Classify rejected its result: %s\nstdout: %.200q", args, exit, want, res.Reason, res.Stdout)
	}
	return res
}

// Call runs entry once with the process environment extended by env (last
// entry wins) and, when cwd is not empty, the working directory set to cwd;
// both are restored before it returns. entry receives the resulting
// environment as its env.
func Call(t testing.TB, entry Entry, args []string, cwd string, env []string, stdin io.Reader, stdout, stderr io.Writer) int {
	t.Helper()
	oldEnv := os.Environ()
	oldDir, dirErr := os.Getwd()
	defer func() {
		os.Clearenv()
		for _, kv := range oldEnv {
			if k, v, ok := strings.Cut(kv, "="); ok {
				_ = os.Setenv(k, v)
			}
		}
		if dirErr == nil {
			_ = os.Chdir(oldDir)
		}
	}()
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok {
			if err := os.Setenv(k, v); err != nil {
				t.Fatalf("inproc: setting %s: %v", k, err)
			}
		}
	}
	if cwd != "" {
		if err := os.Chdir(cwd); err != nil {
			t.Fatalf("inproc: %v", err)
		}
	}
	return entry(args, os.Environ(), stdin, stdout, stderr)
}

// handlerEnv is the PG_RESCUE_* environment the wrapper would set before this
// handler's turn. It mirrors contracttest's own (unexported) list, which
// TestHandlerEnvMatchesContracttest checks against the real thing.
func handlerEnv(r *report.Report, reportPath, outputPath, dir string) []string {
	env := []string{
		"PG_RESCUE_REPORT=" + reportPath,
		"PG_RESCUE_OUTPUT_FILE=" + outputPath,
		"PG_RESCUE_RUN_DIR=" + dir,
		"PG_RESCUE_RUN_LOG=" + filepath.Join(dir, "runs.jsonl"),
		"PG_RESCUE_RUN_ID=" + r.RunID,
		"PG_RESCUE_CONTEXT=" + r.Context,
		"PG_RESCUE_FINGERPRINT=" + r.Fingerprint,
		"PG_RESCUE_DEPTH=" + strconv.Itoa(max(r.Depth, 1)),
		"PG_RESCUE_POSITION=" + strconv.Itoa(len(r.Attempts)+1),
	}
	if len(r.Attempts) < len(r.Handlers) {
		env = append(env, "PG_RESCUE_HANDLER="+r.Handlers[len(r.Attempts)])
	}
	if r.ParentRunID != nil {
		env = append(env, "PG_RESCUE_PARENT_RUN_ID="+*r.ParentRunID)
	}
	if r.Command != nil {
		env = append(env, "PG_RESCUE_CMD="+report.QuoteArgv(r.Command.Argv), "PG_RESCUE_EXIT="+strconv.Itoa(r.Command.Exit))
	} else {
		env = append(env, "PG_RESCUE_CMD=", "PG_RESCUE_EXIT=")
	}
	return env
}
