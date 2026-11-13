package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-rescue/internal/contract"
	"github.com/phillipgreenii/pg-rescue/internal/contracttest"
	"github.com/phillipgreenii/pg-rescue/internal/inproc"
	"github.com/phillipgreenii/pg-rescue/internal/notify"
	"github.com/phillipgreenii/pg-rescue/internal/notifyclient/fakedaemon"
	"github.com/phillipgreenii/pg-rescue/internal/report"
	"github.com/phillipgreenii/pg-rescue/internal/testenv"
)

// TestMain re-executes the test binary as the real pg-rescue-notify main (the
// GO_WANT_HELPER_PROCESS pattern used across this repo) for the one test that
// needs a genuine process, the orphan test. Every other test calls run in
// process through inproc.Run: a race-built child costs a flat 1.0 s at exit.
func TestMain(m *testing.M) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") == "1" {
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

// fakeNotifier starts a fake notifier socket server and returns the env that
// points the handler at it. It never posts a banner.
func fakeNotifier(t *testing.T, o fakedaemon.Options) (env []string, srv *fakedaemon.Server) {
	t.Helper()
	if o.Reply == "" && !o.Hang {
		o.Reply = fakedaemon.OKReply
	}
	srv = fakedaemon.Start(t, o)
	return []string{"PG_OSX_NOTIFIER_SOCKET=" + srv.Path}, srv
}

// handlerArgv is the argv that runs the real main as a process.
func handlerArgv(args ...string) []string {
	return append([]string{os.Args[0], "-test.run=^$", "--"}, args...)
}

// runHandler runs the handler in process against a canned report.
func runHandler(t *testing.T, fixture string, env []string, args ...string) contracttest.Result {
	t.Helper()
	return inproc.Run(t, run, args, fixture, env)
}

// onlyPost asserts the fake daemon got exactly one request, a notify/post
// carrying title, body, sound and source only (never id, group, level,
// subtitle or url), and returns title, body and sound.
func onlyPost(t *testing.T, srv *fakedaemon.Server) (title, body, sound string) {
	t.Helper()
	reqs := srv.Requests()
	if len(reqs) != 1 {
		t.Fatalf("the fake daemon got %d requests, want 1", len(reqs))
	}
	r := fakedaemon.Decode(t, reqs[0])
	if r.ProtocolVersion != 1 || r.Service != "notify" || r.Op != "post" {
		t.Errorf("envelope = %+v", r)
	}
	if len(r.Args) != 4 || r.Args["source"] != "pg-rescue" {
		t.Errorf("args = %v, want exactly title, body, sound and source=pg-rescue", r.Args)
	}
	str := func(k string) string { v, _ := r.Args[k].(string); return v }
	return str("title"), str("body"), str("sound")
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
	if res.Exit != contract.ExitDeclined || res.Outcome != contract.Declined {
		t.Fatalf("exit=%d outcome=%s reason=%q stderr=%q; want exit 23 declined", res.Exit, res.Outcome, res.Reason, res.Stderr)
	}
}

// RS-1
func TestDefaultTemplatesPostOneNotificationAndDecline(t *testing.T) {
	for _, fixture := range []string{"first-attempt", "three-prior-attempts", "stdin-mode"} {
		t.Run(fixture, func(t *testing.T) {
			env, srv := fakeNotifier(t, fakedaemon.Options{})
			res := runHandler(t, contracttest.Fixture(fixture), env)
			assertDeclined(t, res)
			title, body, sound := onlyPost(t, srv)
			if sound != "none" {
				t.Errorf("sound = %q, want none when --sound is not given", sound)
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

// RS-2
func TestInlineTemplates(t *testing.T) {
	env, srv := fakeNotifier(t, fakedaemon.Options{})
	res := runHandler(
		t, contracttest.Fixture("three-prior-attempts"), env,
		"--title-template", "T {{.Repo}} exit={{.Exit}}",
		"--body-template", "B {{.Host}} {{.Context}}",
	)
	assertDeclined(t, res)
	title, body, _ := onlyPost(t, srv)
	if title != "T repo exit=1" || body != "B phillipg-mbp-02 sync-projects: rebase repo onto origin" {
		t.Errorf("title=%q body=%q", title, body)
	}
}

// RS-2
func TestFileTemplates(t *testing.T) {
	env, srv := fakeNotifier(t, fakedaemon.Options{})
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
	res := runHandler(t, contracttest.Fixture("three-prior-attempts"), env,
		"--title-template-file", titleFile, "--body-template-file", bodyFile)
	assertDeclined(t, res)
	title, body, _ := onlyPost(t, srv)
	if title != "file title 1" || body != "file body Resolved the conflict markers and continued the rebase" {
		t.Errorf("title=%q body=%q", title, body)
	}
}

// RS-3: the sound is a request field; empty (not given) means none.
func TestSoundIsARequestField(t *testing.T) {
	env, srv := fakeNotifier(t, fakedaemon.Options{})
	res := runHandler(t, contracttest.Fixture("first-attempt"), env, "--sound", "Glass")
	assertDeclined(t, res)
	if _, _, sound := onlyPost(t, srv); sound != "Glass" {
		t.Errorf("sound = %q, want Glass", sound)
	}
	env, srv = fakeNotifier(t, fakedaemon.Options{})
	res = runHandler(t, contracttest.Fixture("first-attempt"), env, "--sound", "")
	assertDeclined(t, res)
	if _, _, sound := onlyPost(t, srv); sound != "none" {
		t.Errorf("an empty --sound sent %q, want none", sound)
	}
}

// RS-4: AppleScript and shell metacharacters in a summary arrive unchanged.
func TestHostileSummaryReachesTheDaemonUnchanged(t *testing.T) {
	env, srv := fakeNotifier(t, fakedaemon.Options{})
	hostile := `done" & (do shell script "touch /tmp/pwned") & "\ and do shell script "id" -- -e 'x'`
	res := runHandler(t, withSummary(t, hostile), env,
		"--body-template", "{{.LastSummary}}", "--title-template", "{{.LastSummary}}")
	assertDeclined(t, res)
	title, body, _ := onlyPost(t, srv)
	if body != hostile || title != hostile {
		t.Errorf("hostile summary altered:\n title=%q\n body =%q\n want  %q", title, body, hostile)
	}
}

// RS-5: a title that starts with "-" is data.
func TestTitleStartingWithDashIsData(t *testing.T) {
	env, srv := fakeNotifier(t, fakedaemon.Options{})
	res := runHandler(t, contracttest.Fixture("first-attempt"), env, "--title-template=-e --sound x")
	assertDeclined(t, res)
	if title, _, _ := onlyPost(t, srv); title != "-e --sound x" {
		t.Errorf("title = %q", title)
	}
}

// RS-6: control characters are stripped and the daemon's BYTE caps are met on
// a rune boundary (title 256 B, body 2048 B).
func TestControlCharactersStrippedAndByteLengthCapped(t *testing.T) {
	env, srv := fakeNotifier(t, fakedaemon.Options{})
	res := runHandler(t, withSummary(t, "a\x00b\x07c\x1b[31md\ne\x7ff\u0085g"), env,
		"--body-template", "{{.LastSummary}}", "--sound", "Gl\x07ass")
	assertDeclined(t, res)
	_, body, sound := onlyPost(t, srv)
	if body != "abc[31md efg" {
		t.Errorf("body = %q, want control characters stripped and the newline turned into a space", body)
	}
	if sound != "Glass" {
		t.Errorf("sound = %q, want control characters stripped from it too", sound)
	}

	// 200 runes of 3 bytes each is 600 B: over the title cap, and a cut at 256
	// would split a rune, so it must land on 85 runes (255 B).
	env, srv = fakeNotifier(t, fakedaemon.Options{})
	res = runHandler(t, withSummary(t, strings.Repeat("é", 1500)), env,
		"--title-template", strings.Repeat("日", 200), "--body-template", "{{.LastSummary}}")
	assertDeclined(t, res)
	title, body, _ := onlyPost(t, srv)
	if title != strings.Repeat("日", 85) {
		t.Errorf("title has %d bytes: %q", len(title), title)
	}
	if body != strings.Repeat("é", 1024) {
		t.Errorf("body has %d bytes, want 2048 bytes of whole runes", len(body))
	}
	// Exactly at the caps: nothing is cut.
	env, srv = fakeNotifier(t, fakedaemon.Options{})
	res = runHandler(t, contracttest.Fixture("first-attempt"), env,
		"--title-template", strings.Repeat("x", 256), "--body-template", strings.Repeat("y", 2048), "--sound", strings.Repeat("s", 64))
	assertDeclined(t, res)
	title, body, sound = onlyPost(t, srv)
	if len(title) != 256 || len(body) != 2048 || len(sound) != 64 {
		t.Errorf("lengths = %d/%d/%d, want 256/2048/64", len(title), len(body), len(sound))
	}
}

// RS-7: a failed post exits 1, never 2, whatever code the daemon replies with.
func TestPostFailureExits1(t *testing.T) {
	for _, code := range []string{"unauthenticated", "invalid_argument", "unavailable", "unknown_op", "not_found", "version_mismatch"} {
		env, _ := fakeNotifier(t, fakedaemon.Options{
			Reply: `{"protocolVersion":1,"schemaVersion":1,"error":{"code":"` + code + `","message":"boom"}}`,
		})
		res := runHandler(t, contracttest.Fixture("first-attempt"), env)
		if res.Exit != 1 || res.Stdout != "" || !strings.Contains(res.Stderr, code) {
			t.Errorf("%s: exit=%d stdout=%q stderr=%q; want exit 1, empty stdout, the daemon's code on stderr", code, res.Exit, res.Stdout, res.Stderr)
		}
	}
	env, _ := fakeNotifier(t, fakedaemon.Options{Reply: "not json"})
	if res := runHandler(t, contracttest.Fixture("first-attempt"), env); res.Exit != 1 || res.Stdout != "" {
		t.Errorf("a garbled reply gave exit %d, want 1", res.Exit)
	}
}

// RS-8: a daemon that is not running exits 1; nothing falls back.
func TestDaemonNotRunningExits1(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "none.sock")
	res := runHandler(t, contracttest.Fixture("first-attempt"), []string{"PG_OSX_NOTIFIER_SOCKET=" + missing})
	if res.Exit != 1 || res.Stdout != "" || !strings.Contains(res.Stderr, "notifier unreachable") {
		t.Errorf("exit=%d stdout=%q stderr=%q", res.Exit, res.Stdout, res.Stderr)
	}
}

// RS-9
func TestBadInputExits1NeverDeclined(t *testing.T) {
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
			env, srv := fakeNotifier(t, fakedaemon.Options{})
			res := runHandler(t, tc.fixture, append(env, tc.extra...), tc.args...)
			if res.Exit != 1 || res.Stdout != "" || res.Stderr == "" {
				t.Errorf("exit=%d stdout=%q stderr=%q; want exit 1 with a message on stderr", res.Exit, res.Stdout, res.Stderr)
			}
			if n := len(srv.Requests()); n != 0 {
				t.Errorf("%d notification(s) posted despite the error", n)
			}
		})
	}
}

// RS-15: a title that is empty after trimming, and a --sound outside
// [A-Za-z0-9 _-] or over 64 bytes, exit 1 (never 2) with nothing posted.
func TestContractViolationsExit1AndPostNothing(t *testing.T) {
	for name, args := range map[string][]string{
		"empty title":        {"--title-template", ""},
		"blank title":        {"--title-template", " \t "},
		"title of controls":  {"--title-template", "\x01\x02"},
		"sound with a slash": {"--sound", "../etc/passwd"},
		"sound with a dot":   {"--sound", "Glass.aiff"},
		"sound 65 bytes":     {"--sound", strings.Repeat("s", 65)},
	} {
		t.Run(name, func(t *testing.T) {
			env, srv := fakeNotifier(t, fakedaemon.Options{})
			res := runHandler(t, contracttest.Fixture("first-attempt"), env, args...)
			if res.Exit != 1 || res.Stdout != "" || res.Stderr == "" {
				t.Errorf("exit=%d stdout=%q stderr=%q; want exit 1", res.Exit, res.Stdout, res.Stderr)
			}
			if n := len(srv.Requests()); n != 0 {
				t.Errorf("%d notification(s) posted", n)
			}
		})
	}
}

// runPlain runs the handler without contracttest, for the flags that print
// text instead of a result.
func runPlain(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errb bytes.Buffer
	code = inproc.Call(t, run, args, "", []string{"PG_RESCUE_REPORT="}, nil, &out, &errb)
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

// RS-11: a real orphan. A short-lived shell (the wrapper) starts the real
// handler, as a process, against a fake daemon that holds its reply; the
// wrapper exits once the daemon has the request. The handler must notice, close
// the in-flight connection (the daemon sees it close) and go away.
func TestOrphanedHandlerClosesTheInFlightPostAndExits(t *testing.T) {
	env, srv := fakeNotifier(t, fakedaemon.Options{Hang: true})
	goFile := filepath.Join(t.TempDir(), "go")
	const wrapper = `"$@" </dev/null >/dev/null 2>&1 &
handler=$!
i=0
while [ ! -e "$WRAPPER_GO" ] && [ "$i" -lt 1500 ]; do sleep 0.01; i=$((i + 1)); done
if [ -e "$WRAPPER_GO" ]; then exit 0; fi
kill "$handler"
exit 3`
	cmd := exec.CommandContext(context.Background(), "/bin/sh", append([]string{"-c", wrapper, "wrapper"}, handlerArgv()...)...)
	cmd.Env = append(os.Environ(), env...)
	cmd.Env = append(
		cmd.Env,
		"GO_WANT_HELPER_PROCESS=1",
		"WRAPPER_GO="+goFile,
		"PG_RESCUE_REPORT="+contracttest.Fixture("first-attempt"),
	)
	go func() {
		deadline := time.Now().Add(14 * time.Second)
		for len(srv.Requests()) == 0 && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		_ = os.WriteFile(goFile, nil, 0o600)
	}()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("wrapper failed: %v\n%s", err, out)
	}
	if len(srv.Requests()) == 0 {
		t.Fatal("the fake daemon never received the post")
	}
	select {
	case <-srv.Closed():
	case <-time.After(15 * time.Second):
		t.Error("the handler did not close its in-flight connection after it was orphaned")
	}
}
