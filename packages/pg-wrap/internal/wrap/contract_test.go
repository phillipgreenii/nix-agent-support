package wrap_test

// The fast half of the wrapper contract suite: WC-1..WC-9 and the composition
// rules, proven by driving the wrap library's composition with REAL wrappers
// on the host. No build tag: the commit-time test runner runs it. The slow
// real-process cases (WC-4, WC-5, quoting) live in contract_slow_test.go behind
// the `integration` tag. Every test name contains "Contract" or "WC".

import (
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-wrap/internal/wrap"
)

// WC-1: a wrapper runs the rest of its argv as the payload, with argv and stdin
// unmodified.
func TestWC1_ArgvAndStdinUnmodified(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
	eachWrapper(t, func(t *testing.T, w wrapperCase, e wrap.Entry) {
		// Every run owns its temp dir and env, so the wrapper x code matrix of
		// real subprocess spawns runs in parallel (eachWrapper parallelises the
		// wrapper level).
		for _, code := range []int{0, 1, 2, 9, 42, 97, 124, 127} {
			t.Run("exit"+strconv.Itoa(code), func(t *testing.T) {
				t.Parallel()
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
			})
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
	t.Parallel()
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

// WC-6: the chain works under the base environment alone, reaching its tools by
// absolute path, never prompts, and carries no variable outside the base set.
func TestWC6_BaseEnvAloneNoPromptNoLeak(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
