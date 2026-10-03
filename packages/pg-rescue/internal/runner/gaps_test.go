package runner_test

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-rescue/internal/cli"
	"github.com/phillipgreenii/pg-rescue/internal/config"
	"github.com/phillipgreenii/pg-rescue/internal/contract"
	"github.com/phillipgreenii/pg-rescue/internal/runner"
)

// The tests in this file close gaps that mutation testing (pg-go-mutate)
// found in the chain runner: assertions the first suite did not make.

// signalOnceFileExists makes the injected clock queue sig the first time it is
// read after marker exists: that is, at a moment no child is being waited on.
func (e *e2e) signalOnceFileExists(marker string, sig syscall.Signal) {
	e.exec.Signals = make(chan os.Signal, 8)
	var sent bool
	e.rt.Now = func() time.Time {
		if !sent && exists(marker) {
			sent = true
			e.exec.Signals <- sig
		}
		return time.Date(2026, 10, 2, 14, 3, 11, 0, time.UTC)
	}
}

func TestSignalBetweenHandlersStopsTheChain(t *testing.T) {
	dir := t.TempDir()
	done := filepath.Join(dir, "first-done")
	second := filepath.Join(dir, "second-ran")
	e := newE2E(t, "", []hd{
		{name: "first", argv: helperArgv("result", "outcome=declined", "touch="+done)},
		{name: "second", argv: helperArgv("ran", "file="+second, "code=2")},
	}, chainOf("first", "second"))
	e.signalOnceFileExists(done, syscall.SIGTERM)
	code, _, _ := e.wrap("first,second", helperArgv("exit", "code=1"))
	r := e.result()
	if code != 143 || r.Kind != runner.KindInterrupted || r.Interrupted == nil || r.Interrupted.Phase != runner.PhaseBetween {
		t.Fatalf("exit=%d result=%+v interrupted=%+v", code, r, r.Interrupted)
	}
	if exists(second) {
		t.Error("the second handler must not start after a signal")
	}
	if atts := e.onDisk().Attempts; len(atts) != 1 || atts[0].Outcome != contract.Declined {
		t.Errorf("the finished handler stays recorded: %+v", atts)
	}
}

func TestSignalAfterTheLastHandlerIsStillAnInterrupt(t *testing.T) {
	dir := t.TempDir()
	done := filepath.Join(dir, "done")
	e := newE2E(t, "", []hd{{name: "only", argv: helperArgv("result", "outcome=declined", "touch="+done)}}, chainOf("only"))
	e.signalOnceFileExists(done, syscall.SIGHUP)
	code, _, _ := e.wrap("only", helperArgv("exit", "code=1"))
	if code != 129 || e.result().Kind != runner.KindInterrupted {
		t.Errorf("exit=%d kind=%s", code, e.result().Kind)
	}
}

func TestTheFirstSignalWins(t *testing.T) {
	dir := t.TempDir()
	started := filepath.Join(dir, "started")
	e := newE2E(t, "", []hd{result("h", "declined")}, chainOf("h"))
	e.exec.Signals = make(chan os.Signal, 8)
	done := make(chan int, 1)
	go func() {
		c, _, _ := e.wrap("h", helperArgv("ignore-term", "secs=2", "started="+started))
		done <- c
	}()
	waitForFile(t, started)
	e.exec.Signals <- syscall.SIGINT
	e.exec.Signals <- syscall.SIGTERM
	if c := <-done; c != 130 || e.result().Interrupted.Signal != syscall.SIGINT {
		t.Errorf("exit=%d interruption=%+v; the first signal (INT) decides", c, e.result().Interrupted)
	}
}

func TestSignalWhileReadingStdin(t *testing.T) {
	e := newE2E(t, "", []hd{result("h", "declined")}, chainOf("h"))
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pw.Close() }()

	defer func() { _ = pr.Close() }()

	e.rt.Stdin = pr // never closed: the read blocks until a signal arrives
	e.exec.Signals = make(chan os.Signal, 1)
	time.AfterFunc(300*time.Millisecond, func() { e.exec.Signals <- syscall.SIGINT })
	code, _, _ := e.wrap("h", nil, "--stdin", "--verify", "true")
	r := e.result()
	if code != 130 || r.Interrupted == nil || r.Interrupted.Phase != runner.PhaseStdin {
		t.Errorf("exit=%d interruption=%+v", code, r.Interrupted)
	}
}

func TestCommandAndVerifyStayInTheWrappersProcessGroup(t *testing.T) {
	dir := t.TempDir()
	e := newE2E(t, "", []hd{result("fixer", "resolved")}, chainOf("fixer"))
	e.wrap("fixer", helperArgv("record", "dest="+dir+"/cmd", "code=1"), "--verify", helperShell("record", "dest="+dir+"/verify"))
	for _, f := range []string{"cmd", "verify"} {
		r := readRecord(t, filepath.Join(dir, f))
		if r.Pgid != r.ParentPgid || r.Pgid == r.Pid {
			t.Errorf("%s: pid=%d pgid=%d parent pgid=%d; it must stay in the wrapper's group", f, r.Pid, r.Pgid, r.ParentPgid)
		}
	}
}

func TestCommandOutputThatArrivesAfterItExitsIsStillCaptured(t *testing.T) {
	// The command leaves a child holding its stdout; like a shell pipeline the
	// wrapper keeps reading until the pipe closes.
	e := newE2E(t, "", []hd{result("d", "declined")}, chainOf("d"))
	start := time.Now()
	code, stdout, _ := e.wrap("d", helperArgv("grandchild", "hold=1", "child=late", "ms=900", "text=LATE-TEXT\n", "code=1"))
	if code != 1 || stdout != "LATE-TEXT\n" || time.Since(start) < 800*time.Millisecond {
		t.Errorf("exit=%d stdout=%q after %v", code, stdout, time.Since(start))
	}
}

func TestGrandchildThatIgnoresTermIsKilledAfterTheGrace(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "pid")
	e := newE2E(t, "", []hd{{name: "h", argv: helperArgv("grandchild", "child=ignore-term", "ready="+pidFile+".ready", "pid="+pidFile, "code=2")}}, chainOf("h"))
	e.wrap("h", helperArgv("exit", "code=1"))
	pid, _ := strconv.Atoi(waitForFile(t, pidFile))
	if !eventuallyDead(pid) {
		t.Errorf("grandchild %d survived TERM and was never killed", pid)
	}
}

func TestEscapedPipeHolderCannotWedgeTheWrapper(t *testing.T) {
	// A grandchild in its own session survives the group kill and keeps the
	// handler's pipes open; the wrapper's wait for them is bounded.
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "pid")
	e := newE2E(t, "", []hd{{name: "h", argv: helperArgv("grandchild", "hold=1", "setsid=1", "secs=6", "pid="+pidFile, "code=2")}}, chainOf("h"))
	start := time.Now()
	code, _, _ := e.wrap("h", helperArgv("exit", "code=1"))
	took := time.Since(start)
	if code != 1 || took > 4*time.Second {
		t.Errorf("exit=%d after %v: the wait for the pipes must be bounded", code, took)
	}
	pid, _ := strconv.Atoi(waitForFile(t, pidFile))
	_ = syscall.Kill(pid, syscall.SIGKILL) // the escapee is the test's to clean up
}

func TestDepthIgnoresNonsense(t *testing.T) {
	for in, want := range map[string]int{"": 1, "abc": 1, "-5": 1, "0": 1, "2": 3, "10": 11} {
		e := newE2E(t, "", []hd{result("d", "declined")}, chainOf("d"))
		e.env["PG_RESCUE_DEPTH"] = in
		e.wrap("d", helperArgv("exit", "code=1"))
		if got := e.onDisk().Depth; got != want {
			t.Errorf("PG_RESCUE_DEPTH=%q -> depth %d; want %d", in, got, want)
		}
	}
}

func TestAttemptWithoutDetailsHasNoDetailsFile(t *testing.T) {
	e := newE2E(t, "", []hd{result("d", "declined", "summary=just a summary")}, chainOf("d"))
	e.wrap("d", helperArgv("exit", "code=1"))
	a := e.onDisk().Attempts[0]
	if a.Reported.Details != "" || a.Reported.DetailsFile != "" {
		t.Errorf("reported = %+v", a.Reported)
	}
	if ents, _ := filepath.Glob(filepath.Join(e.rdir(), "*.details")); len(ents) != 0 {
		t.Errorf("stray details files: %v", ents)
	}
}

func TestReportWriteFailureFailsOnlyThatAttempt(t *testing.T) {
	e := newE2E(t, "", []hd{
		{name: "locks", argv: helperArgv("chmod-rundir", "mode=500", "code=2")},
		result("blocked", "declined"),
	}, chainOf("locks", "blocked"))
	t.Cleanup(func() {
		if e.exec.Last != nil && e.exec.Last.Report != nil {
			_ = os.Chmod(filepath.Dir(e.exec.Last.Report.OutputFile), 0o700)
		}
	})
	code, _, _ := e.wrap("locks,blocked", helperArgv("exit", "code=3"))
	atts := e.result().Report.Attempts
	if code != 3 || len(atts) != 2 {
		t.Fatalf("exit=%d attempts=%+v", code, atts)
	}
	if a := atts[1]; a.Outcome != contract.Failed || !strings.HasPrefix(a.Reason, "cannot write report.json: ") {
		t.Errorf("attempt 2 = %+v", a)
	}
}

func TestStderrFileThatCannotBeCreatedFailsTheAttempt(t *testing.T) {
	e := newE2E(t, "", []hd{
		{name: "squats", argv: helperArgv("exit", "code=2", "touch={rundir}/attempt-2.stderr.log")},
		result("blocked", "declined"),
	}, chainOf("squats", "blocked"))
	e.wrap("squats,blocked", helperArgv("exit", "code=3"))
	if a := e.result().Report.Attempts[1]; a.Outcome != contract.Failed || !strings.HasPrefix(a.Reason, "cannot create the stderr file: ") {
		t.Errorf("attempt 2 = %+v", a)
	}
}

func TestVerifyOutputFileThatCannotBeCreatedFailsTheClaim(t *testing.T) {
	e := newE2E(t, "", []hd{{name: "squats", argv: helperArgv("result", "outcome=resolved", "touch={rundir}/attempt-1.verify.log")}}, chainOf("squats"))
	code, _, _ := e.wrap("squats", helperArgv("exit", "code=3"), "--verify", "true")
	if a := e.result().Report.Attempts[0]; code != 3 || a.Outcome != contract.Failed ||
		!strings.HasPrefix(a.Reason, "resolved → verify could not start: cannot create the output file") {
		t.Errorf("exit=%d attempt=%+v", code, a)
	}
}

func TestVerifyKilledBySignalIsReported(t *testing.T) {
	e := newE2E(t, "", []hd{result("fixer", "resolved")}, chainOf("fixer"))
	code, _, _ := e.wrap("fixer", helperArgv("exit", "code=4"), "--verify", helperShell("selfkill", "sig=9"))
	if a := e.result().Report.Attempts[0]; code != 4 || a.Reason != "resolved → verify killed by signal SIGKILL" {
		t.Errorf("exit=%d attempt=%+v", code, a)
	}
}

func TestShellFallbackAndLookupFailureForVerify(t *testing.T) {
	// With no PATH to find sh on, --verify still works through /bin/sh.
	e := newE2E(t, "", []hd{result("fixer", "resolved")}, chainOf("fixer"))
	e.rt.LookPath = func(string) (string, error) { return "", errors.New("no PATH") }
	if code, _, stderr := e.wrap("fixer", helperArgv("exit", "code=4"), "--verify", "exit 0"); code != 0 {
		t.Errorf("exit=%d (stderr %s)", code, stderr)
	}

	// The default re-run needs the command to resolve again; when it cannot,
	// the claim fails with the lookup error.
	e2 := newE2E(t, "", []hd{result("fixer", "resolved")}, chainOf("fixer"))
	calls := 0
	e2.rt.LookPath = func(name string) (string, error) {
		calls++
		if calls == 1 {
			return exec.LookPath("false") // the command resolves once...
		}
		return "", errors.New("gone from PATH") // ...and not again
	}
	code, _, _ := e2.wrap("fixer", []string{"false"})
	if a := e2.result().Report.Attempts[0]; code != 1 || a.Reason != "resolved → verify could not start: gone from PATH" {
		t.Errorf("exit=%d attempt=%+v", code, a)
	}
}

func TestUnspawnableCommandIsAWrapperError(t *testing.T) {
	e := newE2E(t, "", []hd{result("h", "resolved")}, chainOf("h"))
	for name, cmd := range map[string][]string{
		"not on PATH":       {"pg-rescue-no-such-command-xyz"},
		"missing path":      {"/no/such/dir/cmd"},
		"not an executable": {e.cfgPath},
		"relative missing":  {"./no-such-script"},
	} {
		code, stdout, stderr := e.wrap("h", cmd)
		if code != 70 || stdout != "" || !strings.HasPrefix(stderr, "pg-rescue: cannot run") {
			t.Errorf("%s: exit=%d stdout=%q stderr=%q", name, code, stdout, stderr)
		}
		if r := e.result(); r.Kind != runner.KindError {
			t.Errorf("%s: kind = %s", name, r.Kind)
		}
		if dirs := e.runDirs(); len(dirs) != 0 {
			t.Errorf("%s: run directory left behind: %v", name, dirs)
		}
	}
}

// failingWriter stands in for a caller that closed our stdout.
type failingWriter struct{ calls int }

func (w *failingWriter) Write([]byte) (int, error) { w.calls++; return 0, errors.New("broken pipe") }

// params builds runner.Params directly, leaving every injectable seam unset
// so the defaults are exercised.
func params(t *testing.T, cfgText string, opts *cli.Options) runner.Params {
	t.Helper()
	cfg, err := config.Parse("test.toml", []byte(cfgText), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	runDir := filepath.Join(t.TempDir(), "run")
	if err := os.Mkdir(runDir, 0o700); err != nil {
		t.Fatal(err)
	}
	return runner.Params{
		Options: opts, Config: cfg, Cwd: t.TempDir(), RunID: "20261002T140311Z-00000000", RunDir: runDir,
		StateRoot: t.TempDir(), StartedAt: time.Now(),
	}
}

func TestRunWithEveryDefault(t *testing.T) {
	p := params(t, "[handler.h]\ncommand = [\"sh\", \"-c\", \"exit 2\"]\n", &cli.Options{Argv: []string{"sh", "-c", "echo out; echo err >&2; exit 1"}})
	p.Handlers = []string{"h"}
	var so, se bytes.Buffer
	p.Stdout, p.Stderr = &so, &se
	r := runner.Run(p)
	if r.ExitCode != 1 || r.Kind != runner.KindUnhandled || so.String() != "out\n" || se.String() != "err\n" {
		t.Errorf("result=%+v stdout=%q stderr=%q", r, so.String(), se.String())
	}
	if r.Duration < 0 || len(r.Report.Attempts) != 1 || r.Report.Attempts[0].Outcome != contract.Declined {
		t.Errorf("report = %+v", r.Report)
	}
	if !strings.HasSuffix(r.Binaries[0], "/sh") {
		t.Errorf("binary = %q; the default lookup must resolve sh on PATH", r.Binaries[0])
	}
	if r.Report.Depth != 1 || r.Report.ParentRunID != nil {
		t.Errorf("depth=%d parent=%v from the real environment", r.Report.Depth, r.Report.ParentRunID)
	}
}

func TestRunSurvivesNilStreamsAndABrokenStdout(t *testing.T) {
	// nil Stdout and Stderr: output is captured, not passed through.
	p := params(t, "[handler.h]\ncommand = [\"true\"]\n", &cli.Options{Argv: []string{"sh", "-c", "echo out; exit 0"}})
	if r := runner.Run(p); r.ExitCode != 0 {
		t.Errorf("exit = %d", r.ExitCode)
	}

	// A broken passthrough neither fails the run nor stops the capture, and
	// is written to only once.
	p2 := params(t, "[handler.h]\ncommand = [\"sh\", \"-c\", \"exit 2\"]\n", &cli.Options{Argv: []string{"sh", "-c", "i=0; while [ $i -lt 50 ]; do echo line$i; i=$((i+1)); done; exit 7"}})
	p2.Handlers = []string{"h"}
	fw := &failingWriter{}
	p2.Stdout, p2.Stderr = fw, fw
	r := runner.Run(p2)
	if r.ExitCode != 7 || fw.calls > 2 {
		t.Errorf("exit=%d passthrough writes=%d", r.ExitCode, fw.calls)
	}
	if b, _ := os.ReadFile(r.Report.OutputFile); !strings.Contains(string(b), "line49\n") {
		t.Errorf("capture stopped early: %.60q", b)
	}
}

func TestStdinModeWithNoStdinIsEmptyInput(t *testing.T) {
	p := params(t, "[handler.h]\ncommand = [\"sh\", \"-c\", \"exit 2\"]\n", &cli.Options{Stdin: true, HasVerify: true, Verify: "true"})
	p.Handlers = []string{"h"}
	r := runner.Run(p)
	if r.ExitCode != 1 || r.Report == nil || r.Report.OutputTail != "" {
		t.Errorf("result=%+v", r)
	}
}

func TestOutputLogThatCannotBeCreatedIsAWrapperError(t *testing.T) {
	p := params(t, "[handler.h]\ncommand = [\"true\"]\n", &cli.Options{Argv: []string{"true"}})
	p.RunDir = filepath.Join(p.RunDir, "missing", "dir")
	var se bytes.Buffer
	p.Stderr = &se
	r := runner.Run(p)
	if r.ExitCode != 70 || r.Kind != runner.KindError || !strings.Contains(se.String(), "cannot create the output log") {
		t.Errorf("result=%+v stderr=%q", r, se.String())
	}
}
