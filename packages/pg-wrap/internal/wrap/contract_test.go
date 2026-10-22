package wrap_test

// The wrapper contract suite: WC-1..WC-9 and the composition rules, proven by
// driving the wrap library's composition with REAL wrappers on the host. No
// build tag: the commit-time test runner runs it. Every test name contains
// "Contract" or "WC" so `-run 'Contract|WC'` selects the whole suite.

import (
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-wrap/internal/wrap"
)

// WC-1: a wrapper runs the rest of its argv as the payload, with argv and stdin
// unmodified.
func TestWC1_ArgvAndStdinUnmodified(t *testing.T) {
	stdin := "first line\n$(touch SENT); 'quoted' \"double\"\nlast line without newline"
	eachWrapper(t, func(t *testing.T, w wrapperCase, e wrap.Entry) {
		r := runChain(t, runOpts{entries: []wrap.Entry{e}, args: hostile, stdin: stdin})
		if r.exit != 0 || r.rep == nil {
			t.Fatalf("exit=%d rep=%v stderr=%s", r.exit, r.rep, r.stderr)
		}
		if !reflect.DeepEqual(r.rep.argv, hostile) {
			t.Fatalf("argv changed:\n got %q\nwant %q", r.rep.argv, hostile)
		}
		if r.rep.stdin != stdin {
			t.Fatalf("stdin changed: got %q want %q", r.rep.stdin, stdin)
		}
		noSentinel(t, r.chain)
	})
}

// WC-2: the payload's exit status comes back unchanged, including statuses that
// collide with a wrapper's own (flox 1, timeout 124, a caller's busy 9, pg-wrap's
// own 97). A wrapper that fails activation exits before the
// payload starts, with the reason on stderr.
func TestWC2_ExitStatusUnchanged(t *testing.T) {
	eachWrapper(t, func(t *testing.T, w wrapperCase, e wrap.Entry) {
		for _, code := range []int{0, 1, 2, 9, 42, 97, 124, 127} {
			r := runChain(t, runOpts{
				entries: []wrap.Entry{e},
				env:     []string{"PROBE_EXIT=" + strconv.Itoa(code)},
			})
			if r.rep == nil {
				t.Fatalf("payload did not run for PROBE_EXIT=%d: exit=%d stderr=%s", code, r.exit, r.stderr)
			}
			if r.exit != code {
				t.Fatalf("PROBE_EXIT=%d came back as %d", code, r.exit)
			}
		}
	})
}

func TestWC2_ActivationFailureExitsBeforePayload(t *testing.T) {
	fail := wrap.Entry{Shell: `/bin/sh -c 'echo "pg-wrap[activate]: cannot activate" >&2; exit 3'`}
	r := runChain(t, runOpts{entries: []wrap.Entry{
		{Shell: "/usr/bin/env OUTER=1"}, fail, {Shell: "/usr/bin/env INNER=1"},
	}})
	if r.exit != 3 {
		t.Fatalf("exit = %d, want the failing wrapper's 3", r.exit)
	}
	if r.rep != nil {
		t.Fatal("payload ran although activation failed")
	}
	if !strings.Contains(r.stderr.String(), "pg-wrap[activate]: cannot activate") {
		t.Fatalf("reason missing from stderr: %q", r.stderr)
	}
}

// WC-3: a wrapper writes nothing to stdout. Stdout is exactly the payload's own,
// and a failing chain writes none; the wrapper's own messages go to stderr with
// the pg-wrap[<stage>]: prefix.
func TestWC3_StdoutIsExactlyThePayloads(t *testing.T) {
	eachWrapper(t, func(t *testing.T, w wrapperCase, e wrap.Entry) {
		r := runChain(t, runOpts{entries: []wrap.Entry{e}})
		if r.exit != 0 {
			t.Fatalf("exit=%d stderr=%s", r.exit, r.stderr)
		}
		if got := r.stdout.String(); got != "PROBE-STDOUT\n" {
			t.Fatalf("stdout = %q, want only the payload's own output", got)
		}
		if !strings.Contains(r.stderr.String(), "probe-stderr") {
			t.Fatalf("payload stderr lost: %q", r.stderr)
		}
	})
}

func TestWC3_FailingChainWritesNoStdoutAndPrefixesStderr(t *testing.T) {
	failing := wrap.Entry{Shell: `/bin/sh -c 'echo "pg-wrap[activate]: cannot activate" >&2; exit 3'`}
	r := runChain(t, runOpts{entries: []wrap.Entry{failing}})
	if r.exit != 3 || r.rep != nil {
		t.Fatalf("exit=%d rep=%v: the failing layer must stop the chain", r.exit, r.rep)
	}
	if r.stdout.Len() != 0 {
		t.Fatalf("wrapper wrote to stdout: %q", r.stdout)
	}
	if !strings.HasPrefix(r.stderr.String(), "pg-wrap[activate]:") {
		t.Fatalf("stderr = %q, want a pg-wrap[<stage>]: prefix", r.stderr)
	}
}

// WC-4: the payload keeps the controlling terminal and runs in the foreground
// process group. Proven under a real tmux pane: the payload's tty equals the
// pane's tty and its process group is the terminal's foreground group.
func TestWC4_ControllingTerminalAndForegroundGroup(t *testing.T) {
	tmux := lookTool(t, "tmux")
	cases := []struct {
		name  string
		entry func(t *testing.T) wrap.Entry
	}{
		{"none", func(t *testing.T) wrap.Entry { return wrap.Entry{} }},
		{"env", func(t *testing.T) wrap.Entry { return wrap.Entry{Shell: "/usr/bin/env WRAPPED=env"} }},
		{"sh-string", func(t *testing.T) wrap.Entry { return wrap.Entry{Shell: `/bin/sh -c 'exec "$@"' sh`} }},
		{"timeout-foreground", func(t *testing.T) wrap.Entry {
			return wrap.Entry{Argv: []string{gnuTimeout(t), "--foreground", "60"}}
		}},
		{"flox", floxEntry},
		{"nix-develop", nixDevelopEntry},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := c.entry(t)
			var entries []wrap.Entry
			if e.Shell != "" || len(e.Argv) > 0 {
				entries = append(entries, e)
			}
			rep, ref := runInTmux(t, tmux, entries)
			if rep == nil {
				t.Fatal("payload did not run under tmux")
			}
			if rep.tty == "" || rep.tty == "?" || rep.tty == "??" {
				t.Fatalf("payload has no controlling terminal (tty=%q)", rep.tty)
			}
			if rep.tty != ref {
				t.Fatalf("payload tty %q differs from the pane's tty %q", rep.tty, ref)
			}
			if rep.pgid <= 0 || rep.pgid != rep.tpgid {
				t.Fatalf("payload is not in the foreground group: pgid=%d foreground pgid=%d", rep.pgid, rep.tpgid)
			}
		})
	}
}

// WC-5: SIGTERM, SIGINT and SIGHUP reach the payload through the wrapper.
func TestWC5_SignalsReachPayload(t *testing.T) {
	sigs := []struct {
		name string
		sig  syscall.Signal
	}{{"TERM", syscall.SIGTERM}, {"INT", syscall.SIGINT}, {"HUP", syscall.SIGHUP}}
	eachWrapper(t, func(t *testing.T, w wrapperCase, e wrap.Entry) {
		for _, s := range sigs {
			t.Run(s.name, func(t *testing.T) {
				// A signal ignored in the test process (nohup, a background launch) is
				// inherited as ignored by every child, and a non-interactive sh cannot
				// trap a signal that was ignored on entry.
				if signal.Ignored(s.sig) {
					t.Skipf("SIG%s is ignored in the test process; children inherit that", s.name)
				}
				c := buildChain(t, runOpts{entries: []wrap.Entry{e}, env: []string{"PROBE_WAIT=1"}})
				if err := c.cmd.Start(); err != nil {
					t.Fatal(err)
				}
				done := make(chan error, 1)
				go func() { done <- c.cmd.Wait() }()
				t.Cleanup(func() { _ = syscall.Kill(-c.cmd.Process.Pid, syscall.SIGKILL) })
				waitFor(t, c.prefix+".ready")
				// Signal only the wrapper process the adapter started, not its group.
				if err := c.cmd.Process.Signal(s.sig); err != nil {
					t.Fatal(err)
				}
				select {
				case <-done:
				case <-time.After(10 * time.Second):
					t.Fatalf("chain did not exit after SIG%s", s.name)
				}
				b, err := os.ReadFile(c.prefix + ".sig")
				if err != nil {
					t.Fatalf("payload never saw a signal after SIG%s: %v", s.name, err)
				}
				if got := strings.TrimSpace(string(b)); got != s.name {
					t.Fatalf("payload saw %s, want %s", got, s.name)
				}
			})
		}
	})
}

// WC-5 (adapter side): cancelling through a non-exec wrapper leaves no orphan
// when the chain runs under wrap.Group. The control shows why the group matters:
// exec.CommandContext's default SIGKILL to the direct child orphans the payload.
func TestWC5_GroupCancelThroughNonExecTimeoutLeavesNoOrphan(t *testing.T) {
	for _, name := range []string{"timeout", "timeout-foreground"} {
		t.Run(name, func(t *testing.T) {
			var e wrap.Entry
			for _, w := range wrapperCases() {
				if w.name == name {
					e = w.entry(t)
				}
			}
			c := buildChain(t, runOpts{entries: []wrap.Entry{e}, env: []string{"PROBE_WAIT=1"}, group: true})
			if err := c.cmd.Start(); err != nil {
				t.Fatal(err)
			}
			pgid := c.cmd.Process.Pid
			done := make(chan error, 1)
			go func() { done <- c.cmd.Wait() }()
			t.Cleanup(func() { _ = syscall.Kill(-pgid, syscall.SIGKILL) })
			waitFor(t, c.prefix+".ready")
			rep := readReport(t, c.prefix)
			if rep == nil || rep.pid == c.cmd.Process.Pid {
				t.Fatalf("timeout must stay the parent of the payload: %+v", rep)
			}
			c.cancel()
			select {
			case <-done:
			case <-time.After(15 * time.Second):
				t.Fatal("Wait did not return after cancel")
			}
			waitGroupGone(t, pgid)
			if !gone(rep.pid) {
				t.Fatalf("payload pid %d survived the cancel", rep.pid)
			}
		})
	}
}

func TestWC5_ControlPlainCancelOrphansThePayload(t *testing.T) {
	e := wrap.Entry{Argv: []string{gnuTimeout(t), "60"}}
	c := buildChain(t, runOpts{entries: []wrap.Entry{e}, env: []string{"PROBE_WAIT=1"}})
	// The orphan holds the output pipes open; bound Wait so the cancel can return.
	c.cmd.WaitDelay = 2 * time.Second
	if err := c.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pgid := c.cmd.Process.Pid
	done := make(chan error, 1)
	go func() { done <- c.cmd.Wait() }()
	t.Cleanup(func() { _ = syscall.Kill(-pgid, syscall.SIGKILL) })
	waitFor(t, c.prefix+".ready")
	rep := readReport(t, c.prefix)
	c.cancel() // the default Cancel: SIGKILL to the direct child only
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("Wait did not return after cancel")
	}
	if gone(rep.pid) {
		t.Fatal("payload died with the wrapper: the orphan premise of wrap.Group no longer holds")
	}
	// The payload is still alive: exactly the orphan wrap.Group exists to prevent.
}

// WC-6: the chain works under the base environment alone, reaching its tools by
// absolute path, never prompts, and carries no variable outside the base set.
func TestWC6_BaseEnvAloneNoPromptNoLeak(t *testing.T) {
	polluted := append(os.Environ(),
		"SECRET_TOKEN=leak", "SSH_AUTH_SOCK=/leak/sock", "GH_TOKEN=leak", "CCPOOL_LEAK=1")
	allowed := map[string]bool{
		"HOME": true, "USER": true, "LOGNAME": true, "TMPDIR": true, "LANG": true,
		"TERM": true, "COLORTERM": true, "PATH": true,
		"PWD": true, "OLDPWD": true, "SHLVL": true, "_": true, "WRAPPED": true,
		"LC_ALL": true, "LC_CTYPE": true, "__CF_USER_TEXT_ENCODING": true,
	}
	for _, name := range []string{"none", "env", "sh-string", "timeout", "timeout-foreground"} {
		t.Run(name, func(t *testing.T) {
			var entries []wrap.Entry
			for _, w := range wrapperCases() {
				if w.name == name {
					entries = append(entries, w.entry(t))
				}
			}
			start := time.Now()
			r := runChain(t, runOpts{entries: entries, source: polluted}) // stdin is /dev/null
			if r.exit != 0 || r.rep == nil {
				t.Fatalf("exit=%d stderr=%s", r.exit, r.stderr)
			}
			if time.Since(start) > chainTimeout/2 {
				t.Fatalf("chain took %s: it is waiting on something", time.Since(start))
			}
			if got := r.rep.env["PATH"]; got != "/usr/bin:/bin:/usr/sbin:/sbin" {
				t.Fatalf("PATH = %q, want the fixed base path", got)
			}
			for k := range r.rep.env {
				if !allowed[k] && !strings.HasPrefix(k, "PROBE_") {
					t.Errorf("payload env carries %q, which is not in the base set", k)
				}
			}
		})
	}
}

// WC-7: a wrapper with no cleanup to do execs, so the payload keeps the pid the
// adapter started. timeout stays the parent (it has cleanup: the timer).
func TestWC7_ExecWhenNoCleanup(t *testing.T) {
	eachWrapper(t, func(t *testing.T, w wrapperCase, e wrap.Entry) {
		c := buildChain(t, runOpts{entries: []wrap.Entry{e}})
		if err := c.cmd.Start(); err != nil {
			t.Fatal(err)
		}
		pid := c.cmd.Process.Pid
		if err := c.cmd.Wait(); err != nil {
			t.Fatalf("%v: %s", err, c.stderr)
		}
		rep := readReport(t, c.prefix)
		if rep == nil {
			t.Fatal("payload did not run")
		}
		if w.exec && rep.pid != pid {
			t.Fatalf("%s did not exec: payload pid %d, started pid %d", w.name, rep.pid, pid)
		}
		if !w.exec && rep.pid == pid {
			t.Fatalf("%s unexpectedly exec'd its payload (pid %d)", w.name, pid)
		}
	})
}

// WC-8: item data comes only from environment variables or argv, never from text
// interpolated into a string entry. A spec has no templating, so an array
// element is used literally.
func TestWC8_ItemDataOnlyViaEnvOrArgv(t *testing.T) {
	data := `a;touch SENT $(touch SENT) "q" 's' ` + "`touch SENT` b  c"

	t.Run("environment variable in a string entry", func(t *testing.T) {
		r := runChain(t, runOpts{
			entries: []wrap.Entry{{Shell: `/usr/bin/env "ITEM=$PG_WRAP_ITEM_ID"`}},
			env:     []string{"PG_WRAP_ITEM_ID=" + data},
		})
		if r.exit != 0 || r.rep == nil {
			t.Fatalf("exit=%d stderr=%s", r.exit, r.stderr)
		}
		if got := r.rep.env["ITEM"]; got != data {
			t.Fatalf("ITEM = %q, want %q", got, data)
		}
		noSentinel(t, r.chain)
	})

	t.Run("argv through a string and an array entry", func(t *testing.T) {
		r := runChain(t, runOpts{
			entries: []wrap.Entry{
				{Shell: "/usr/bin/env A=1"},
				{Argv: []string{"/usr/bin/env", "B=2"}},
			},
			args: []string{data},
		})
		if r.exit != 0 || r.rep == nil {
			t.Fatalf("exit=%d stderr=%s", r.exit, r.stderr)
		}
		if !reflect.DeepEqual(r.rep.argv, []string{data}) {
			t.Fatalf("argv = %q, want %q", r.rep.argv, []string{data})
		}
		noSentinel(t, r.chain)
	})

	t.Run("an array element is used literally, with no templating", func(t *testing.T) {
		r := runChain(t, runOpts{
			entries: []wrap.Entry{{Argv: []string{"/usr/bin/env", "ITEM={{.ItemID}}"}}},
		})
		if r.exit != 0 || r.rep == nil {
			t.Fatalf("exit=%d stderr=%s", r.exit, r.stderr)
		}
		if got := r.rep.env["ITEM"]; got != "{{.ItemID}}" {
			t.Fatalf("ITEM = %q, want the literal element", got)
		}
	})

	t.Run("a string entry holding braces is shell text, never rendered", func(t *testing.T) {
		argv, err := wrap.Compose([]wrap.Entry{{Shell: "/usr/bin/env X={{.ItemID}}"}}, []string{"/usr/bin/true"})
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"/bin/sh", "-c", `exec /usr/bin/env X={{.ItemID}} "$@"`, "sh", "/usr/bin/true"}
		if !reflect.DeepEqual(argv, want) {
			t.Fatalf("got %q want %q", argv, want)
		}
	})
}

// WC-9: the first word of a string entry and the first element of an array entry
// MUST be an absolute path.
func TestWC9_FirstWordMustBeAbsolutePath(t *testing.T) {
	bad := []wrap.Entry{
		{Shell: "env FOO=1"},
		{Shell: "./wrapper"},
		{Shell: "~/bin/wrapper"},
		{Shell: "$HOME/bin/wrapper"},
		{Shell: "  relative/path arg"},
		{Argv: []string{"env", "FOO=1"}},
		{Argv: []string{"bin/wrapper"}},
		{Argv: []string{"$WRAPPER"}},
	}
	for _, e := range bad {
		if err := wrap.Validate([]wrap.Entry{e}); err == nil {
			t.Errorf("Validate accepted %+v", e)
		}
	}
	good := []wrap.Entry{
		{Shell: "/usr/bin/env FOO=1"},
		{Shell: "  /usr/bin/env FOO=1"},
		{Argv: []string{"/usr/bin/env", "FOO=1"}},
		{Argv: []string{"/usr/bin/env", "relative-later-element"}},
	}
	if err := wrap.Validate(good); err != nil {
		t.Errorf("Validate rejected absolute entries: %v", err)
	}
	// A rejected entry in a later position is still caught.
	if err := wrap.Validate([]wrap.Entry{good[0], bad[0]}); err == nil {
		t.Error("Validate missed a relative entry after an absolute one")
	}
	// Decode(real wrap lists) then Validate: the whole path a caller takes.
	entries, err := wrap.Decode([]any{"/usr/bin/env A=1", []any{"/usr/bin/env", "B=2"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := wrap.Validate(entries); err != nil {
		t.Fatal(err)
	}
}

// Nested PATH wraps: two string entries that each prepend to $PATH both survive
// into the payload's PATH, in layer order, including inside flox.
func TestContractNestedPathWraps(t *testing.T) {
	prefixes := func(t *testing.T) (string, string) {
		d := t.TempDir()
		return filepath.Join(d, "a-bin"), filepath.Join(d, "b-bin")
	}
	pathEntries := func(a, b string) []wrap.Entry {
		return []wrap.Entry{
			{Shell: "/usr/bin/env PATH=" + a + ":$PATH"},
			{Shell: "/usr/bin/env PATH=" + b + ":$PATH"},
		}
	}
	const base = "/usr/bin:/bin:/usr/sbin:/sbin"

	t.Run("plain", func(t *testing.T) {
		a, b := prefixes(t)
		r := runChain(t, runOpts{entries: pathEntries(a, b)})
		if r.exit != 0 || r.rep == nil {
			t.Fatalf("exit=%d stderr=%s", r.exit, r.stderr)
		}
		if got, want := r.rep.env["PATH"], b+":"+a+":"+base; got != want {
			t.Fatalf("PATH = %q, want %q", got, want)
		}
	})

	t.Run("timeout in between", func(t *testing.T) {
		a, b := prefixes(t)
		entries := []wrap.Entry{
			{Shell: "/usr/bin/env PATH=" + a + ":$PATH"},
			{Argv: []string{gnuTimeout(t), "60"}},
			{Shell: "/usr/bin/env PATH=" + b + ":$PATH"},
		}
		r := runChain(t, runOpts{entries: entries})
		if r.exit != 0 || r.rep == nil {
			t.Fatalf("exit=%d stderr=%s", r.exit, r.stderr)
		}
		if got, want := r.rep.env["PATH"], b+":"+a+":"+base; got != want {
			t.Fatalf("PATH = %q, want %q", got, want)
		}
	})

	t.Run("inside flox", func(t *testing.T) {
		flox := floxEntry(t)
		a, b := prefixes(t)
		r := runChain(t, runOpts{entries: append([]wrap.Entry{flox}, pathEntries(a, b)...)})
		if r.exit != 0 || r.rep == nil {
			t.Fatalf("exit=%d stderr=%s", r.exit, r.stderr)
		}
		assertPathOrder(t, r.rep.env["PATH"], b, a)
	})

	t.Run("outside flox", func(t *testing.T) {
		flox := floxEntry(t)
		a, b := prefixes(t)
		r := runChain(t, runOpts{entries: append(pathEntries(a, b), flox)})
		if r.exit != 0 || r.rep == nil {
			t.Fatalf("exit=%d stderr=%s", r.exit, r.stderr)
		}
		assertPathOrder(t, r.rep.env["PATH"], b, a)
	})
}

func assertPathOrder(t *testing.T, path string, first, second string) {
	t.Helper()
	parts := strings.Split(path, ":")
	i, j := indexOf(parts, first), indexOf(parts, second)
	if i < 0 || j < 0 || i >= j {
		t.Fatalf("PATH must hold %q before %q: %q", first, second, path)
	}
}

func indexOf(ss []string, s string) int {
	for i, x := range ss {
		if x == s {
			return i
		}
	}
	return -1
}

// Argv injection: item data containing $(...), ;, quotes and spaces reaches the
// payload verbatim, through "$@" at every layer and through an array entry.
func TestContractArgvInjection(t *testing.T) {
	t.Run("through nested string entries", func(t *testing.T) {
		entries := []wrap.Entry{
			{Shell: "/usr/bin/env A=1"},
			{Shell: `/bin/sh -c 'exec "$@"' sh`},
			{Shell: "/usr/bin/env B=2"},
		}
		r := runChain(t, runOpts{entries: entries, args: hostile})
		if r.exit != 0 || r.rep == nil {
			t.Fatalf("exit=%d stderr=%s", r.exit, r.stderr)
		}
		if !reflect.DeepEqual(r.rep.argv, hostile) {
			t.Fatalf("got %q want %q", r.rep.argv, hostile)
		}
		noSentinel(t, r.chain)
	})

	t.Run("through an array entry", func(t *testing.T) {
		entries := []wrap.Entry{{Argv: []string{"/usr/bin/env", "FIXED=1"}}}
		r := runChain(t, runOpts{entries: entries, args: hostile})
		if r.exit != 0 || r.rep == nil {
			t.Fatalf("exit=%d stderr=%s", r.exit, r.stderr)
		}
		if !reflect.DeepEqual(r.rep.argv, hostile) {
			t.Fatalf("got %q want %q", r.rep.argv, hostile)
		}
		if r.rep.env["FIXED"] != "1" {
			t.Fatal("the array entry's own element was lost")
		}
		noSentinel(t, r.chain)
	})

	t.Run("an array entry's elements stay separate argv elements", func(t *testing.T) {
		argv, err := wrap.Compose(
			[]wrap.Entry{{Argv: []string{"/usr/bin/env", "ITEM=a b; $(x)"}}},
			[]string{"/bin/p", "arg one"},
		)
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"/usr/bin/env", "ITEM=a b; $(x)", "/bin/p", "arg one"}
		if !reflect.DeepEqual(argv, want) {
			t.Fatalf("got %q want %q", argv, want)
		}
	})
}

// Quoting inside string entries follows POSIX sh semantics.
func TestContractQuotingInStringEntries(t *testing.T) {
	cases := []struct {
		name  string
		entry string
		key   string
		want  string
	}{
		{"single quotes keep spaces", `/usr/bin/env 'A=b c'`, "A", "b c"},
		{"double quotes keep spaces", `/usr/bin/env "A=b c"`, "A", "b c"},
		{"backslash escapes a space", `/usr/bin/env A=b\ c`, "A", "b c"},
		{"single quotes keep a dollar literal", `/usr/bin/env 'A=$HOME'`, "A", "$HOME"},
		{"double quotes expand HOME", `/usr/bin/env A="$HOME"`, "A", os.Getenv("HOME")},
		{"unquoted semicolon ends nothing: it is an argument", `/usr/bin/env A='x;y'`, "A", "x;y"},
		{"embedded double quote", `/usr/bin/env 'A=say "hi"'`, "A", `say "hi"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := runChain(t, runOpts{entries: []wrap.Entry{{Shell: c.entry}}})
			if r.exit != 0 || r.rep == nil {
				t.Fatalf("exit=%d stderr=%s", r.exit, r.stderr)
			}
			if got := r.rep.env[c.key]; got != c.want {
				t.Fatalf("%s = %q, want %q", c.key, got, c.want)
			}
		})
	}

	t.Run("a later layer expands the previous layer's env", func(t *testing.T) {
		r := runChain(t, runOpts{entries: []wrap.Entry{
			{Shell: "/usr/bin/env HOME=/custom/home"},
			{Shell: `/usr/bin/env A="$HOME"`},
		}})
		if r.exit != 0 || r.rep == nil {
			t.Fatalf("exit=%d stderr=%s", r.exit, r.stderr)
		}
		if got := r.rep.env["A"]; got != "/custom/home" {
			t.Fatalf("A = %q, want the previous layer's HOME", got)
		}
	})
}

// An empty wrap list composes to just the payload, which runs.
func TestContractEmptyWrapList(t *testing.T) {
	r := runChain(t, runOpts{args: []string{"one", "two words"}})
	if r.exit != 0 || r.rep == nil {
		t.Fatalf("exit=%d stderr=%s", r.exit, r.stderr)
	}
	if !reflect.DeepEqual(r.rep.argv, []string{"one", "two words"}) {
		t.Fatalf("argv = %q", r.rep.argv)
	}
	if len(r.argv) != 4 || r.argv[2] != "one" {
		t.Fatalf("an empty wrap list must compose to just the payload, got %s", wrap.Display(r.argv))
	}
}

// Command-not-found in a string entry: a non-zero exit plus a stderr message
// naming the command, before the payload. The status is the
// shell's: 127 for a bare name and for a missing command run through env, and
// either 126 or 127 for a missing absolute path that sh itself execs (POSIX dash
// says 127, macOS /bin/sh, which is bash 3.2, says 126).
func TestContractCommandNotFound(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-such-wrapper")
	cases := []struct {
		name    string
		entries []wrap.Entry
		want    []int
	}{
		{"bare name", []wrap.Entry{{Shell: "no-such-wrapper"}}, []int{127}},
		{"absolute path, first layer", []wrap.Entry{{Shell: missing + " --flag"}}, []int{126, 127}},
		{"absolute path, middle layer", []wrap.Entry{
			{Shell: "/usr/bin/env A=1"}, {Shell: missing}, {Shell: "/usr/bin/env B=2"},
		}, []int{126, 127}},
		{"missing command behind env", []wrap.Entry{{Shell: "/usr/bin/env " + missing}}, []int{127}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := runChain(t, runOpts{entries: c.entries})
			ok := false
			for _, w := range c.want {
				ok = ok || r.exit == w
			}
			if !ok {
				t.Fatalf("exit = %d, want one of %v (stderr %q)", r.exit, c.want, r.stderr)
			}
			if !strings.Contains(r.stderr.String(), "no-such-wrapper") {
				t.Fatalf("stderr must name the missing command: %q", r.stderr)
			}
			if r.rep != nil {
				t.Fatal("the payload may not be reached")
			}
			if r.stdout.Len() != 0 {
				t.Fatalf("stdout = %q", r.stdout)
			}
		})
	}
}

// A layer's own failure stops the chain before the payload and its status comes
// back as it is; a payload's failure comes back unchanged; a layer exiting 9 is
// passed through (pg-wrap cannot remap it, because layers exec).
func TestContractFailureSemantics(t *testing.T) {
	t.Run("failing wrapper stops the chain", func(t *testing.T) {
		r := runChain(t, runOpts{entries: []wrap.Entry{{Shell: "/usr/bin/false"}}})
		if r.exit != 1 || r.rep != nil {
			t.Fatalf("exit=%d rep=%v", r.exit, r.rep)
		}
	})
	t.Run("command not found stops the chain", func(t *testing.T) {
		r := runChain(t, runOpts{entries: []wrap.Entry{{Shell: "/usr/bin/env /nonexistent/wrapper"}}})
		if r.exit != 127 || r.rep != nil {
			t.Fatalf("exit=%d rep=%v", r.exit, r.rep)
		}
	})
	t.Run("a layer exiting 9 is passed through", func(t *testing.T) {
		nine := wrap.Entry{Shell: `/bin/sh -c 'echo "layer: busy" >&2; exit 9' sh`}
		r := runChain(t, runOpts{entries: []wrap.Entry{{Shell: "/usr/bin/env A=1"}, nine}})
		if r.exit != 9 || r.rep != nil {
			t.Fatalf("exit=%d rep=%v, want 9 before the payload", r.exit, r.rep)
		}
	})
	t.Run("payload failure passes through", func(t *testing.T) {
		r := runChain(t, runOpts{
			entries: []wrap.Entry{{Shell: "/usr/bin/env A=1"}},
			env:     []string{"PROBE_EXIT=5"},
		})
		if r.exit != 5 || r.rep == nil {
			t.Fatalf("exit=%d rep=%v", r.exit, r.rep)
		}
	})
}

// ---- helpers local to this file ----

func waitFor(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", path)
}

func gone(pid int) bool {
	return syscall.Kill(pid, 0) == syscall.ESRCH
}

// waitGroupGone fails unless no process remains in the group within a few
// seconds: the orphan check.
func waitGroupGone(t *testing.T, pgid int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if syscall.Kill(-pgid, 0) == syscall.ESRCH {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("process group %d still has live members after cancel: an orphaned payload", pgid)
}

// runInTmux runs the chain as the only command of a real tmux pane and returns
// the payload's report plus the pane's own tty (taken by the pane's shell before
// the chain starts).
func runInTmux(t *testing.T, tmux string, entries []wrap.Entry) (*report, string) {
	t.Helper()
	// A short, private socket path: unix socket paths are length-limited.
	sockDir, err := os.MkdirTemp("/tmp", "wcc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sockDir) })
	sock := filepath.Join(sockDir, "s")
	t.Cleanup(func() { _ = exec.Command(tmux, "-S", sock, "kill-server").Run() })

	dir := t.TempDir()
	probe := newProbe(t, dir)
	prefix := filepath.Join(dir, "report")
	argv, err := wrap.Compose(entries, []string{probe, prefix})
	if err != nil {
		t.Fatal(err)
	}
	env := wrap.BaseEnv(os.Environ(), nil)
	script := filepath.Join(dir, "pane.sh")
	body := "#!/bin/sh\n" +
		"ps -o tty= -p $$ | tr -d ' ' > " + filepath.Join(dir, "ref-tty") + "\n" +
		"/usr/bin/env -i " + wrap.Display(env) + " " + wrap.Display(argv) + "\n" +
		"echo $? > " + filepath.Join(dir, "done") + "\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(tmux, "-S", sock, "-f", "/dev/null",
		"new-session", "-d", "-x", "100", "-y", "30", script).CombinedOutput()
	if err != nil {
		t.Skipf("tmux cannot start a session here: %v: %s", err, strings.TrimSpace(string(out)))
	}
	waitFor(t, filepath.Join(dir, "done"))
	refB, _ := os.ReadFile(filepath.Join(dir, "ref-tty"))
	return readReport(t, prefix), strings.TrimSpace(string(refB))
}
