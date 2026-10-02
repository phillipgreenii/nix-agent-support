package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-rescue/internal/contract"
	"github.com/phillipgreenii/pg-rescue/internal/contracttest"
	"github.com/phillipgreenii/pg-rescue/internal/notify"
	"github.com/phillipgreenii/pg-rescue/internal/report"
	"github.com/phillipgreenii/pg-rescue/internal/testenv"
)

// TestMain re-executes the test binary as the real pg-rescue-notify main (the
// GO_WANT_HELPER_PROCESS pattern used across this repo), so contracttest.Run
// can drive a genuine process. GO_NOTIFY_ROLE=parent is the orphan test's
// short-lived parent.
func TestMain(m *testing.M) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") == "1" {
		if os.Getenv("GO_NOTIFY_ROLE") == "parent" {
			parentRole()
			return
		}
		for i, a := range os.Args {
			if a == "--" {
				os.Args = append([]string{"pg-rescue-notify"}, os.Args[i+1:]...)
				break
			}
		}
		main()
		return
	}
	os.Exit(testenv.Run(m))
}

// parentRole starts the real handler, waits until its fake osascript is
// running, then exits, orphaning the handler.
func parentRole() {
	pidFile := os.Getenv("FAKE_OSASCRIPT_PIDFILE")
	child := exec.Command(os.Args[0], "-test.run=^$", "--")
	child.Env = append(os.Environ(), "GO_NOTIFY_ROLE=child")
	if err := child.Start(); err != nil {
		os.Exit(2)
	}
	for i := 0; i < 1000; i++ {
		if b, err := os.ReadFile(pidFile); err == nil && strings.TrimSpace(string(b)) != "" {
			os.Exit(0)
		}
		time.Sleep(10 * time.Millisecond)
	}
	_ = child.Process.Kill()
	os.Exit(3)
}

// fakeOSAScript puts a fake osascript first on PATH. It records its argv,
// NUL-separated, in the returned file (so an argument holding any character
// round-trips exactly), then behaves as the FAKE_OSASCRIPT_* variables say.
// It never posts a notification.
func fakeOSAScript(t *testing.T) (env []string, argvFile string) {
	t.Helper()
	dir := t.TempDir()
	argvFile = filepath.Join(dir, "argv")
	script := `#!/bin/sh
if [ -n "$FAKE_OSASCRIPT_PIDFILE" ]; then echo $$ > "$FAKE_OSASCRIPT_PIDFILE"; fi
printf '%s\0' "$@" > "$FAKE_OSASCRIPT_ARGV"
if [ -n "$FAKE_OSASCRIPT_HANG" ]; then exec sleep 30; fi
if [ -n "$FAKE_OSASCRIPT_FAIL" ]; then echo "execution error: boom" >&2; exit "$FAKE_OSASCRIPT_FAIL"; fi
exit 0
`
	if err := os.WriteFile(filepath.Join(dir, "osascript"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return []string{
		"PATH=" + dir + string(os.PathListSeparator) + os.Getenv("PATH"),
		"FAKE_OSASCRIPT_ARGV=" + argvFile,
	}, argvFile
}

// handlerArgv is how contracttest.Run starts the real main.
func handlerArgv(args ...string) []string {
	return append([]string{os.Args[0], "-test.run=^$", "--"}, args...)
}

func helperEnv(extra ...string) []string {
	return append([]string{"GO_WANT_HELPER_PROCESS=1"}, extra...)
}

func recordedArgv(t *testing.T, file string) []string {
	t.Helper()
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("the fake osascript was never run: %v", err)
	}
	parts := strings.Split(string(b), "\x00")
	return parts[:len(parts)-1] // the trailing NUL leaves one empty element
}

// valuesOf checks that osascript got the fixed script and returns the three
// values after "--": title, body, sound.
func valuesOf(t *testing.T, argv []string) (title, body, sound string) {
	t.Helper()
	var want []string
	for _, line := range notify.Script {
		want = append(want, "-e", line)
	}
	want = append(want, "--")
	if len(argv) != len(want)+3 {
		t.Fatalf("osascript argv has %d items, want %d: %q", len(argv), len(want)+3, argv)
	}
	for i := range want {
		if argv[i] != want[i] {
			t.Fatalf("osascript argv[%d] = %q, want the fixed script text %q", i, argv[i], want[i])
		}
	}
	return argv[len(want)], argv[len(want)+1], argv[len(want)+2]
}

// withSummary writes a copy of the three-prior-attempts report whose first
// attempt reports summary, and returns its path.
func withSummary(t *testing.T, summary string) string {
	t.Helper()
	raw, err := os.ReadFile(contracttest.Fixture("three-prior-attempts"))
	if err != nil {
		t.Fatal(err)
	}
	var r report.Report
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatal(err)
	}
	// LastSummary is the most recent attempt that has one: attempt 2.
	r.Attempts[1].Reported.Summary = summary
	out, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "report.json")
	if err := os.WriteFile(p, out, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func assertDeclined(t *testing.T, res contracttest.Result) {
	t.Helper()
	if res.Exit != 2 || res.Outcome != contract.Declined {
		t.Fatalf("exit=%d outcome=%s reason=%q stderr=%q; want exit 2 declined", res.Exit, res.Outcome, res.Reason, res.Stderr)
	}
}

func TestDefaultTemplatesPostOneNotificationAndDecline(t *testing.T) {
	env, argvFile := fakeOSAScript(t)
	for _, fixture := range []string{"first-attempt", "three-prior-attempts", "stdin-mode"} {
		t.Run(fixture, func(t *testing.T) {
			_ = os.Remove(argvFile)
			res := contracttest.Run(t, handlerArgv(), contracttest.Fixture(fixture), helperEnv(env...))
			assertDeclined(t, res)
			title, body, sound := valuesOf(t, recordedArgv(t, argvFile))
			if sound != "" {
				t.Errorf("sound = %q, want empty when --sound is not given", sound)
			}
			switch fixture {
			case "three-prior-attempts":
				if title != "pg-rescue: git pull --rebase failed" {
					t.Errorf("title = %q", title)
				}
				if body != "repo: Resolved the conflict markers and continued the rebase" {
					t.Errorf("body = %q", body)
				}
			case "stdin-mode":
				if title != "pg-rescue: stdin input failed" {
					t.Errorf("title = %q", title)
				}
			}
		})
	}
}

func TestInlineTemplates(t *testing.T) {
	env, argvFile := fakeOSAScript(t)
	res := contracttest.Run(t, handlerArgv(
		"--title-template", "T {{.Repo}} exit={{.Exit}}",
		"--body-template", "B {{.Host}} {{.Context}}",
	), contracttest.Fixture("three-prior-attempts"), helperEnv(env...))
	assertDeclined(t, res)
	title, body, _ := valuesOf(t, recordedArgv(t, argvFile))
	if title != "T repo exit=1" || body != "B phillipg-mbp-02 sync-projects: rebase repo onto origin" {
		t.Errorf("title=%q body=%q", title, body)
	}
}

func TestFileTemplates(t *testing.T) {
	env, argvFile := fakeOSAScript(t)
	dir := t.TempDir()
	// A file normally ends with a newline; it must not reach the notification.
	titleFile := filepath.Join(dir, "title.tmpl")
	bodyFile := filepath.Join(dir, "body.tmpl")
	if err := os.WriteFile(titleFile, []byte("file title {{.Exit}}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bodyFile, []byte("file body {{.LastSummary}}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	res := contracttest.Run(t, handlerArgv("--title-template-file", titleFile, "--body-template-file", bodyFile),
		contracttest.Fixture("three-prior-attempts"), helperEnv(env...))
	assertDeclined(t, res)
	title, body, _ := valuesOf(t, recordedArgv(t, argvFile))
	if title != "file title 1" || body != "file body Resolved the conflict markers and continued the rebase" {
		t.Errorf("title=%q body=%q", title, body)
	}
}

func TestSoundIsAnArgvItem(t *testing.T) {
	env, argvFile := fakeOSAScript(t)
	res := contracttest.Run(t, handlerArgv("--sound", "Glass"), contracttest.Fixture("first-attempt"), helperEnv(env...))
	assertDeclined(t, res)
	argv := recordedArgv(t, argvFile)
	if _, _, sound := valuesOf(t, argv); sound != "Glass" || argv[len(argv)-1] != "Glass" {
		t.Errorf("sound item = %q; argv = %q", sound, argv)
	}
	for _, a := range argv[:len(argv)-3] {
		if strings.Contains(a, "Glass") {
			t.Errorf("the sound name leaked into the script text: %q", a)
		}
	}
}

// The injection test: AppleScript and shell metacharacters in a summary must
// arrive as one argv item, unchanged, and the script text must stay the fixed
// constant (valuesOf checks that).
func TestHostileSummaryReachesOsascriptAsArgvUnchanged(t *testing.T) {
	env, argvFile := fakeOSAScript(t)
	hostile := `done" & (do shell script "touch /tmp/pwned") & "\ and do shell script "id" -- -e 'x'`
	res := contracttest.Run(t, handlerArgv("--body-template", "{{.LastSummary}}", "--title-template", "{{.LastSummary}}"),
		withSummary(t, hostile), helperEnv(env...))
	assertDeclined(t, res)
	title, body, _ := valuesOf(t, recordedArgv(t, argvFile))
	if body != hostile || title != hostile {
		t.Errorf("hostile summary altered:\n title=%q\n body =%q\n want  %q", title, body, hostile)
	}
}

func TestControlCharactersStrippedAndLengthCapped(t *testing.T) {
	env, argvFile := fakeOSAScript(t)

	res := contracttest.Run(t, handlerArgv("--body-template", "{{.LastSummary}}", "--sound", "Gl\x07ass"),
		withSummary(t, "a\x00b\x07c\x1b[31md\ne\x7ff\u0085g"), helperEnv(env...))
	assertDeclined(t, res)
	_, body, sound := valuesOf(t, recordedArgv(t, argvFile))
	if body != "abc[31md efg" {
		t.Errorf("body = %q, want control characters stripped and the newline turned into a space", body)
	}
	if sound != "Glass" {
		t.Errorf("sound = %q, want control characters stripped from it too", sound)
	}

	long := strings.Repeat("x", 500)
	res = contracttest.Run(t, handlerArgv("--title-template", long, "--body-template", "{{.LastSummary}}", "--sound", long),
		withSummary(t, strings.Repeat("é", 300)), helperEnv(env...))
	assertDeclined(t, res)
	title, body, sound := valuesOf(t, recordedArgv(t, argvFile))
	if title != strings.Repeat("x", 200) || sound != strings.Repeat("x", 200) {
		t.Errorf("title/sound lengths = %d/%d, want 200", len(title), len(sound))
	}
	if body != strings.Repeat("é", 200) {
		t.Errorf("body has %d bytes; want 200 characters", len(body))
	}
}

func TestPostFailureExits1(t *testing.T) {
	env, _ := fakeOSAScript(t)
	res := contracttest.Run(t, handlerArgv(), contracttest.Fixture("first-attempt"), helperEnv(append(env, "FAKE_OSASCRIPT_FAIL=1")...))
	if res.Exit != 1 || res.Stdout != "" || !strings.Contains(res.Stderr, "boom") {
		t.Errorf("exit=%d stdout=%q stderr=%q; want exit 1, empty stdout, osascript's message on stderr", res.Exit, res.Stdout, res.Stderr)
	}
	// An osascript that itself exits 2 is still a failure to post, not "declined".
	res = contracttest.Run(t, handlerArgv(), contracttest.Fixture("first-attempt"), helperEnv(append(env, "FAKE_OSASCRIPT_FAIL=2")...))
	if res.Exit != 1 {
		t.Errorf("osascript exit 2 gave handler exit %d, want 1", res.Exit)
	}
}

func TestMissingOsascriptExits1(t *testing.T) {
	empty := t.TempDir()
	res := contracttest.Run(t, handlerArgv(), contracttest.Fixture("first-attempt"), helperEnv("PATH="+empty))
	if res.Exit != 1 || !strings.Contains(res.Stderr, "osascript") {
		t.Errorf("exit=%d stderr=%q", res.Exit, res.Stderr)
	}
}

func TestBadInputExits1NeverDeclined(t *testing.T) {
	env, argvFile := fakeOSAScript(t)
	fixture := contracttest.Fixture("first-attempt")
	dir := t.TempDir()
	missing := filepath.Join(dir, "nope")
	for name, tc := range map[string]struct {
		args    []string
		fixture string
		extra   []string
	}{
		"unknown flag":                {args: []string{"--nope"}, fixture: fixture},
		"stray argument":              {args: []string{"extra"}, fixture: fixture},
		"inline and file for a title": {args: []string{"--title-template", "a", "--title-template-file", missing}, fixture: fixture},
		"inline and file for a body":  {args: []string{"--body-template", "a", "--body-template-file", missing}, fixture: fixture},
		"unreadable template file":    {args: []string{"--body-template-file", missing}, fixture: fixture},
		"unknown template field":      {args: []string{"--body-template", "{{.Nope}}"}, fixture: fixture},
		"template syntax error":       {args: []string{"--title-template", "{{"}, fixture: fixture},
		"unknown schema_version":      {fixture: contracttest.Fixture("unknown-schema-version")},
		"no report":                   {fixture: fixture, extra: []string{"PG_RESCUE_REPORT=" + missing}},
	} {
		t.Run(name, func(t *testing.T) {
			_ = os.Remove(argvFile)
			res := contracttest.Run(t, handlerArgv(tc.args...), tc.fixture, helperEnv(append(env, tc.extra...)...))
			if res.Exit != 1 || res.Stdout != "" || res.Stderr == "" {
				t.Errorf("exit=%d stdout=%q stderr=%q; want exit 1 with a message on stderr", res.Exit, res.Stdout, res.Stderr)
			}
			if _, err := os.Stat(argvFile); err == nil {
				t.Error("a notification was posted despite the error")
			}
		})
	}
}

// runPlain runs the handler without contracttest, for the flags that print
// text instead of a result.
func runPlain(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	cmd := exec.Command(os.Args[0], append([]string{"-test.run=^$", "--"}, args...)...)
	cmd.Env = append(os.Environ(), "GO_WANT_HELPER_PROCESS=1", "PG_RESCUE_REPORT=")
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatal(err)
		}
		code = ee.ExitCode()
	}
	return code, out.String(), errb.String()
}

func TestPrintFlagsNeedNoReport(t *testing.T) {
	code, stdout, stderr := runPlain(t, "--print-default-template")
	want := "-- title --\npg-rescue: {{.Cmd}} failed\n\n-- body --\n{{.Repo}}: {{.LastSummary}}\n"
	if code != 0 || stdout != want {
		t.Errorf("code=%d stdout=%q stderr=%q; want %q", code, stdout, stderr, want)
	}
	code, stdout, stderr = runPlain(t, "--print-template-vars")
	if code != 0 || !strings.Contains(stdout, ".LastSummary") || !strings.Contains(stdout, ".Quote") {
		t.Errorf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}

func TestPrintedDefaultsAreTheDefaults(t *testing.T) {
	if notify.DefaultTitleTemplate != "pg-rescue: {{.Cmd}} failed" || notify.DefaultBodyTemplate != "{{.Repo}}: {{.LastSummary}}" {
		t.Errorf("defaults drifted from design section 8.4: %q %q", notify.DefaultTitleTemplate, notify.DefaultBodyTemplate)
	}
}

// A real orphan: the parent role starts the real handler against a fake
// osascript that hangs, then exits. The handler must notice, kill osascript
// and go away.
func TestOrphanedHandlerKillsOsascriptAndExits(t *testing.T) {
	env, _ := fakeOSAScript(t)
	pidFile := filepath.Join(t.TempDir(), "osascript.pid")
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), helperEnv(env...)...)
	cmd.Env = append(
		cmd.Env,
		"GO_NOTIFY_ROLE=parent",
		"FAKE_OSASCRIPT_HANG=1",
		"FAKE_OSASCRIPT_PIDFILE="+pidFile,
		"PG_RESCUE_REPORT="+contracttest.Fixture("first-attempt"),
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("parent role failed: %v\n%s", err, out)
	}
	b, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 0 {
		t.Fatalf("bad osascript pid %q", b)
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if gone(pid) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Error("the fake osascript is still running after its handler was orphaned")
}

// gone reports whether pid has exited; a zombie nothing has reaped counts.
func gone(pid int) bool {
	if err := syscall.Kill(pid, 0); err != nil {
		return true
	}
	stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return false // no /proc (macOS): kill(0) succeeded, so it is alive
	}
	i := strings.LastIndexByte(string(stat), ')')
	return i >= 0 && i+2 < len(stat) && stat[i+2] == 'Z'
}
