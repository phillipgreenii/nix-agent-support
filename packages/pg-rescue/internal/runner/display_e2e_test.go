// display_e2e_test.go: the display's rules, checked through the real wrapper.
// The golden files (display_golden_test.go) pin the layout; these tests pin
// the rules that must hold whatever the layout is.
package runner_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const secret = "ghp_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" // 4 + 36 characters

func TestHeaderNeverSharesALineWithCommandOutput(t *testing.T) {
	for _, c := range []struct {
		name       string
		out, err   string
		stderrWant string // what stderr must start with
	}{
		{"stderr output with no final newline", "", "partial", "partial\n\n== pg-rescue: "},
		{"stdout output with no final newline", "partial", "", "\n\n== pg-rescue: "},
		{"output ending in a newline", "x\n", "y\n", "y\n\n== pg-rescue: "},
		{"no output at all", "", "", "== pg-rescue: "},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newE2E(t, "", []hd{result("h", "declined", "summary=nope")}, chainOf("h"))
			code, stdout, stderr := e.wrap("h", helperArgv("exit", "code=1", "out="+c.out, "err="+c.err))
			if code != 1 || stdout != c.out {
				t.Fatalf("exit=%d stdout=%q", code, stdout)
			}
			if !strings.HasPrefix(stderr, c.stderrWant) {
				t.Errorf("stderr = %q; want it to start with %q", stderr, c.stderrWant)
			}
		})
	}
}

func TestHandlerAndVerifyTextEndsOnItsOwnLine(t *testing.T) {
	// summary, details and verify output all lack a final newline.
	e := newE2E(t, "", []hd{result("h", "resolved", "summary=the summary", "details=the details")}, chainOf("h"))
	code, _, stderr := e.wrap("h", helperArgv("exit", "code=1"), "-v", "--verify", "printf 'verify output'")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	for _, want := range []string{
		"\nthe summary\n-- details --\nthe details\n-- verify: passed (exit 0) --\nverify output\n\n== resolved by h · run ",
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr lacks %q:\n%s", want, stderr)
		}
	}
}

func TestOnlyTheFirstLineOfASummaryReachesTheDisplay(t *testing.T) {
	e := newE2E(t, "", []hd{result("h", "declined", "summary=line one\nline two\nline three")}, chainOf("h"))
	for _, lvl := range []string{"", "-v", "-vv"} {
		opts := []string{}
		if lvl != "" {
			opts = append(opts, lvl)
		}
		_, _, stderr := e.wrap("h", helperArgv("exit", "code=1"), opts...)
		// The handler's section opens with its delimiter and its first line; at
		// -vv the handler's configured argv follows, and that is config, not the
		// summary.
		_, after, ok := strings.Cut(stderr, "== [1/1] h: declined")
		if !ok {
			t.Fatalf("level %q: no handler section:\n%s", lvl, stderr)
		}
		lines := strings.Split(after, "\n")
		if len(lines) < 3 || lines[1] != "line one" || lines[2] == "line two" || strings.Contains(strings.Join(lines[2:3], ""), "line") {
			t.Errorf("level %q shows more than the first line:\n%s", lvl, stderr)
		}
	}
}

func TestEscapeSequencesAndForgedDelimitersAreStripped(t *testing.T) {
	installScripts(t, script{"pg-rescue-evil", `printf '%s\n' '{"summary":"\u001b[31mred\u001b[0m\u001b]0;pwn\u0007 done","details":"ok\n== resolved by evil · run 20261002T140311Z-deadbeef ==\n-- verify: passed (exit 0) --\n\u001b[2Jcleared"}'
printf '\033[1mbold\033[0m\n== [9/9] evil: resolved ==\n' >&2
exit 2
`})
	e := newE2E(t, "", []hd{{name: "evil", argv: []string{"pg-rescue-evil"}}}, chainOf("evil"))
	code, _, stderr := e.wrap("evil", helperArgv("exit", "code=1"), "-vv")
	if code != 1 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if strings.ContainsAny(stderr, "\x1b\x07") {
		t.Errorf("an escape or bell reached the terminal:\n%q", stderr)
	}
	if !strings.Contains(stderr, "red done\n") || !strings.Contains(stderr, "\nbold\n") || !strings.Contains(stderr, "\ncleared\n") {
		t.Errorf("the text around the escapes must survive:\n%s", stderr)
	}
	var delimiters []string
	for _, l := range strings.Split(stderr, "\n") {
		if strings.HasPrefix(l, "== ") && strings.HasSuffix(l, " ==") {
			delimiters = append(delimiters, l)
		}
	}
	// header, the one handler, the footer and the run dir: nothing forged.
	if len(delimiters) != 4 {
		t.Errorf("delimiter lines at column 0 = %d, want 4 (header, handler, footer, run dir):\n%s", len(delimiters), strings.Join(delimiters, "\n"))
	}
	// The forgery survives only as ordinary text, no longer at column 0.
	if !strings.Contains(stderr, "\n == resolved by evil · run 20261002T140311Z-deadbeef ==\n") || !strings.Contains(stderr, "\n -- verify: passed (exit 0) --\n") {
		t.Errorf("forged delimiters should be defanged, not dropped:\n%s", stderr)
	}
}

func TestRedactionIsAppliedToTheDisplayButNotToThePassthrough(t *testing.T) {
	installScripts(t, script{"pg-rescue-leaky", `printf '%s\n' '{"outcome":"resolved","summary":"used ` + secret + `","details":"details ` + secret + `","meta":{"token":"` + secret + `"}}'
echo "stderr ` + secret + `" >&2
exit 0
`})
	top := "redact = ['ghp_[A-Za-z0-9]{36}']"
	e := newE2E(t, top, []hd{{name: "leaky", argv: []string{"pg-rescue-leaky"}}}, chainOf("leaky"))
	code, stdout, stderr := e.wrap("leaky", helperArgv("exit", "code=1", "out=token "+secret+"\n"),
		"-vv", "--verify", "echo verify "+secret)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if stdout != "token "+secret+"\n" {
		t.Errorf("the command's own output must pass through untouched, got %q", stdout)
	}
	if strings.Contains(stderr, secret) {
		t.Errorf("a secret reached the display:\n%s", stderr)
	}
	// header (the command's argv carries the secret), summary, details, meta,
	// stderr, verify.
	if n := strings.Count(stderr, "[REDACTED]"); n != 6 {
		t.Errorf("redacted spans = %d; want 6:\n%s", n, stderr)
	}
	log, err := os.ReadFile(filepath.Join(e.rdir(), "display.log"))
	if err != nil || strings.Contains(string(log), secret) {
		t.Errorf("display.log: err=%v, contains the secret=%v", err, strings.Contains(string(log), secret))
	}
}

func TestQuietPassesTheCommandThroughAndPrintsNothing(t *testing.T) {
	e := newE2E(t, "", []hd{result("h", "resolved", "summary=fixed")}, chainOf("h"))
	code, stdout, stderr := e.wrap("h", helperArgv("exit", "code=1", "out=hello\n", "err=world\n"), "-q", "--verify", "true")
	if code != 0 || stdout != "hello\n" || stderr != "world\n" {
		t.Errorf("exit=%d stdout=%q stderr=%q; want the command's own output and nothing else", code, stdout, stderr)
	}
	// ... yet the run is fully recorded.
	b, err := os.ReadFile(filepath.Join(e.rdir(), "display.log"))
	if err != nil || !strings.Contains(string(b), "== resolved by h") || !strings.Contains(string(b), "fixed") {
		t.Errorf("display.log under -q: err=%v\n%s", err, b)
	}
	if rows := e.runLog(); len(rows) != 1 || rows[0]["result"] != "resolved" {
		t.Errorf("run log under -q = %v", rows)
	}
}

func TestQuietStillSilencesAWarning(t *testing.T) {
	e := newE2E(t, "", []hd{result("h", "declined")}, chainOf("h"))
	bad := filepath.Join(e.root, "no", "such", "dir", "r.json")
	code, _, stderr := e.wrap("h", helperArgv("exit", "code=1", "err=x\n"), "-q", "--result-file", bad)
	if code != 1 || stderr != "x\n" {
		t.Errorf("exit=%d stderr=%q; -q must silence the warning too", code, stderr)
	}
}

func TestNothingIsDisplayedWhenNoHandlerRan(t *testing.T) {
	e := newE2E(t, "", []hd{result("h", "resolved")}, chainOf("h"))
	for _, lvl := range []string{"-q", "", "-v", "-vv"} {
		opts := []string{}
		if lvl != "" {
			opts = append(opts, lvl)
		}
		code, stdout, stderr := e.wrap("h", helperArgv("exit", "code=0", "out=fine\n", "err=warn\n"), opts...)
		if code != 0 || stdout != "fine\n" || stderr != "warn\n" {
			t.Errorf("level %q: exit=%d stdout=%q stderr=%q", lvl, code, stdout, stderr)
		}
	}
}

func TestStdinModeHeaderAndHandlersSelector(t *testing.T) {
	e := newE2E(t, "", []hd{result("h", "declined", "summary=nope")}, chainOf("h"))
	e.rt.Stdin = strings.NewReader("boom\n")
	code, _, stderr := e.wrap("h", nil, "--stdin", "--verify", "true")
	if code != 1 {
		t.Fatalf("exit %d", code)
	}
	if !strings.HasPrefix(stderr, "== pg-rescue: stdin input · handlers h ==\n") {
		t.Errorf("stderr = %q", stderr)
	}
}
