package runner_test

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/phillipgreenii/pg-rescue/internal/contract"
	"github.com/phillipgreenii/pg-rescue/internal/report"
	"github.com/phillipgreenii/pg-rescue/internal/runner"
)

func TestVerifyPassAndFail(t *testing.T) {
	for _, c := range []struct {
		name       string
		verify     string
		wantExit   int
		wantKind   runner.Kind
		wantResult contract.Outcome
		wantReason string
	}{
		{"pass", "exit 0", 0, runner.KindResolved, contract.Resolved, "verify passed"},
		{"fail", "exit 3", 5, runner.KindUnhandled, contract.Failed, "resolved → verify failed (exit 3)"},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newE2E(t, "", []hd{result("fixer", "resolved", "summary=claims a fix")}, chainOf("fixer"))
			code, _, stderr := e.wrap("fixer", helperArgv("exit", "code=5"), "--verify", c.verify)
			if code != c.wantExit || e.result().Kind != c.wantKind {
				t.Fatalf("exit=%d kind=%s (stderr %s)", code, e.result().Kind, stderr)
			}
			a := e.onDisk().Attempts[0]
			if a.Outcome != c.wantResult || a.Reason != c.wantReason {
				t.Errorf("attempt = %+v", a)
			}
			if a.VerifyMS == nil || a.VerifyOutputFile == "" || !exists(a.VerifyOutputFile) {
				t.Errorf("verify facts missing: %+v", a)
			}
			if a.Reported.Summary != "claims a fix" || a.Exit != 0 {
				t.Errorf("the handler's own claim and exit are kept: %+v", a)
			}
			if c.wantKind == runner.KindResolved && e.result().ResolvedBy != "fixer" {
				t.Errorf("resolved_by = %q", e.result().ResolvedBy)
			}
		})
	}
}

func TestVerifyFailureContinuesTheChain(t *testing.T) {
	dir := t.TempDir()
	// fix-small claims a fix that does not hold; fix-large really fixes it.
	e := newE2E(t, "", []hd{
		result("fix-small", "resolved"),
		{name: "fix-large", argv: helperArgv("result", "outcome=resolved", "touch="+dir+"/fixed")},
	}, chainOf("fix-small", "fix-large"))
	code, _, stderr := e.wrap("fix-small,fix-large", helperArgv("needs", "file="+dir+"/fixed", "code=1"))
	if code != 0 {
		t.Fatalf("exit = %d (stderr %s)", code, stderr)
	}
	atts := e.onDisk().Attempts
	if len(atts) != 2 || atts[0].Outcome != contract.Failed || atts[0].Reason != "resolved → verify failed (exit 1)" ||
		atts[1].Outcome != contract.Resolved || atts[1].Reason != "verify passed" {
		t.Errorf("attempts = %+v", atts)
	}
}

func TestDefaultVerifyRerunsTheOriginalCommand(t *testing.T) {
	dir := t.TempDir()
	flag := filepath.Join(dir, "flag")
	// "flip" fails the first time and succeeds after: the re-run is the verify.
	e := newE2E(t, "", []hd{result("fixer", "resolved")}, chainOf("fixer"))
	code, _, stderr := e.wrap("fixer", helperArgv("flip", "file="+flag))
	if code != 0 || e.result().Kind != runner.KindResolved {
		t.Fatalf("exit=%d kind=%s stderr=%s", code, e.result().Kind, stderr)
	}
	if rep := e.onDisk(); rep.Verify != nil {
		t.Errorf("verify = %q; the report records null for a re-run", *rep.Verify)
	}
	// And when the re-run still fails, the claim is rejected with its exit.
	e2 := newE2E(t, "", []hd{result("fixer", "resolved")}, chainOf("fixer"))
	code, _, _ = e2.wrap("fixer", helperArgv("exit", "code=9"))
	if a := e2.onDisk().Attempts[0]; code != 9 || a.Reason != "resolved → verify failed (exit 9)" {
		t.Errorf("exit=%d attempt=%+v", code, a)
	}
}

func TestVerifyTimeoutFailsTheAttempt(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "pid")
	e := newE2E(t, "", []hd{{name: "fixer", argv: helperArgv("result", "outcome=resolved"), timeout: "3s"}}, chainOf("fixer"))
	start := time.Now()
	code, _, _ := e.wrap("fixer", helperArgv("exit", "code=4"), "--verify", helperShell("sleep", "started="+pidFile, "secs=60"))
	if time.Since(start) > 10*time.Second {
		t.Errorf("verify was not bounded: %v", time.Since(start))
	}
	a := e.onDisk().Attempts[0]
	if code != 4 || a.Outcome != contract.Failed || a.Reason != "resolved → verify timed out after 3s" {
		t.Errorf("exit=%d attempt=%+v", code, a)
	}
	pid, _ := strconv.Atoi(waitForFile(t, pidFile))
	if !eventuallyDead(pid) {
		t.Errorf("the timed-out verify %d is still alive", pid)
	}
}

func TestVerifyEnvironmentOutputAndStdout(t *testing.T) {
	dir := t.TempDir()
	e := newE2E(t, "", []hd{result("fixer", "resolved")}, chainOf("fixer"))
	// The record helper prints nothing; add noise on both streams.
	verify := `echo VERIFY-STDOUT; echo VERIFY-STDERR >&2; ` + helperShell("record", "dest="+dir+"/verify-rec")
	code, stdout, stderr := e.wrap("fixer", helperArgv("exit", "code=1", "out=cmd-out\n"), "--verify", verify)
	if code != 0 {
		t.Fatalf("exit = %d (stderr %s)", code, stderr)
	}
	if strings.Contains(stdout, "VERIFY") || strings.Contains(stderr, "VERIFY") {
		t.Errorf("verify output leaked to the caller: stdout=%q stderr=%q", stdout, stderr)
	}
	if stdout != "cmd-out\n" {
		t.Errorf("stdout = %q; want only the original run's output", stdout)
	}
	a := e.onDisk().Attempts[0]
	b, err := os.ReadFile(a.VerifyOutputFile)
	if err != nil || string(b) != "VERIFY-STDOUT\nVERIFY-STDERR\n" {
		t.Errorf("verify log = %q (%v)", b, err)
	}
	env := readRecord(t, filepath.Join(dir, "verify-rec")).Env
	if env["PG_RESCUE_HANDLER"] != "fixer" || env["PG_RESCUE_POSITION"] != "1" || env["PG_RESCUE_EXIT"] != "1" ||
		env["PG_RESCUE_REPORT"] == "" || env["PG_RESCUE_RUN_ID"] != e.result().Report.RunID {
		t.Errorf("verify env = %v", env)
	}
}

func TestVerifyInheritsStdinLikeTheCommand(t *testing.T) {
	e := newE2E(t, "", []hd{result("fixer", "resolved")}, chainOf("fixer"))
	in, _ := os.CreateTemp(e.root, "in")
	_, _ = in.WriteString("magic")
	_, _ = in.Seek(0, 0)
	e.rt.Stdin = in
	// The command does not read stdin; verify reads it and passes only if it is "magic".
	code, _, stderr := e.wrap("fixer", helperArgv("exit", "code=1"), "--verify", `test "$(cat)" = magic`)
	if code != 0 {
		t.Errorf("exit = %d (stderr %s)", code, stderr)
	}
}

func TestStdoutAndStderrStaySeparateAndOutputLogKeepsArrivalOrder(t *testing.T) {
	e := newE2E(t, "", []hd{result("d", "declined", "summary=handler text")}, chainOf("d"))
	code, stdout, stderr := e.wrap("d", helperArgv("interleave", "code=2"))
	if code != 2 {
		t.Fatalf("exit %d", code)
	}
	// The display goes to stderr, after the command's own stderr; stdout holds
	// the command's stdout and nothing else.
	if stdout != "out1\nout2\nout3\n" || !strings.HasPrefix(stderr, "err1\nerr2\nerr3\n") {
		t.Errorf("stdout=%q stderr=%q", stdout, stderr)
	}
	if strings.Contains(stdout, "handler text") || strings.Contains(stdout, "pg-rescue") {
		t.Error("handler text must never reach the caller's stdout")
	}
	b, _ := os.ReadFile(e.onDisk().OutputFile)
	if string(b) != "out1\nerr1\nout2\nerr2\nout3\nerr3\n" {
		t.Errorf("output.log = %q", b)
	}
}

func TestQuietStillCapturesAndPassesOutputThrough(t *testing.T) {
	e := newE2E(t, "", []hd{result("d", "declined")}, chainOf("d"))
	code, stdout, stderr := e.wrap("d", helperArgv("exit", "code=1", "out=hello\n", "err=world\n"), "-q")
	if code != 1 || stdout != "hello\n" || stderr != "world\n" {
		t.Errorf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	b, _ := os.ReadFile(e.onDisk().OutputFile)
	if !strings.Contains(string(b), "hello") || !strings.Contains(string(b), "world") {
		t.Errorf("output.log = %q", b)
	}
}

func TestCaptureCapKeepsHeadAndTail(t *testing.T) {
	e := newE2E(t, "", []hd{result("d", "declined")}, chainOf("d"))
	e.exec.Limits.Head, e.exec.Limits.Tail = 20, 20
	code, stdout, _ := e.wrap("d", helperArgv("lines", "n=30", "code=1"))
	if code != 1 {
		t.Fatalf("exit %d", code)
	}
	var full strings.Builder
	for i := 1; i <= 30; i++ {
		full.WriteString("line " + strconv.Itoa(i) + "\n")
	}
	if stdout != full.String() {
		t.Error("the passthrough must not be capped")
	}
	b, _ := os.ReadFile(e.onDisk().OutputFile)
	got := string(b)
	omitted := full.Len() - 40
	marker := "\n[pg-rescue: output truncated: " + strconv.Itoa(omitted) + " bytes omitted; kept the first 20 and the last 20]\n"
	if want := full.String()[:20] + marker + full.String()[full.Len()-20:]; got != want {
		t.Errorf("output.log = %q\nwant %q", got, want)
	}
}

func TestStdinInputIsCappedToo(t *testing.T) {
	e := newE2E(t, "", []hd{result("d", "declined")}, chainOf("d"))
	e.exec.Limits.Head, e.exec.Limits.Tail = 10, 10
	in, _ := os.CreateTemp(e.root, "in")
	_, _ = in.WriteString(strings.Repeat("a", 50) + strings.Repeat("b", 10))
	_, _ = in.Seek(0, 0)
	e.rt.Stdin = in
	e.wrap("d", nil, "--stdin", "--verify", "true")
	b, _ := os.ReadFile(e.onDisk().OutputFile)
	if !strings.HasPrefix(string(b), strings.Repeat("a", 10)+"\n[pg-rescue: output truncated") || !strings.HasSuffix(string(b), "]\n"+strings.Repeat("b", 10)) {
		t.Errorf("output.log = %q", b)
	}
}

func TestInheritedStdinReachesTheCommand(t *testing.T) {
	e := newE2E(t, "", []hd{result("d", "declined")}, chainOf("d"))
	in, _ := os.CreateTemp(e.root, "in")
	_, _ = in.WriteString("typed by the user\n")
	_, _ = in.Seek(0, 0)
	e.rt.Stdin = in
	code, stdout, _ := e.wrap("d", helperArgv("cat", "code=1"))
	if code != 1 || stdout != "typed by the user\n" {
		t.Errorf("exit=%d stdout=%q", code, stdout)
	}
}

func TestOutputTailBoundaries(t *testing.T) {
	cases := []struct {
		name      string
		args      []string
		check     func(t *testing.T, tail string)
		wantLines int
	}{
		{"exactly 200 lines keeps all", []string{"lines", "n=200", "code=1"}, func(t *testing.T, tail string) {
			if !strings.HasPrefix(tail, "line 1\n") || !strings.HasSuffix(tail, "line 200\n") {
				t.Errorf("tail = %q...%q", tail[:10], tail[len(tail)-12:])
			}
		}, 200},
		{"201 lines drops the first", []string{"lines", "n=201", "code=1"}, func(t *testing.T, tail string) {
			if !strings.HasPrefix(tail, "line 2\n") || !strings.HasSuffix(tail, "line 201\n") {
				t.Errorf("tail starts %q", tail[:10])
			}
		}, 200},
		{"no final newline", []string{"lines", "n=250", "final_newline=0", "code=1"}, func(t *testing.T, tail string) {
			if strings.HasSuffix(tail, "\n") || !strings.HasSuffix(tail, "line 250") || !strings.HasPrefix(tail, "line 51\n") {
				t.Errorf("tail = %q...%q", tail[:10], tail[len(tail)-12:])
			}
		}, 200},
		{"output far beyond the window", []string{"lines", "n=30000", "code=1"}, func(t *testing.T, tail string) {
			if !strings.HasPrefix(tail, "line 29801\n") || !strings.HasSuffix(tail, "line 30000\n") {
				t.Errorf("tail = %q...%q", tail[:12], tail[len(tail)-12:])
			}
		}, 200},
		{"invalid UTF-8", []string{"bytes", "hex=6162ff63fe0a", "code=1"}, func(t *testing.T, tail string) {
			if !utf8.ValidString(tail) || tail != "ab\uFFFDc\uFFFD\n" {
				t.Errorf("tail = %q", tail)
			}
		}, 1},
		{"one huge line is cut to 64 KiB", []string{"bytes", "b=x", "n=102400", "code=1"}, func(t *testing.T, tail string) {
			if len(tail) != 64<<10 || strings.Trim(tail, "x") != "" {
				t.Errorf("len = %d", len(tail))
			}
		}, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newE2E(t, "", []hd{result("d", "declined")}, chainOf("d"))
			e.wrap("d", helperArgv(c.args[0], c.args[1:]...))
			rep := e.onDisk() // also proves report.json is valid JSON whatever the output was
			c.check(t, rep.OutputTail)
			if n := strings.Count(strings.TrimSuffix(rep.OutputTail, "\n"), "\n") + 1; n != c.wantLines {
				t.Errorf("lines = %d; want %d", n, c.wantLines)
			}
			if len(rep.OutputTail) > 64<<10 {
				t.Errorf("tail is %d bytes", len(rep.OutputTail))
			}
		})
	}
}

func TestRedactAppliesToCopiesOnly(t *testing.T) {
	top := `redact = ['sekrit-[0-9]+', 'Bearer \S+']`
	longDetails := "sekrit-456 " + strings.Repeat("d", 20000)
	e := newE2E(t, top, []hd{result("d", "declined", "summary=see sekrit-789", "details="+longDetails)}, chainOf("d"))
	_, stdout, _ := e.wrap("d", helperArgv("exit", "code=1", "out=token sekrit-123 and Bearer abc.def\n"))
	rep := e.onDisk()
	if strings.Contains(rep.OutputTail, "sekrit") || strings.Contains(rep.OutputTail, "abc.def") ||
		rep.OutputTail != "token [REDACTED] and [REDACTED]\n" {
		t.Errorf("output_tail = %q", rep.OutputTail)
	}
	a := rep.Attempts[0]
	if strings.Contains(a.Reported.Summary, "sekrit") || a.Reported.Summary != "see [REDACTED]" {
		t.Errorf("summary = %q", a.Reported.Summary)
	}
	if strings.Contains(a.Reported.Details, "sekrit") || !strings.HasPrefix(a.Reported.Details, "[REDACTED] d") {
		t.Errorf("details = %.40q", a.Reported.Details)
	}
	// The raw copies stay raw.
	if !strings.Contains(stdout, "sekrit-123") {
		t.Error("the command's passthrough is untouched")
	}
	if b, _ := os.ReadFile(rep.OutputFile); !strings.Contains(string(b), "sekrit-123") {
		t.Error("output.log must stay raw")
	}
	if b, _ := os.ReadFile(a.Reported.DetailsFile); string(b) != longDetails {
		t.Errorf("details_file has %d bytes; want the full raw %d", len(b), len(longDetails))
	}
}

func TestDetailsAreCappedInTheReportAndFullInTheFile(t *testing.T) {
	details := strings.Repeat("é", 10000) // 20000 bytes of 2-byte runes
	e := newE2E(t, "", []hd{result("d", "declined", "details="+details)}, chainOf("d"))
	e.wrap("d", helperArgv("exit", "code=1"))
	a := e.onDisk().Attempts[0]
	if len(a.Reported.Details) > 16<<10 || !utf8.ValidString(a.Reported.Details) || !strings.HasPrefix(details, a.Reported.Details) {
		t.Errorf("details = %d bytes valid=%v", len(a.Reported.Details), utf8.ValidString(a.Reported.Details))
	}
	if b, _ := os.ReadFile(a.Reported.DetailsFile); string(b) != details {
		t.Errorf("details_file = %d bytes", len(b))
	}
	// Short details are not truncated and still get a file.
	e2 := newE2E(t, "", []hd{result("d", "declined", "details=short")}, chainOf("d"))
	e2.wrap("d", helperArgv("exit", "code=1"))
	if a := e2.onDisk().Attempts[0]; a.Reported.Details != "short" || a.Reported.DetailsFile == "" {
		t.Errorf("attempt = %+v", a)
	}
}

func TestReportFactsAndSchemaShape(t *testing.T) {
	e := newE2E(t, "", []hd{{name: "d", argv: helperArgv("result", "outcome=declined"), tags: []string{"deterministic"}}}, map[string][]string{"sync": {"d"}})
	code, _, _ := e.run("--config", e.cfgPath, "--chain", "sync", "--context", "ctx", "--verify", "true", "--", "sh", "-c", "echo to-stdout; exit 3")
	rep := e.onDisk()
	if code != 3 || rep.SchemaVersion != report.SchemaVersion || rep.Chain == nil || *rep.Chain != "sync" ||
		rep.Mode != report.ModeArgv || rep.Command == nil || rep.Command.Exit != 3 || rep.Command.Cwd != e.cwd ||
		rep.Context != "ctx" || rep.Verify == nil || *rep.Verify != "true" || rep.Host != "testhost" ||
		rep.StartedAt.Format(time.RFC3339) != "2026-10-02T14:03:11Z" || rep.Depth != 1 || rep.ParentRunID != nil {
		t.Errorf("report = %+v", rep)
	}
	if a := rep.Attempts[0]; a.Handler != "d" || a.Position != 1 || len(a.Tags) != 1 || a.Tags[0] != "deterministic" || a.Exit != 2 ||
		a.Outcome != contract.Declined || a.StderrFile == "" {
		t.Errorf("attempt = %+v", a)
	}
	raw, _ := os.ReadFile(filepath.Join(e.rdir(), "report.json"))
	for _, key := range []string{`"attempts": [`, `"parent_run_id": null`, `"output_tail": "to-stdout\n"`, `"tags": [`} {
		if !strings.Contains(string(raw), key) {
			t.Errorf("report.json lacks %s:\n%s", key, raw)
		}
	}
}
