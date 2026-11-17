//go:build integration

package supervisor

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-assistants/internal/config"
	"github.com/phillipgreenii/x/tmux"
)

// wantBlanked are the CLAUDE* markers a session MUST see set and empty. The
// test sets them in the tmux server's environment so the blanking is
// observable. The list is written out here, not taken from LaunchEnv's, so a
// marker dropped there fails this test.
var wantBlanked = []string{
	"CLAUDECODE", "CLAUDE_CODE_CHILD_SESSION", "CLAUDE_CODE_ENTRYPOINT",
	"CLAUDE_CODE_SESSION_ID", "CLAUDE_CODE_EXECPATH",
}

// recordedEnv names the variables the stand-in claude records when set: the
// markers, the fixed entries, the extra entry, PATH and the startup-file
// markers.
var recordedEnv = append(slices.Clone(wantBlanked),
	"DISABLE_AUTO_COMPACT", "PG_ASSISTANT_ID", "PG_ASSISTANTS_STATE_DIR",
	"FOO", "PATH", "PGA_LOGIN", "PGA_INTERACTIVE", "PGA_SESSVAR", "PGA_SYSVAR", "PGA_DAEMON_ONLY")

// chainAssistant is the assistant every case starts; a case changes fields.
func chainAssistant() config.Assistant {
	return config.Assistant{
		ID: "example-a", Name: "Example A", Model: "claude-sonnet-5-5", Effort: "medium",
		Prompt: `Resume "now", please`,
		Env:    map[string]string{"FOO": "bar"},
	}
}

// TestRealLaunchChain runs LaunchArgv, LaunchEnv and LaunchCwd through a
// real tmux server and a real login shell, up to a stand-in claude that
// records what it was given.
func TestRealLaunchChain(t *testing.T) {
	// shCase starts a under /bin/sh in cwd (a name under the test dir) with
	// no startup files of the operator's.
	shCase := func(t *testing.T, a config.Assistant, cwd string) launchRecord {
		shell := requireTool(t, "/bin/sh")
		c, dir := newRealTmux(t)
		t.Setenv("HOME", mkdir(t, dir, "home")) // no ~/.profile
		t.Setenv("ENV", "")                     // no interactive startup file
		return runChain(t, c, dir, shell, a, cwd, nil)
	}

	t.Run("sh", func(t *testing.T) {
		r := shCase(t, chainAssistant(), "cwd")
		if got := r.env["FOO"]; got != "bar" {
			t.Errorf("FOO = %q, want bar", got)
		}
	})

	// tmux reads an argument ending in ";" as the end of a command: unescaped,
	// a name like this fails new-session ("unknown command: --model"), an env
	// value does too, and the prompt loses its ";".
	t.Run("trailing semicolons", func(t *testing.T) {
		a := chainAssistant()
		a.Name, a.Model, a.Prompt = "Example A;", "claude-sonnet-5-5;", "Resume now;"
		a.Env = map[string]string{"FOO": "bar;"}
		r := shCase(t, a, "cwd;")
		if got := r.env["FOO"]; got != "bar;" {
			t.Errorf("FOO = %q, want %q", got, "bar;")
		}
	})

	// Unescaped, a prompt of just ";" disappears.
	t.Run("lone semicolon prompt", func(t *testing.T) {
		a := chainAssistant()
		a.Prompt = ";"
		shCase(t, a, "cwd")
	})

	// The operator's own startup files run: the login shell is interactive
	// (-i) as well as a login shell (-l), as in a terminal.
	t.Run("zsh login startup files", func(t *testing.T) {
		shell := requireTool(t, "zsh")
		c, dir := newRealTmux(t)
		zdot, bin := mkdir(t, dir, "zdot"), mkdir(t, dir, "zbin")
		t.Setenv("HOME", zdot)
		t.Setenv("ZDOTDIR", zdot)
		writeFile(t, filepath.Join(zdot, ".zshenv"), 0o600,
			// The host's /etc/zprofile, /etc/zshrc and /etc/zlogin are not
			// read, so the result does not depend on them (a stock macOS
			// /etc/zprofile reorders PATH through path_helper).
			"unsetopt GLOBAL_RCS\n"+
				"export PATH="+shellQuote(bin)+":\"$PATH\"\n")
		writeFile(t, filepath.Join(zdot, ".zprofile"), 0o600, "export PGA_LOGIN=zprofile\n")
		writeFile(t, filepath.Join(zdot, ".zshrc"), 0o600, "export PGA_INTERACTIVE=zshrc\n")

		r := runChain(t, c, dir, shell, chainAssistant(), "cwd", nil)
		if p := r.env["PATH"]; !strings.HasPrefix(p, bin+":") {
			t.Errorf("PATH = %q, want it to start with %q", p, bin+":")
		}
		if got := r.env["PGA_LOGIN"]; got != "zprofile" {
			t.Errorf("PGA_LOGIN = %q, want zprofile (the login file ran)", got)
		}
		if got := r.env["PGA_INTERACTIVE"]; got != "zshrc" {
			t.Errorf("PGA_INTERACTIVE = %q, want zshrc (the interactive file ran)", got)
		}
	})

	// The tmux server keeps the environment of the client that started it:
	// the daemon's own login shell, which already ran the operator's startup
	// files and so exported their run-once guards (home-manager's
	// hm-session-vars.sh returns when its guard is non-empty; nix-darwin's
	// /etc/zshenv skips set-environment unless its guard is empty). Without
	// blanking them every session would skip those files and keep the values
	// frozen when the server started. The guards here mirror both forms.
	t.Run("zsh reruns guarded startup files", func(t *testing.T) {
		shell := requireTool(t, "zsh")
		c, dir := newRealTmux(t)
		zdot := mkdir(t, dir, "zdot")
		t.Setenv("HOME", zdot)
		t.Setenv("ZDOTDIR", zdot)
		// As the daemon's login shell left them, when the server started.
		t.Setenv("PGA_HM_GUARD", "1")
		t.Setenv("PGA_SYS_GUARD", "1")
		t.Setenv("PGA_SESSVAR", "stale")
		t.Setenv("PGA_SYSVAR", "stale")
		t.Setenv("PGA_DAEMON_ONLY", "daemon")
		writeFile(t, filepath.Join(zdot, ".zshenv"), 0o600,
			"unsetopt GLOBAL_RCS\n"+
				". \"$ZDOTDIR/session-vars.sh\"\n"+
				"if [ -z \"${PGA_SYS_GUARD-}\" ]; then . \"$ZDOTDIR/set-environment\"; fi\n")
		writeFile(t, filepath.Join(zdot, "session-vars.sh"), 0o600,
			"if [ -n \"${PGA_HM_GUARD-}\" ]; then return; fi\n"+
				"export PGA_HM_GUARD=1\n"+
				"export PGA_SESSVAR=fresh\n")
		writeFile(t, filepath.Join(zdot, "set-environment"), 0o600,
			"export PGA_SYS_GUARD=1\n"+
				"export PGA_SYSVAR=fresh\n")

		r := runChain(t, c, dir, shell, chainAssistant(), "cwd",
			[]string{"PGA_HM_GUARD", "PGA_SYS_GUARD", "PGA_DAEMON_ONLY"})
		if got := r.env["PGA_SESSVAR"]; got != "fresh" {
			t.Errorf("PGA_SESSVAR = %q, want fresh (the -n guarded file ran again)", got)
		}
		if got := r.env["PGA_SYSVAR"]; got != "fresh" {
			t.Errorf("PGA_SYSVAR = %q, want fresh (the -z guarded file ran again)", got)
		}
		if got, ok := r.env["PGA_DAEMON_ONLY"]; !ok || got != "" {
			t.Errorf("PGA_DAEMON_ONLY = %q (set %v), want set and empty", got, ok)
		}
	})
}

// launchRecord is what the stand-in claude recorded.
type launchRecord struct {
	pid  int
	argv []string          // its arguments, without argv[0]
	env  map[string]string // the recordedEnv variables that were set
	cwd  string            // its working directory, symlinks resolved
}

// runChain starts a through shell with the stand-in claude, in dir/cwdName,
// waits for its record and checks what every launch has in common: the pane
// pid is the claude pid, claude got exactly the intended arguments (the
// prompt as the one argument after --) in the intended directory, the fixed
// environment arrived and the CLAUDE* markers are set and empty. The
// arguments are compared with the assistant's own values, not with
// LaunchArgv's tokens, which carry tmux's escapes.
func runChain(t *testing.T, c tmux.Client, dir, shell string, a config.Assistant, cwdName string, blank []string) launchRecord {
	t.Helper()
	ctx := t.Context()
	for _, k := range wantBlanked {
		t.Setenv(k, "outer") // as if the daemon ran under another Claude
	}
	claude, rec := fakeClaude(t, dir)
	stateDir := filepath.Join(dir, "state")
	a.Cwd = mkdir(t, dir, cwdName)
	argv := LaunchArgv(config.Paths{Shell: shell, Claude: claude}, a, "example-session")
	if err := c.NewSession(ctx, a.ID, LaunchCwd(a), LaunchEnv(a, stateDir, blank), argv); err != nil {
		t.Fatal(err)
	}
	waitForRecord(t, c, a.ID, rec)

	r := readRecord(t, rec)
	pid, err := c.PanePID(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if pid != r.pid {
		t.Errorf("pane pid %d != claude pid %d: the shell did not exec claude", pid, r.pid)
	}
	want := []string{
		"--allowed-tools", "Bash(pg-assistants exit)", "--session-id", "example-session",
		"--name", a.Name, "--model", a.Model, "--effort", a.Effort, "--", a.Prompt,
	}
	if !slices.Equal(r.argv, want) {
		t.Errorf("claude argv =\n  %q\nwant\n  %q", r.argv, want)
	}
	wantCwd, err := filepath.EvalSymlinks(a.Cwd)
	if err != nil {
		t.Fatal(err)
	}
	if r.cwd != wantCwd {
		t.Errorf("claude cwd = %q, want %q", r.cwd, wantCwd)
	}
	for k, want := range map[string]string{
		"DISABLE_AUTO_COMPACT":    "1",
		"PG_ASSISTANT_ID":         "example-a",
		"PG_ASSISTANTS_STATE_DIR": stateDir,
	} {
		if got, ok := r.env[k]; !ok || got != want {
			t.Errorf("%s = %q (set %v), want %q", k, got, ok, want)
		}
	}
	for _, k := range wantBlanked {
		if got, ok := r.env[k]; !ok || got != "" {
			t.Errorf("%s = %q (set %v), want set and empty", k, got, ok)
		}
	}
	return r
}

// fakeClaude writes a stand-in claude to dir/fake/claude. It records $$, its
// working directory (pwd -P), its arguments and the recordedEnv variables
// that are set (both NUL-separated)
// under dir/rec, writes dir/rec/done last, then execs sleep so the pane stays
// alive. It uses only shell builtins until that exec.
func fakeClaude(t *testing.T, dir string) (bin, rec string) {
	t.Helper()
	sleep := requireTool(t, "sleep")
	rec = mkdir(t, dir, "rec")
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	fmt.Fprintf(&b, "rec=%s\n", shellQuote(rec))
	b.WriteString(`printf '%s' "$$" > "$rec/pid"` + "\n")
	b.WriteString(`pwd -P > "$rec/cwd"` + "\n")
	b.WriteString(`: > "$rec/argv"` + "\n")
	b.WriteString(`for a in "$@"; do printf '%s\0' "$a" >> "$rec/argv"; done` + "\n")
	b.WriteString(`: > "$rec/env"` + "\n")
	for _, k := range recordedEnv {
		fmt.Fprintf(&b, `if [ "${%[1]s+set}" = set ]; then printf '%%s=%%s\0' %[1]s "$%[1]s" >> "$rec/env"; fi`+"\n", k)
	}
	b.WriteString(`: > "$rec/done"` + "\n")
	fmt.Fprintf(&b, "exec %s 600\n", shellQuote(sleep))
	bin = filepath.Join(mkdir(t, dir, "fake"), "claude")
	writeFile(t, bin, 0o755, b.String())
	return bin, rec
}

// waitForRecord waits up to 10s for rec/done. On a timeout it reports the
// pane's state, with its exit status and output when it has died.
func waitForRecord(t *testing.T, c tmux.Client, name, rec string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(rec, "done")); err == nil {
			return
		}
		if time.Now().After(deadline) {
			st, err := c.State(t.Context(), name)
			if err == nil && st.Exists && st.Dead {
				d, derr := c.Dead(t.Context(), name, 40)
				t.Fatalf("no record within 10s; the pane died: %+v (%v)", d, derr)
			}
			t.Fatalf("no record within 10s; pane state %+v (%v)", st, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func readRecord(t *testing.T, rec string) launchRecord {
	t.Helper()
	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join(rec, name))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	pid, err := strconv.Atoi(read("pid"))
	if err != nil {
		t.Fatalf("recorded pid: %v", err)
	}
	r := launchRecord{
		pid:  pid,
		argv: splitNUL(read("argv")),
		env:  map[string]string{},
		cwd:  strings.TrimSuffix(read("cwd"), "\n"),
	}
	for _, kv := range splitNUL(read("env")) {
		k, v, _ := strings.Cut(kv, "=")
		r.env[k] = v
	}
	return r
}

func splitNUL(s string) []string {
	parts := strings.Split(s, "\x00")
	return parts[:len(parts)-1] // every entry ends with a NUL
}

func mkdir(t *testing.T, parent, name string) string {
	t.Helper()
	p := filepath.Join(parent, name)
	if err := os.MkdirAll(p, 0o700); err != nil {
		t.Fatal(err)
	}
	return p
}

func writeFile(t *testing.T, path string, mode os.FileMode, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
