package contracttest

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-rescue/internal/contract"
	"github.com/phillipgreenii/pg-rescue/internal/report"
)

// ---- the trivial fake handler: this test binary, re-executed ---------------
//
// TestHelperProcess is not a test. When GO_WANT_HELPER_PROCESS=1 it plays a
// handler, behaving as its first argument after "--" says.

func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	args := os.Args
	for i, a := range args {
		if a == "--" {
			args = args[i+1:]
			break
		}
	}
	print := func(r contract.Result) {
		b, err := r.Render()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(70)
		}
		_, _ = os.Stdout.Write(b)
	}
	switch args[0] {
	case "describe": // resolve, describing the report and environment it was given
		b, err := os.ReadFile(os.Getenv("PG_RESCUE_REPORT"))
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		var r report.Report
		_ = json.Unmarshal(b, &r)
		if r.SchemaVersion != report.SchemaVersion {
			fmt.Fprintf(os.Stderr, "unsupported schema_version %d\n", r.SchemaVersion)
			os.Exit(1) // the documented way to refuse a version you do not know
		}
		wd, _ := os.Getwd()
		print(contract.Result{Outcome: contract.Resolved, Summary: fmt.Sprintf(
			"run=%s handler=%s position=%s depth=%s parent=%s attempts=%d mode=%s cmd=[%s] exit=[%s] cwd=%s",
			os.Getenv("PG_RESCUE_RUN_ID"), os.Getenv("PG_RESCUE_HANDLER"), os.Getenv("PG_RESCUE_POSITION"),
			os.Getenv("PG_RESCUE_DEPTH"), os.Getenv("PG_RESCUE_PARENT_RUN_ID"), len(r.Attempts), r.Mode,
			os.Getenv("PG_RESCUE_CMD"), os.Getenv("PG_RESCUE_EXIT"), wd,
		)})
	case "exit": // exit with a code and print the rest of the args as stdout
		fmt.Print(strings.Join(args[2:], " "))
		code := 0
		_, _ = fmt.Sscan(args[1], &code)
		os.Exit(code)
	case "env":
		print(contract.Result{Outcome: contract.Declined, Summary: os.Getenv(args[1])})
		os.Exit(contract.ExitDeclined)
	case "stdin":
		b, _ := io.ReadAll(os.Stdin)
		print(contract.Result{Outcome: contract.Deferred, Summary: fmt.Sprintf("stdin bytes=%d", len(b))})
		os.Exit(contract.ExitDeferred)
	case "big": // 2 MiB on stdout; the harness must drain it without blocking
		chunk := strings.Repeat("a", 1<<16)
		for i := 0; i < 32; i++ {
			fmt.Print(chunk)
		}
		os.Exit(0)
	case "hang":
		time.Sleep(time.Minute)
	}
	os.Exit(0)
}

func fake(mode string, args ...string) []string {
	return append([]string{os.Args[0], "-test.run=^TestHelperProcess$", "--", mode}, args...)
}

var helperEnv = []string{"GO_WANT_HELPER_PROCESS=1"}

// ---- recording TB, to observe failures Run reports without failing this test

type recorder struct {
	testing.TB
	errs, fatals []string
}

func (r *recorder) Errorf(format string, a ...any) {
	r.errs = append(r.errs, fmt.Sprintf(format, a...))
}

func (r *recorder) Fatalf(format string, a ...any) {
	r.fatals = append(r.fatals, fmt.Sprintf(format, a...))
	runtime.Goexit()
}

// runRecorded calls Run in its own goroutine, so a Fatalf can end it.
func runRecorded(t *testing.T, argv []string, fixture string, env []string) (res Result, rec *recorder) {
	t.Helper()
	rec = &recorder{TB: t}
	done := make(chan struct{})
	go func() {
		defer close(done)
		res = Run(rec, argv, fixture, env)
	}()
	<-done
	return res, rec
}

// ---- tests -----------------------------------------------------------------

func TestRunDeliversEachCannedReport(t *testing.T) {
	for _, tc := range []struct {
		fixture string
		want    []string
	}{
		{"first-attempt", []string{"run=20261002T140311Z-7f3a9c2e", "handler=flake-lock-conflict", "position=1", "depth=1", "parent=", "attempts=0", "mode=argv", "cmd=[git pull --rebase]", "exit=[1]"}},
		{"three-prior-attempts", []string{"handler=p1-later", "position=4", "depth=2", "parent=20261002T135900Z-0b1c2d3e", "attempts=3", "mode=argv"}},
		{"stdin-mode", []string{"run=20261002T150000Z-00ff00ff", "handler=notify", "position=1", "attempts=0", "mode=stdin", "cmd=[]", "exit=[]"}},
	} {
		t.Run(tc.fixture, func(t *testing.T) {
			res := Run(t, fake("describe"), Fixture(tc.fixture), helperEnv)
			if res.Exit != 0 || res.Outcome != contract.Resolved || res.Reason != "exit 0" {
				t.Fatalf("result = %+v", res)
			}
			for _, w := range tc.want {
				if !strings.Contains(res.Reported.Summary, w+" ") && !strings.HasSuffix(res.Reported.Summary, w) {
					t.Errorf("handler did not see %q; summary: %s", w, res.Reported.Summary)
				}
			}
			b, err := os.ReadFile(res.ReportPath)
			if err != nil {
				t.Fatal(err)
			}
			orig, _ := os.ReadFile(Fixture(tc.fixture))
			if string(b) != string(orig) {
				t.Error("PG_RESCUE_REPORT must hold the fixture byte for byte")
			}
		})
	}
}

// An unknown schema_version is a fixture a handler is allowed to refuse: exit 1
// is outside the outcome table, so Run accepts it and hands the verdict back.
func TestRunAcceptsAHandlerThatRefusesAnUnknownSchemaVersion(t *testing.T) {
	res, rec := runRecorded(t, fake("describe"), Fixture("unknown-schema-version"), helperEnv)
	if len(rec.errs)+len(rec.fatals) != 0 {
		t.Fatalf("a refusal is not a contract violation: %v %v", rec.errs, rec.fatals)
	}
	if res.Exit != 1 || res.Outcome != contract.Failed || res.Reason != "exit 1" || !strings.Contains(res.Stderr, "schema_version 2") {
		t.Errorf("result = %+v", res)
	}
}

func TestRunMapsBareExitCodes(t *testing.T) {
	for code, want := range map[string]contract.Outcome{"0": contract.Resolved, "2": contract.Declined, "3": contract.Deferred, "1": contract.Failed, "75": contract.Failed} {
		res := Run(t, fake("exit", code), Fixture("first-attempt"), helperEnv)
		if res.Outcome != want || res.Reason != "exit "+code {
			t.Errorf("exit %s: %+v; want outcome %s", code, res, want)
		}
	}
}

func TestRunReportsClassifyRejections(t *testing.T) {
	for name, tc := range map[string]struct {
		argv []string
		want string
	}{
		"log text on stdout":  {fake("exit", "0", "Successfully rebased"), "handlers must log to stderr"},
		"disagreeing outcome": {fake("exit", "2", `{"outcome":"resolved"}`), `disagrees with exit code 2`},
		"two objects":         {fake("exit", "3", `{} {}`), "data after the JSON object"},
		"oversized result":    {fake("big"), "1 MiB cap"},
	} {
		t.Run(name, func(t *testing.T) {
			res, rec := runRecorded(t, tc.argv, Fixture("first-attempt"), helperEnv)
			if res.Outcome != contract.Failed {
				t.Errorf("outcome = %s; want failed", res.Outcome)
			}
			if len(rec.fatals) != 0 || len(rec.errs) != 1 || !strings.Contains(rec.errs[0], tc.want) {
				t.Errorf("want exactly one contract error mentioning %q; got errs=%v fatals=%v", tc.want, rec.errs, rec.fatals)
			}
		})
	}
}

func TestRunDrainsOversizedStdoutAndFlagsTruncation(t *testing.T) {
	res, _ := runRecorded(t, fake("big"), Fixture("first-attempt"), helperEnv)
	if !res.Truncated || len(res.Stdout) != contract.MaxResultBytes || res.Exit != 0 {
		t.Errorf("truncated=%v kept=%d exit=%d; want the first 1 MiB kept and the handler never blocked", res.Truncated, len(res.Stdout), res.Exit)
	}
}

func TestRunEnvironmentAndStdin(t *testing.T) {
	res := Run(t, fake("env", "MY_EXTRA"), Fixture("first-attempt"), append([]string{"MY_EXTRA=from-caller"}, helperEnv...))
	if res.Reported.Summary != "from-caller" || res.Outcome != contract.Declined {
		t.Errorf("env passed through Run's env argument: %+v", res)
	}
	// The env argument comes last, so it overrides the harness's own values.
	res = Run(t, fake("env", "PG_RESCUE_HANDLER"), Fixture("first-attempt"), append([]string{"PG_RESCUE_HANDLER=override"}, helperEnv...))
	if res.Reported.Summary != "override" {
		t.Errorf("caller env must win: %+v", res)
	}
	res = Run(t, fake("stdin"), Fixture("first-attempt"), helperEnv)
	if res.Reported.Summary != "stdin bytes=0" || res.Outcome != contract.Deferred {
		t.Errorf("stdin must be /dev/null: %+v", res)
	}
}

func TestRunWorksInTheReportsCwdWhenItExists(t *testing.T) {
	dir := t.TempDir()
	raw, _ := os.ReadFile(Fixture("first-attempt"))
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	m["command"].(map[string]any)["cwd"] = dir
	custom := t.TempDir() + "/report.json"
	b, _ := json.Marshal(m)
	if err := os.WriteFile(custom, b, 0o600); err != nil {
		t.Fatal(err)
	}
	res := Run(t, fake("describe"), custom, helperEnv)
	_, got, _ := strings.Cut(res.Reported.Summary, "cwd=")
	// macOS reports /private/var for /var, so compare resolved paths.
	gotReal, _ := filepath.EvalSymlinks(got)
	wantReal, _ := filepath.EvalSymlinks(dir)
	if gotReal == "" || gotReal != wantReal {
		t.Errorf("handler cwd = %q; want the report's command cwd %q", got, dir)
	}
	// A cwd that does not exist falls back to a scratch directory instead of failing.
	m["command"].(map[string]any)["cwd"] = "/nonexistent/dir"
	b, _ = json.Marshal(m)
	_ = os.WriteFile(custom, b, 0o600)
	res = Run(t, fake("describe"), custom, helperEnv)
	if res.Outcome != contract.Resolved {
		t.Errorf("missing cwd: %+v", res)
	}
}

func TestRunFailsOnAMissingFixtureOrBinaryAndOnTimeout(t *testing.T) {
	_, rec := runRecorded(t, fake("exit", "0"), "/nonexistent/report.json", helperEnv)
	if len(rec.fatals) != 1 || !strings.Contains(rec.fatals[0], "report fixture") {
		t.Errorf("missing fixture: %v", rec.fatals)
	}
	_, rec = runRecorded(t, []string{"/nonexistent/handler"}, Fixture("first-attempt"), nil)
	if len(rec.fatals) != 1 || !strings.Contains(rec.fatals[0], "running") {
		t.Errorf("missing binary: %v", rec.fatals)
	}
	old := runTimeout
	runTimeout = 300 * time.Millisecond
	defer func() { runTimeout = old }()
	start := time.Now()
	_, rec = runRecorded(t, fake("hang"), Fixture("first-attempt"), helperEnv)
	if len(rec.fatals) != 1 || time.Since(start) > 10*time.Second {
		t.Errorf("a hung handler must be killed and reported: fatals=%v after %v", rec.fatals, time.Since(start))
	}
}

func TestFixturesExist(t *testing.T) {
	for _, n := range []string{"first-attempt", "three-prior-attempts", "stdin-mode", "unknown-schema-version"} {
		if _, err := os.Stat(Fixture(n)); err != nil {
			t.Errorf("canned report %s: %v", n, err)
		}
	}
}
