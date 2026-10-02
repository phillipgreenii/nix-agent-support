// Package contracttest runs a handler binary against a canned failure report
// and checks the result with contract.Classify, the one implementation of the
// result rules. Each reference handler's tests call Run once per canned
// report in testdata/reports/.
package contracttest

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-rescue/internal/contract"
	"github.com/phillipgreenii/pg-rescue/internal/report"
)

// runTimeout bounds one handler run; tests shorten it.
var runTimeout = 30 * time.Second

// Result is what one handler run produced and how the contract read it.
type Result struct {
	Exit      int
	Stdout    string
	Stderr    string
	Truncated bool
	Outcome   contract.Outcome
	Reason    string
	Reported  contract.Reported
	// ReportPath is the report.json the handler was given.
	ReportPath string
}

// Fixture returns the path of a canned report in testdata/reports/, for
// example Fixture("first-attempt"). The canned reports are:
// first-attempt, three-prior-attempts, stdin-mode and unknown-schema-version.
func Fixture(name string) string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "testdata", "reports", name+".json")
}

// Run executes argv as the handler would be executed: cwd is the report's
// command cwd (else a scratch dir), stdin is /dev/null, it has its own process
// group, and PG_RESCUE_REPORT and the other PG_RESCUE_* variables describe the
// fixture. env (KEY=VALUE entries) is added last, so it can put fake binaries
// on PATH or override anything.
//
// Run fails t if the handler cannot be started or times out. It also fails t
// when the handler exits with a code in the outcome table (0, 2, 3) but
// contract.Classify does not accept its stdout, which is the contract
// violation every handler test exists to catch. What the outcome SHOULD be is
// the caller's to assert on the returned Result: a handler that rightly exits 1
// on an unknown schema_version is accepted here.
func Run(t testing.TB, argv []string, reportFixture string, env []string) Result {
	t.Helper()
	raw, err := os.ReadFile(reportFixture)
	if err != nil {
		t.Fatalf("contracttest: cannot read the report fixture: %v", err)
	}
	dir := t.TempDir()
	reportPath := filepath.Join(dir, "report.json")
	outputPath := filepath.Join(dir, "output.log")
	var r report.Report
	_ = json.Unmarshal(raw, &r) // an unparseable fixture is still delivered as-is
	if err := os.WriteFile(reportPath, raw, 0o600); err != nil {
		t.Fatalf("contracttest: %v", err)
	}
	if err := os.WriteFile(outputPath, []byte(r.OutputTail), 0o600); err != nil {
		t.Fatalf("contracttest: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), runTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	if r.Command != nil {
		if st, err := os.Stat(r.Command.Cwd); err == nil && st.IsDir() {
			cmd.Dir = r.Command.Cwd
		}
	}
	cmd.Env = append(append(os.Environ(), handlerEnv(&r, reportPath, outputPath, dir)...), env...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second
	stdout, stderr := contract.NewResultBuffer(), &bytes.Buffer{}
	cmd.Stdout, cmd.Stderr = stdout, stderr

	exit := 0
	if err := cmd.Run(); err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok || ctx.Err() != nil {
			t.Fatalf("contracttest: running %q: %v (stderr: %s)", argv, err, stderr.String())
		}
		exit = ee.ExitCode()
	}

	res := Result{Exit: exit, Stdout: string(stdout.Bytes()), Stderr: stderr.String(), Truncated: stdout.Truncated(), ReportPath: reportPath}
	res.Outcome, res.Reason, res.Reported = contract.Classify(exit, stdout.Bytes(), res.Truncated)
	if want := contract.OutcomeForExit(exit); want != contract.Failed && res.Outcome != want {
		t.Errorf("contracttest: handler %q exited %d (%s) but contract.Classify rejected its result: %s\nstdout: %.200q", argv, exit, want, res.Reason, res.Stdout)
	}
	return res
}

// handlerEnv is the PG_RESCUE_* environment the wrapper would set before this
// handler's turn.
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
