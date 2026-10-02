package display

// gaps_test.go closes the assertion gaps pg-go-mutate reported against the
// renderer: each test below kills a surviving mutant by asserting something
// the first round of tests left unchecked. The survivors that remain are
// equivalent mutants (a string compared with "" by < or >, a level compared
// with the maximum level by ==, a length compared with 0 by <=).

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-rescue/internal/cli"
	"github.com/phillipgreenii/pg-rescue/internal/config"
	"github.com/phillipgreenii/pg-rescue/internal/contract"
	"github.com/phillipgreenii/pg-rescue/internal/report"
	"github.com/phillipgreenii/pg-rescue/internal/runner"
)

func TestEachSubSectionAppearsExactlyWhereItHasContent(t *testing.T) {
	in, files := scenario()
	count := func(s, sub string) int { return strings.Count(s, sub) }

	v := render(t, in, files, cli.Verbose)
	if n := count(v, "-- details --"); n != 2 { // fix-small and fix-large only
		t.Errorf("-v: details sections = %d; want 2", n)
	}
	if n := count(v, "-- verify:"); n != 2 { // the two attempts that ran verify
		t.Errorf("-v: verify sections = %d; want 2", n)
	}
	if n := count(v, "skipped (chain stopped)"); n != 2 {
		t.Errorf("-v: skipped lines = %d; want 2:\n%s", n, v)
	}
	if strings.Contains(v, "== [1/5] flake-lock-conflict: skipped") || strings.Contains(v, "== [3/5] fix-large: skipped") {
		t.Errorf("-v: only the handlers after the last attempt are skipped:\n%s", v)
	}

	vv := render(t, in, files, cli.VeryVerbose)
	for sub, want := range map[string]int{
		"-- argv --": 3, "-- details --": 2, "-- meta --": 1, "-- stderr --": 1, "-- verify:": 2,
		"skipped (chain stopped)": 2,
	} {
		if n := count(vv, sub); n != want {
			t.Errorf("-vv: %q appears %d times; want %d:\n%s", sub, n, want, vv)
		}
	}
	// A failed verify is still shown at -vv, with its output.
	if !strings.Contains(vv, "-- verify: failed (exit 128, 900ms) --\nerror: cannot pull with rebase: You have unstaged changes.\n") {
		t.Errorf("-vv lacks the failed verify:\n%s", vv)
	}
	d := render(t, in, files, cli.Normal)
	if strings.Contains(d, "skipped") || strings.Contains(d, "-- details --") {
		t.Errorf("the default level shows neither skipped handlers nor details:\n%s", d)
	}
}

func TestAHandlerWithNoConfiguredCommandHasNoArgvSection(t *testing.T) {
	in, files := scenario()
	in.Handlers["fix-large"].Command = nil
	if got := render(t, in, files, cli.VeryVerbose); strings.Count(got, "-- argv --") != 2 {
		t.Errorf("an empty command must not produce an argv section:\n%s", got)
	}
}

func TestVerifyWithoutAnOutputFileOrWithoutATiming(t *testing.T) {
	// Verify that could not create its output file has a path but no timing.
	in, files := scenario()
	a := &in.Result.Report.Attempts[0]
	a.VerifyOutputFile, a.VerifyMS = "/run/attempt-1.verify.log", nil
	a.Reason = "resolved → verify could not start: cannot create the output file: boom"
	a.Outcome = contract.Failed
	got := render(t, in, files, cli.Verbose)
	if !strings.Contains(got, "-- verify: failed (could not start: cannot create the output file: boom) --\n") {
		t.Errorf("a verify with a path but no timing must still be shown:\n%s", got)
	}
	// ... and one with a timing but no output file.
	in, files = scenario()
	a = &in.Result.Report.Attempts[0]
	a.VerifyMS = new(int64)
	a.VerifyOutputFile = ""
	a.Reason = "resolved → verify failed (exit 3)"
	got = render(t, in, files, cli.Verbose)
	if !strings.Contains(got, "== [1/5] flake-lock-conflict: declined (verify failed: exit 3) ==\nConflict spans source files, not just flake.lock\n-- verify: failed (exit 3) --\n") {
		t.Errorf("a verify with a timing but no output file must still be shown:\n%s", got)
	}
}

func TestOnlyTheExactPassedReasonCountsAsPassed(t *testing.T) {
	in, files := scenario()
	in.Result.Report.Attempts[2].Reason = "zzz not the passed reason"
	got := render(t, in, files, cli.Verbose)
	if !strings.Contains(got, "-- verify: failed (zzz not the passed reason) --") {
		t.Errorf("verify state is read from the exact reason:\n%s", got)
	}
}

func TestDefaultReaderReadsRealFiles(t *testing.T) {
	dir := t.TempDir()
	stderr := filepath.Join(dir, "attempt-3.stderr.log")
	if err := os.WriteFile(stderr, []byte("from disk\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	in, _ := scenario()
	in.Result.Report.Attempts[2].StderrFile = stderr
	in.ReadFile = nil // the production default
	if got := Render(in, cli.VeryVerbose); !strings.Contains(got, "-- stderr --\nfrom disk\n") {
		t.Errorf("the default reader should read real files:\n%s", got)
	}
}

func TestParentheticalReasonsThatLookLikeTrivialOnes(t *testing.T) {
	r := &renderer{in: Input{Redact: func(s string) string { return s }}}
	h := &config.Handler{Timeout: time.Minute}
	for _, c := range []struct {
		name  string
		a     report.Attempt
		level cli.Verbosity
		want  string
	}{
		{
			"a reason that extends 'exit N' is not trivial",
			report.Attempt{Outcome: contract.Failed, Reason: "exit 1; stdout ignored: bad", Exit: 1},
			cli.VeryVerbose,
			" (exit 1; stdout ignored: bad, exit 1, 0ms, timeout 1m)",
		},
		{
			"a reason naming a different exit is not trivial",
			report.Attempt{Outcome: contract.Failed, Reason: "exit 7", Exit: 8},
			cli.VeryVerbose,
			" (exit 7, exit 8, 0ms, timeout 1m)",
		},
		{
			"a reason that extends 'verify passed' is not trivial",
			report.Attempt{Outcome: contract.Resolved, Reason: "verify passed twice"},
			cli.Normal,
			" (verify passed twice)",
		},
		{
			"a reason that sorts before 'verify passed' is not trivial",
			report.Attempt{Outcome: contract.Deferred, Reason: "a custom reason"},
			cli.Normal,
			" (a custom reason)",
		},
		{
			"a declined attempt with no reason has no parentheses",
			report.Attempt{Outcome: contract.Declined},
			cli.Normal, "",
		},
		{
			"a failed attempt with no reason has no parentheses",
			report.Attempt{Outcome: contract.Failed, Exit: -1},
			cli.Normal, "",
		},
		{
			"-vv without a description",
			report.Attempt{Outcome: contract.Declined, Reason: "exit 2", Exit: 2, DurationMS: 2000},
			cli.VeryVerbose,
			" (exit 2, 2.0s, timeout 1m)",
		},
	} {
		r.level = c.level
		if got := r.parenthetical(&c.a, h); got != c.want {
			t.Errorf("%s: %q; want %q", c.name, got, c.want)
		}
	}
}

func TestDurationAndTimeoutEdges(t *testing.T) {
	for d, want := range map[time.Duration]string{
		2*time.Hour + 5*time.Minute + 7*time.Second:  "2h5m7s",
		3*time.Hour + 59*time.Minute + 1*time.Second: "3h59m1s",
	} {
		if got := Duration(d); got != want {
			t.Errorf("Duration(%v) = %q; want %q", d, got, want)
		}
	}
	if got := Timeout(-time.Second); got != "none" {
		t.Errorf("Timeout(-1s) = %q; want none", got)
	}
}

func TestSanitizeStripsTheFirstC1Control(t *testing.T) {
	if got := Sanitize("a\u0080b\u00a0c"); got != "ab\u00a0c" { // U+0080 goes, U+00A0 (no-break space) stays
		t.Errorf("Sanitize = %q", got)
	}
}

func TestRunnerTailConstantsMatchTheReport(t *testing.T) {
	if tailLines != 200 || tailBytes != 64<<10 || runner.TailLines != 200 {
		t.Errorf("tail caps = %d lines, %d bytes", tailLines, tailBytes)
	}
}
