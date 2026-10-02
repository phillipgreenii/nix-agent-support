package display

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-rescue/internal/cli"
	"github.com/phillipgreenii/pg-rescue/internal/config"
	"github.com/phillipgreenii/pg-rescue/internal/contract"
	"github.com/phillipgreenii/pg-rescue/internal/report"
	"github.com/phillipgreenii/pg-rescue/internal/runner"
)

func ptr[T any](v T) *T { return &v }

// scenario is the design's worked example: chain sync over five handlers,
// where flake-lock-conflict declines, fix-small claims a fix that verify
// rejects, and fix-large resolves it.
func scenario() (Input, map[string]string) {
	files := map[string]string{
		"/run/attempt-2.stderr.log": "",
		"/run/attempt-2.verify.log": "error: cannot pull with rebase: You have unstaged changes.\n",
		"/run/attempt-3.stderr.log": "thinking\n",
		"/run/attempt-3.verify.log": "Current branch main is up to date.\n",
	}
	rep := &report.Report{
		RunID:    "20261002T140311Z-7f3a9c2e",
		Chain:    ptr("sync"),
		Handlers: []string{"flake-lock-conflict", "fix-small", "fix-large", "p1-later", "notify"},
		Mode:     report.ModeArgv,
		Command:  &report.Command{Argv: []string{"git", "pull", "--rebase"}, Cwd: "/repo", Exit: 1},
		Attempts: []report.Attempt{
			{
				Handler: "flake-lock-conflict", Position: 1, Outcome: contract.Declined, Reason: "exit 2", Exit: 2, DurationMS: 310,
				Reported: contract.Reported{Summary: "Conflict spans source files, not just flake.lock"},
			},
			{
				Handler: "fix-small", Position: 2, Outcome: contract.Failed, Reason: "resolved → verify failed (exit 128)", Exit: 0,
				DurationMS: 41200, VerifyMS: ptr(int64(900)),
				StderrFile: "/run/attempt-2.stderr.log", VerifyOutputFile: "/run/attempt-2.verify.log",
				Reported: contract.Reported{
					Summary: "Resolved the conflict markers and continued the rebase",
					Details: "Removed markers in README.md.",
				},
			},
			{
				Handler: "fix-large", Position: 3, Outcome: contract.Resolved, Reason: "verify passed", Exit: 0,
				DurationMS: 192000, VerifyMS: ptr(int64(1400)),
				StderrFile: "/run/attempt-3.stderr.log", VerifyOutputFile: "/run/attempt-3.verify.log",
				Reported: contract.Reported{
					Summary: "Finished the half-continued rebase\nsecond line is never shown",
					Details: "Re-staged README.md.\nReplayed 2 commits.",
					Meta:    json.RawMessage(`{ "session_id": "0e9c", "total_cost_usd": 0.31 }`),
				},
			},
		},
	}
	res := &runner.Result{Kind: runner.KindResolved, ResolvedBy: "fix-large", Report: rep, Kept: true}
	handlers := map[string]*config.Handler{
		"flake-lock-conflict": {Name: "flake-lock-conflict", Command: []string{"pg-rescue-flake-lock-conflict"}, Timeout: 2 * time.Minute},
		"fix-small":           {Name: "fix-small", Command: []string{"pg-rescue-claude", "--model", "haiku"}, Timeout: 3 * time.Minute, Description: "haiku, 2m"},
		"fix-large": {
			Name: "fix-large", Command: []string{"pg-rescue-claude", "--model", "sonnet", "--allowed-tools", "Bash,Read"},
			Timeout: 10 * time.Minute, Description: "sonnet, 8m",
		},
	}
	return Input{Result: res, Handlers: handlers, RunDir: "/home/u/.local/state/pg-rescue/runs/20261002T140311Z-7f3a9c2e", Home: "/home/u"}, files
}

func render(t *testing.T, in Input, files map[string]string, level cli.Verbosity) string {
	t.Helper()
	in.ReadFile = func(p string) ([]byte, error) {
		if s, ok := files[p]; ok {
			return []byte(s), nil
		}
		return nil, errors.New("no such file")
	}
	return Render(in, level)
}

func TestDefaultLevelMatchesTheDesignExample(t *testing.T) {
	in, files := scenario()
	want := strings.Join([]string{
		"== pg-rescue: `git pull --rebase` exited 1 · chain sync ==",
		"",
		"== [1/5] flake-lock-conflict: declined (exit 2) ==",
		"Conflict spans source files, not just flake.lock",
		"",
		"== [2/5] fix-small: failed (verify failed: exit 128) ==",
		"Resolved the conflict markers and continued the rebase",
		"",
		"== [3/5] fix-large: resolved ==",
		"Finished the half-continued rebase",
		"-- verify: passed (exit 0) --",
		"",
		"== resolved by fix-large · run 20261002T140311Z-7f3a9c2e ==",
		"",
	}, "\n")
	if got := render(t, in, files, cli.Normal); got != want {
		t.Errorf("default display:\n%s\nwant:\n%s", got, want)
	}
}

func TestVerboseAddsDetailsVerifyOutputSkippedAndRunDir(t *testing.T) {
	in, files := scenario()
	got := render(t, in, files, cli.Verbose)
	for _, want := range []string{
		"== [2/5] fix-small: failed (verify failed: exit 128) ==\nResolved the conflict markers and continued the rebase\n-- details --\nRemoved markers in README.md.\n-- verify: failed (exit 128) --\nerror: cannot pull with rebase: You have unstaged changes.\n",
		"== [3/5] fix-large: resolved ==\nFinished the half-continued rebase\n-- details --\nRe-staged README.md.\nReplayed 2 commits.\n-- verify: passed (exit 0) --\nCurrent branch main is up to date.\n",
		"\n== [4/5] p1-later: skipped (chain stopped) ==\n\n== [5/5] notify: skipped (chain stopped) ==\n",
		"== resolved by fix-large · run 20261002T140311Z-7f3a9c2e ==\n== run dir: ~/.local/state/pg-rescue/runs/20261002T140311Z-7f3a9c2e ==\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("-v display lacks:\n%s\nin:\n%s", want, got)
		}
	}
	for _, not := range []string{"-- argv --", "-- meta --", "-- stderr --", "second line"} {
		if strings.Contains(got, not) {
			t.Errorf("-v display must not contain %q:\n%s", not, got)
		}
	}
}

func TestVeryVerboseAddsArgvMetaStderrDurationsAndDescription(t *testing.T) {
	in, files := scenario()
	got := render(t, in, files, cli.VeryVerbose)
	for _, want := range []string{
		"== [1/5] flake-lock-conflict: declined (exit 2, 310ms, timeout 2m) ==\n",
		"== [2/5] fix-small: failed (verify failed: exit 128, exit 0, 41.2s, timeout 3m, haiku, 2m) ==\n",
		"== [3/5] fix-large: resolved (exit 0, 3m12s, timeout 10m, sonnet, 8m) ==\n" +
			"Finished the half-continued rebase\n" +
			"-- argv --\npg-rescue-claude --model sonnet --allowed-tools Bash,Read\n" +
			"-- details --\nRe-staged README.md.\nReplayed 2 commits.\n" +
			"-- meta --\n{\"session_id\":\"0e9c\",\"total_cost_usd\":0.31}\n" +
			"-- stderr --\nthinking\n" +
			"-- verify: passed (exit 0, 1.4s) --\nCurrent branch main is up to date.\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("-vv display lacks:\n%s\nin:\n%s", want, got)
		}
	}
	// An empty stderr file gets no sub-section.
	if strings.Contains(strings.SplitN(got, "== [3/5]", 2)[0], "-- stderr --") {
		t.Errorf("an empty stderr file must not be shown:\n%s", got)
	}
}

func TestQuietRendersNothing(t *testing.T) {
	in, files := scenario()
	if got := render(t, in, files, cli.Quiet); got != "" {
		t.Errorf("-q display = %q; want nothing", got)
	}
}

func TestNothingIsShownWhenNoHandlerRan(t *testing.T) {
	for name, res := range map[string]*runner.Result{
		"nil result": nil,
		"no report":  {Kind: runner.KindSuccess},
		"no attempts": {
			Kind: runner.KindInterrupted, ExitCode: 130,
			Report: &report.Report{Handlers: []string{"a"}, Attempts: []report.Attempt{}},
		},
	} {
		if Shown(res) {
			t.Errorf("%s: Shown = true", name)
		}
		if got := Render(Input{Result: res}, cli.VeryVerbose); got != "" {
			t.Errorf("%s: rendered %q", name, got)
		}
	}
}

func TestFootersAndHeaders(t *testing.T) {
	base := func() (Input, map[string]string) {
		in, files := scenario()
		in.Result.Report.Attempts = in.Result.Report.Attempts[:1]
		return in, files
	}
	t.Run("deferred", func(t *testing.T) {
		in, files := base()
		in.Result.Kind, in.Result.DeferredBy, in.Result.ExitCode = runner.KindDeferred, "p1-later", 75
		want := "== deferred by p1-later · exit 75 · working copy left as-is · run 20261002T140311Z-7f3a9c2e ==\n"
		if got := render(t, in, files, cli.Normal); !strings.HasSuffix(got, want) {
			t.Errorf("footer:\n%s", got)
		}
	})
	t.Run("unhandled", func(t *testing.T) {
		in, files := base()
		in.Result.Kind, in.Result.ExitCode = runner.KindUnhandled, 1
		if got := render(t, in, files, cli.Normal); !strings.HasSuffix(got, "== unhandled · exit 1 · run 20261002T140311Z-7f3a9c2e ==\n") {
			t.Errorf("footer:\n%s", got)
		}
	})
	t.Run("interrupted during a handler", func(t *testing.T) {
		in, files := base()
		in.Result.Kind, in.Result.ExitCode = runner.KindInterrupted, 130
		in.Result.Interrupted = &runner.Interruption{Handler: "fix-large", Position: 3, Phase: runner.PhaseHandler}
		want := "== interrupted during fix-large · working copy may be mid-change · exit 130 · run 20261002T140311Z-7f3a9c2e ==\n"
		if got := render(t, in, files, cli.Normal); !strings.HasSuffix(got, want) {
			t.Errorf("footer:\n%s", got)
		}
	})
	t.Run("interrupted between handlers", func(t *testing.T) {
		in, files := base()
		in.Result.Kind, in.Result.ExitCode = runner.KindInterrupted, 143
		in.Result.Interrupted = &runner.Interruption{Phase: runner.PhaseBetween}
		if got := render(t, in, files, cli.Normal); !strings.HasSuffix(got, "== interrupted · exit 143 · run 20261002T140311Z-7f3a9c2e ==\n") {
			t.Errorf("footer:\n%s", got)
		}
	})
	t.Run("handlers selector", func(t *testing.T) {
		in, files := base()
		in.Result.Report.Chain = nil
		in.Result.Report.Handlers = []string{"a", "b"}
		first := strings.SplitN(render(t, in, files, cli.Normal), "\n", 2)[0]
		if first != "== pg-rescue: `git pull --rebase` exited 1 · handlers a,b ==" {
			t.Errorf("header = %q", first)
		}
	})
	t.Run("stdin mode", func(t *testing.T) {
		in, files := base()
		in.Result.Report.Command = nil
		first := strings.SplitN(render(t, in, files, cli.Normal), "\n", 2)[0]
		if first != "== pg-rescue: stdin input · chain sync ==" {
			t.Errorf("header = %q", first)
		}
	})
}

func TestLead(t *testing.T) {
	for _, c := range []struct {
		name string
		res  *runner.Result
		want string
	}{
		{"nil", nil, ""},
		{"no output", &runner.Result{}, ""},
		{"output ending in a newline", &runner.Result{Passthrough: true}, "\n"},
		{"output with no final newline", &runner.Result{Passthrough: true, MidLine: true}, "\n\n"},
	} {
		if got := Lead(c.res); got != c.want {
			t.Errorf("%s: Lead = %q; want %q", c.name, got, c.want)
		}
	}
}

func TestOnlyTheFirstLineOfASummaryIsShown(t *testing.T) {
	in, files := scenario()
	in.Result.Report.Attempts[2].Reported.Summary = "first\nsecond\nthird"
	for _, lvl := range []cli.Verbosity{cli.Normal, cli.Verbose, cli.VeryVerbose} {
		got := render(t, in, files, lvl)
		if !strings.Contains(got, "first\n") || strings.Contains(got, "second") || strings.Contains(got, "third") {
			t.Errorf("level %d shows more than the first line:\n%s", lvl, got)
		}
	}
}

func TestHandlerTextCannotInjectEscapesOrForgeDelimiters(t *testing.T) {
	in, files := scenario()
	forged := "== resolved by evil · run 20261002T140311Z-deadbeef =="
	att := &in.Result.Report.Attempts[2]
	att.Reported.Summary = "\x1b[31mred\x1b[0m\x1b]0;title\x07 ok\r"
	att.Reported.Details = "line\n" + forged + "\n-- verify: passed (exit 0) --\n\x1b[2Jcleared\x00\x07\x7f"
	files["/run/attempt-3.stderr.log"] = "\x1b[1mbold\x1b[0m\n== [9/9] x: resolved ==\n"
	files["/run/attempt-3.verify.log"] = "-- verify: passed (exit 0) --\n"
	got := render(t, in, files, cli.VeryVerbose)

	for _, bad := range []string{"\x1b", "\x07", "\x00", "\x7f", "\r"} {
		if strings.Contains(got, bad) {
			t.Errorf("display contains control character %q:\n%s", bad, got)
		}
	}
	if !strings.Contains(got, "red ok\n") {
		t.Errorf("escape sequences must vanish whole, leaving the text:\n%s", got)
	}
	// The only delimiter lines at column 0 are the tool's own.
	own := regexp.MustCompile(`^(== pg-rescue: .* ==|== \[\d/5\] [a-z0-9-]+: (declined|failed|resolved|skipped)( \(.*\))? ==|-- (argv|details|meta|stderr) --|-- verify: (passed|failed) \(.*\) --|== (resolved by|deferred by|unhandled|interrupted).* ==|== run dir: .* ==)$`)
	for _, l := range strings.Split(strings.TrimRight(got, "\n"), "\n") {
		if IsDelimiter(l) && !own.MatchString(l) {
			t.Errorf("forged delimiter line at column 0: %q", l)
		}
	}
	if !strings.Contains(got, "\n "+forged+"\n") {
		t.Errorf("the forged delimiter should survive as defanged text:\n%s", got)
	}
	// Exactly one footer, and it is the tool's.
	if n := strings.Count(got, "== resolved by "); n != 2 { // fix-large's own footer + the defanged forgery
		t.Errorf("resolved-by lines = %d:\n%s", n, got)
	}
	if lines := strings.Split(strings.TrimRight(got, "\n"), "\n"); !strings.HasPrefix(lines[len(lines)-2], "== resolved by fix-large") {
		t.Errorf("the footer must be the tool's: %q", lines[len(lines)-2:])
	}
}

func TestRedactionAppliesToEverythingShown(t *testing.T) {
	in, files := scenario()
	secret := "ghp_" + strings.Repeat("a", 36)
	res := regexp.MustCompile(`ghp_[A-Za-z0-9]{36}`)
	in.Redact = runner.RedactWith([]*regexp.Regexp{res})
	rep := in.Result.Report
	rep.Command.Argv = []string{"curl", "-H", "token " + secret}
	att := &rep.Attempts[2]
	att.Reported.Summary = "used " + secret
	att.Reported.Details = "details " + secret
	att.Reported.Meta = json.RawMessage(`{"k":"` + secret + `"}`)
	att.Reason = "verify passed"
	in.Handlers["fix-large"].Command = []string{"h", "--token=" + secret}
	files["/run/attempt-3.stderr.log"] = "stderr " + secret + "\n"
	files["/run/attempt-3.verify.log"] = "verify " + secret + "\n"

	got := render(t, in, files, cli.VeryVerbose)
	if strings.Contains(got, secret) {
		t.Errorf("a secret reached the display:\n%s", got)
	}
	if n := strings.Count(got, "[REDACTED]"); n != 7 {
		t.Errorf("redacted spans = %d; want 7 (command, summary, argv, details, meta, stderr, verify):\n%s", n, got)
	}
}

func TestAControlCharacterCannotSplitASecretPastRedaction(t *testing.T) {
	in, files := scenario()
	in.Redact = runner.RedactWith([]*regexp.Regexp{regexp.MustCompile(`SECRET[0-9]+`)})
	in.Result.Report.Attempts[2].Reported.Details = "SECRET\x1b[0m12\x0034"
	// Sanitizing first turns this into SECRET1234, which the pattern then catches.
	if got := render(t, in, files, cli.Verbose); strings.Contains(got, "SECRET1234") || !strings.Contains(got, "[REDACTED]") {
		t.Errorf("redaction ran before sanitizing:\n%s", got)
	}
}

func TestStderrAndVerifyOutputAreShownAsATail(t *testing.T) {
	in, files := scenario()
	var b strings.Builder
	for i := 1; i <= 500; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	files["/run/attempt-3.stderr.log"] = b.String()
	got := render(t, in, files, cli.VeryVerbose)
	stderr := strings.SplitN(strings.SplitN(got, "-- stderr --\n", 2)[1], "-- verify", 2)[0]
	if n := strings.Count(stderr, "\n"); n != runner.TailLines {
		t.Errorf("stderr lines shown = %d; want %d", n, runner.TailLines)
	}
	if !strings.HasSuffix(stderr, "line 500\n") {
		t.Errorf("the tail must end at the last line: %q", stderr[len(stderr)-20:])
	}
}

func TestUnreadableOrMissingFilesAreSkipped(t *testing.T) {
	in, _ := scenario()
	got := render(t, in, map[string]string{}, cli.VeryVerbose)
	if strings.Contains(got, "-- stderr --") {
		t.Errorf("a missing stderr file must not produce a section:\n%s", got)
	}
}

func TestVerifyLines(t *testing.T) {
	for _, c := range []struct {
		reason     string
		wantHeader string
		wantVerify string // at -v
	}{
		{"resolved → verify failed (exit 128)", "(verify failed: exit 128)", "-- verify: failed (exit 128) --"},
		{"resolved → verify timed out after 3m", "(verify timed out after 3m)", "-- verify: failed (timed out after 3m) --"},
		{"resolved → verify killed by signal SIGTERM", "(verify killed by signal SIGTERM)", "-- verify: failed (killed by signal SIGTERM) --"},
		{"resolved → verify could not start: boom", "(verify could not start: boom)", "-- verify: failed (could not start: boom) --"},
	} {
		in, files := scenario()
		in.Result.Report.Attempts[1].Reason = c.reason
		v := render(t, in, files, cli.Verbose)
		if !strings.Contains(v, "fix-small: failed "+c.wantHeader+" ==") || !strings.Contains(v, c.wantVerify) {
			t.Errorf("%q: -v display lacks %q / %q:\n%s", c.reason, c.wantHeader, c.wantVerify, v)
		}
		// At the default level a failed verify is only in the header.
		d := render(t, in, files, cli.Normal)
		if strings.Contains(strings.SplitN(d, "== [3/5]", 2)[0], "-- verify") {
			t.Errorf("%q: a failed verify must not get a delimiter at the default level:\n%s", c.reason, d)
		}
	}
}

func TestParentheticalRules(t *testing.T) {
	r := &renderer{in: Input{Redact: func(s string) string { return s }}, level: cli.Normal}
	h := &config.Handler{Timeout: 90 * time.Second, Description: "d"}
	for _, c := range []struct {
		name    string
		a       report.Attempt
		level   cli.Verbosity
		handler *config.Handler
		want    string
	}{
		{"resolved at default shows nothing", report.Attempt{Outcome: contract.Resolved, Reason: "verify passed"}, cli.Normal, h, ""},
		{"deferred at default shows nothing", report.Attempt{Outcome: contract.Deferred, Reason: "exit 3", Exit: 3}, cli.Normal, h, ""},
		{"declined shows its exit", report.Attempt{Outcome: contract.Declined, Reason: "exit 2", Exit: 2}, cli.Normal, h, " (exit 2)"},
		{"failed shows its reason", report.Attempt{Outcome: contract.Failed, Reason: "timed out after 10m", Exit: -1}, cli.Normal, h, " (timed out after 10m)"},
		{"-vv lists everything", report.Attempt{Outcome: contract.Failed, Reason: "timed out after 90s", Exit: -1, DurationMS: 90000}, cli.VeryVerbose, h, " (timed out after 90s, 1m30s, timeout 90s, d)"},
		{"-vv without a config entry", report.Attempt{Outcome: contract.Declined, Reason: "exit 2", Exit: 2, DurationMS: 5}, cli.VeryVerbose, nil, " (exit 2, 5ms)"},
		{"-vv resolved", report.Attempt{Outcome: contract.Resolved, Reason: "verify passed", Exit: 0, DurationMS: 1500}, cli.VeryVerbose, h, " (exit 0, 1.5s, timeout 90s, d)"},
	} {
		r.level = c.level
		if got := r.parenthetical(&c.a, c.handler); got != c.want {
			t.Errorf("%s: %q; want %q", c.name, got, c.want)
		}
	}
}

func TestDurationAndTimeoutFormats(t *testing.T) {
	for d, want := range map[time.Duration]string{
		0:                                     "0ms",
		310 * time.Millisecond:                "310ms",
		999 * time.Millisecond:                "999ms",
		time.Second:                           "1.0s",
		1400 * time.Millisecond:               "1.4s",
		59*time.Second + 940*time.Millisecond: "59.9s",
		time.Minute:                           "1m0s",
		192 * time.Second:                     "3m12s",
		59*time.Minute + 59*time.Second:       "59m59s",
		time.Hour:                             "1h0m0s",
		time.Hour + 2*time.Minute + 3*time.Second: "1h2m3s",
	} {
		if got := Duration(d); got != want {
			t.Errorf("Duration(%v) = %q; want %q", d, got, want)
		}
	}
	for d, want := range map[time.Duration]string{
		0:                      "none",
		10 * time.Minute:       "10m",
		90 * time.Second:       "90s",
		2 * time.Hour:          "2h",
		200 * time.Millisecond: "200ms",
		90 * time.Minute:       "90m",
	} {
		if got := Timeout(d); got != want {
			t.Errorf("Timeout(%v) = %q; want %q", d, got, want)
		}
	}
}

func TestRunDirIsAbbreviatedOnlyUnderHome(t *testing.T) {
	in, files := scenario()
	in.RunDir = "/elsewhere/runs/x"
	if got := render(t, in, files, cli.Verbose); !strings.Contains(got, "== run dir: /elsewhere/runs/x ==\n") {
		t.Errorf("run dir outside home:\n%s", got)
	}
	in.RunDir = "/home/user2/runs/x"
	in.Home = "/home/user"
	if got := render(t, in, files, cli.Verbose); !strings.Contains(got, "== run dir: /home/user2/runs/x ==\n") {
		t.Errorf("a sibling directory sharing the home prefix must not be abbreviated:\n%s", got)
	}
	in.RunDir = ""
	if got := render(t, in, files, cli.Verbose); strings.Contains(got, "run dir") {
		t.Errorf("no run dir line without a run dir:\n%s", got)
	}
}

func TestShortReason(t *testing.T) {
	for in, want := range map[string]string{
		"exit 2":                                  "exit 2",
		"resolved → verify failed (exit 128)":     "verify failed: exit 128",
		"resolved → verify timed out after 3m":    "verify timed out after 3m",
		"verify passed":                           "verify passed",
		"resolved → verify failed (exit 1) extra": "verify failed (exit 1) extra",
	} {
		if got := shortReason(in); got != want {
			t.Errorf("shortReason(%q) = %q; want %q", in, got, want)
		}
	}
}

func TestCompactJSON(t *testing.T) {
	for in, want := range map[string]string{
		``:                         "",
		`null`:                     "",
		`{ "a": 1,  "b": [1, 2] }`: `{"a":1,"b":[1,2]}`,
		`{not json`:                `{not json`,
	} {
		if got := compactJSON(json.RawMessage(in)); got != want {
			t.Errorf("compactJSON(%q) = %q; want %q", in, got, want)
		}
	}
}
