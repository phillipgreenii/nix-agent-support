//go:build integration

package wrap_test

// The slow real-process half of the wrapper contract suite: WC-4 (a real tmux
// pane's controlling terminal), WC-5 (signals and group cancel through real
// wrappers) and the string-entry quoting cases. Each spawns many real
// processes and takes seconds, so they are NOT unit tests (V-4): the unit gate
// skips them and the `pg-wrap-integration-tests` flake check runs them.

import (
	"context"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-wrap/internal/wrap"
)

// WC-4: the payload keeps the controlling terminal and runs in the foreground
// process group. Proven under a real tmux pane: the payload's tty equals the
// pane's tty and its process group is the terminal's foreground group.
func TestWC4_ControllingTerminalAndForegroundGroup(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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

// Quoting inside string entries follows POSIX sh semantics.
func TestContractQuotingInStringEntries(t *testing.T) {
	t.Parallel()
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
	t.Cleanup(func() { _ = exec.CommandContext(context.Background(), tmux, "-S", sock, "kill-server").Run() })

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
	out, err := exec.CommandContext(t.Context(), tmux, "-S", sock, "-f", "/dev/null",
		"new-session", "-d", "-x", "100", "-y", "30", script).CombinedOutput()
	if err != nil {
		t.Skipf("tmux cannot start a session here: %v: %s", err, strings.TrimSpace(string(out)))
	}
	waitFor(t, filepath.Join(dir, "done"))
	refB, _ := os.ReadFile(filepath.Join(dir, "ref-tty"))
	return readReport(t, prefix), strings.TrimSpace(string(refB))
}
