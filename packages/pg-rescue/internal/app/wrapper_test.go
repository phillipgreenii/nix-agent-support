package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-rescue/internal/cli"
	"github.com/phillipgreenii/pg-rescue/internal/rundir"
)

func TestEnvironmentIsIsolated(t *testing.T) {
	// TestMain points everything at a temp dir, so no test can reach the
	// developer's real ~/.local/state/pg-rescue or ~/.config/pg-rescue.
	for _, k := range []string{"HOME", "XDG_STATE_HOME", "XDG_CONFIG_HOME"} {
		if v := os.Getenv(k); !strings.Contains(v, "pg-rescue-test-") {
			t.Errorf("%s = %q; want a testenv temp dir", k, v)
		}
	}
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "PG_RESCUE_") || strings.HasPrefix(kv, "GIT_") {
			t.Errorf("environment leaks %q", kv)
		}
	}
}

// TestWrapperErrorsExit70BeforeTheCommandRuns covers every case in the
// "wrapper errors" list: each must exit 70 and the Executor (the only thing
// that would spawn the command) must never be reached.
func TestWrapperErrorsExit70BeforeTheCommandRuns(t *testing.T) {
	type tc struct {
		name   string
		config string // "" leaves the default good config in place; "-" writes no file
		args   func(h *harness) []string
		prep   func(h *harness)
		want   []string
	}
	cfgFlag := func(h *harness) []string { return []string{"--config", h.cfgPath} }
	with := func(rest ...string) func(h *harness) []string {
		return func(h *harness) []string { return append(cfgFlag(h), rest...) }
	}
	cases := []tc{
		{name: "no selector", args: with("--", "true"), want: []string{"exactly one of --handlers or --chain"}},
		{name: "both selectors", args: with("--handlers", "notify", "--chain", "sync", "--", "true"), want: []string{"mutually exclusive"}},
		{name: "empty --handlers", args: with("--handlers", "", "--", "true"), want: []string{"--handlers", "empty"}},
		{name: "empty entry in --handlers", args: with("--handlers", "notify,,notify", "--", "true"), want: []string{"empty handler name"}},
		{name: "stdin with command", args: with("--stdin", "--chain", "sync", "--verify", "true", "--", "true"), want: []string{"--stdin cannot be combined"}},
		{name: "stdin without verify", args: with("--stdin", "--chain", "sync"), want: []string{"--stdin requires --verify"}},
		{name: "no command", args: with("--chain", "sync"), want: []string{"no command"}},
		{name: "unknown flag", args: with("--chain", "sync", "--nope", "--", "true"), want: []string{"unknown flag"}},
		{
			name: "unknown handler", args: with("--handlers", "fix-smal", "--", "true"),
			want: []string{`unknown handler "fix-smal" in --handlers; configured: fix-large, fix-small, notify, p1-later`},
		},
		{
			name: "unknown handler later in list", args: with("--handlers", "notify,nope", "--", "true"),
			want: []string{`unknown handler "nope" in --handlers`},
		},
		{
			name: "unknown chain", args: with("--chain", "snyc", "--", "true"),
			want: []string{`unknown chain "snyc" in --chain; configured: sync`},
		},
		{name: "missing config file", args: func(h *harness) []string {
			return []string{"--config", filepath.Join(h.root, "absent.toml"), "--chain", "sync", "--", "true"}
		}, want: []string{"absent.toml", "cannot read file"}},
		{name: "missing config file via lookup", config: "-", args: func(h *harness) []string {
			return []string{"--chain", "sync", "--", "true"}
		}, want: []string{filepath.Join("xdg", "pg-rescue", "config.toml"), "cannot read file"}},
		{name: "invalid TOML", config: "[handler.h\n", args: with("--chain", "sync", "--", "true"), want: []string{"config ", "invalid TOML"}},
		{
			name: "unknown config key", config: "[handler.h]\ncommand=[\"x\"]\nbogus=1\n[chain.sync]\nhandlers=[\"h\"]\n",
			args: with("--chain", "sync", "--", "true"),
			want: []string{"[handler.h]", `unknown key "bogus"`, "valid keys: command, description, env, env_file, tags, timeout"},
		},
		{
			name: "missing command", config: "[handler.h]\ntimeout=\"1s\"\n[chain.sync]\nhandlers=[\"h\"]\n",
			args: with("--chain", "sync", "--", "true"), want: []string{"[handler.h]", `missing required key "command"`},
		},
		{
			name: "invalid duration", config: "[handler.h]\ncommand=[\"x\"]\ntimeout=\"soon\"\n[chain.sync]\nhandlers=[\"h\"]\n",
			args: with("--chain", "sync", "--", "true"), want: []string{"[handler.h]", `key "timeout"`, `"soon"`},
		},
		{
			name: "unsafe env_file permissions",
			prep: func(h *harness) {
				p := filepath.Join(h.root, "h.env")
				if err := os.WriteFile(p, []byte("A=1\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(p, 0o644); err != nil {
					t.Fatal(err)
				}
				h.writeConfig(h.cfgPath, "[handler.h]\ncommand=[\"x\"]\nenv_file=\""+p+"\"\n[chain.sync]\nhandlers=[\"h\"]\n")
			},
			args: with("--chain", "sync", "--", "true"), want: []string{"[handler.h]", `key "env_file"`, "unsafe permissions"},
		},
		{
			name:   "chain references unknown instance",
			config: "[handler.a]\ncommand=[\"x\"]\n[chain.sync]\nhandlers=[\"a\",\"ghost\"]\n",
			args:   with("--handlers", "a", "--", "true"),
			want:   []string{"[chain.sync]", `unknown handler "ghost"; configured: a`},
		},
		{
			name:   "unselected chain is still validated",
			config: "[handler.a]\ncommand=[\"x\"]\n[chain.good]\nhandlers=[\"a\"]\n[chain.bad]\nhandlers=[\"ghost\"]\n",
			args:   with("--chain", "good", "--", "true"), want: []string{"[chain.bad]", "ghost"},
		},
		{
			name: "run directory cannot be created",
			prep: func(h *harness) {
				blocker := filepath.Join(h.root, "blocker")
				if err := os.WriteFile(blocker, nil, 0o600); err != nil {
					t.Fatal(err)
				}
				h.env["XDG_STATE_HOME"] = filepath.Join(blocker, "state")
			},
			args: with("--chain", "sync", "--", "true"), want: []string{"cannot create run directory"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := c.config
			if cfg == "" {
				cfg = goodConfig
			}
			h := newHarness(t, "")
			if cfg != "-" {
				h.writeConfig(h.cfgPath, cfg)
			}
			if c.prep != nil {
				c.prep(h)
			}
			code, stdout, stderr := h.run(c.args(h)...)
			if code != 70 {
				t.Errorf("exit = %d; want 70\nstderr: %s", code, stderr)
			}
			if n := len(h.rec.calls); n != 0 {
				t.Errorf("the command was reached %d time(s); a wrapper error must come first", n)
			}
			if stdout != "" {
				t.Errorf("stdout must stay empty on a wrapper error, got %q", stdout)
			}
			if !strings.HasPrefix(stderr, "pg-rescue: ") {
				t.Errorf("stderr should start with the tool name: %q", stderr)
			}
			contains(t, "stderr", stderr, c.want...)
		})
	}
}

func TestNoArgumentsPrintsUsageAndExits70(t *testing.T) {
	h := newHarness(t, goodConfig)
	code, _, stderr := h.run()
	if code != 70 || !strings.Contains(stderr, "usage:") || len(h.rec.calls) != 0 {
		t.Errorf("code=%d calls=%d stderr=%q", code, len(h.rec.calls), stderr)
	}
}

func TestHelpAndVersionExitZero(t *testing.T) {
	h := newHarness(t, goodConfig)
	for _, a := range []string{"-h", "--help"} {
		code, stdout, _ := h.run(a)
		if code != 0 || !strings.Contains(stdout, "usage:") {
			t.Errorf("%s: code=%d stdout=%q", a, code, stdout)
		}
	}
	if code, stdout, _ := h.run("--version"); code != 0 || strings.TrimSpace(stdout) != "pg-rescue test" {
		t.Errorf("--version: code=%d stdout=%q", code, stdout)
	}
	if len(h.rec.calls) != 0 {
		t.Error("help/version must not reach the executor")
	}
}

func TestValidRunHandsAPlanToTheExecutor(t *testing.T) {
	h := newHarness(t, goodConfig)
	code, _, stderr := h.run("--config", h.cfgPath, "--chain", "sync", "--context", "why", "--verify", "true",
		"-vv", "--", "git", "pull", "--rebase")
	if code != 42 {
		t.Fatalf("exit = %d; want the executor's 42 (stderr: %s)", code, stderr)
	}
	if len(h.rec.calls) != 1 {
		t.Fatalf("executor calls = %d; want 1", len(h.rec.calls))
	}
	p := h.rec.calls[0]
	if p.Chain != "sync" || strings.Join(p.Handlers, ",") != "fix-small,fix-large,p1-later,notify" {
		t.Errorf("chain=%q handlers=%v", p.Chain, p.Handlers)
	}
	if strings.Join(p.Options.Argv, " ") != "git pull --rebase" || p.Options.Context != "why" || p.Options.Verbosity != cli.VeryVerbose {
		t.Errorf("options = %+v", p.Options)
	}
	if p.Cwd != h.cwd {
		t.Errorf("cwd = %q; want the wrapper's own cwd %q", p.Cwd, h.cwd)
	}
	if p.Host != "testhost" || p.StartedAt.Format("2006-01-02T15:04:05Z") != "2026-10-02T14:03:11Z" {
		t.Errorf("host=%q started=%v", p.Host, p.StartedAt)
	}
	if p.RunID != "20261002T140311Z-00010203" || !rundir.IDPattern.MatchString(p.RunID) {
		t.Errorf("run id = %q", p.RunID)
	}
	fi, err := os.Stat(p.RunDir)
	if err != nil || !fi.IsDir() || fi.Mode().Perm() != 0o700 {
		t.Errorf("run dir %q: %v %v", p.RunDir, fi, err)
	}
	if want := filepath.Join(h.env["XDG_STATE_HOME"], "pg-rescue", "runs", p.RunID); p.RunDir != want {
		t.Errorf("run dir = %q want %q", p.RunDir, want)
	}
}

func TestHandlersFlagAllowsRepeatsAndLeavesChainEmpty(t *testing.T) {
	h := newHarness(t, goodConfig)
	h.run("--config", h.cfgPath, "--handlers", "notify,fix-small,notify", "--", "true")
	if len(h.rec.calls) != 1 {
		t.Fatal("executor not reached")
	}
	p := h.rec.calls[0]
	if p.Chain != "" || strings.Join(p.Handlers, ",") != "notify,fix-small,notify" {
		t.Errorf("chain=%q handlers=%v", p.Chain, p.Handlers)
	}
}

func TestDirFlagBecomesAbsoluteCwd(t *testing.T) {
	h := newHarness(t, goodConfig)
	h.run("--config", h.cfgPath, "--chain", "sync", "-C", "relative/dir", "--", "true")
	p := h.rec.calls[0]
	if !filepath.IsAbs(p.Cwd) || !strings.HasSuffix(p.Cwd, filepath.Join("relative", "dir")) {
		t.Errorf("cwd = %q", p.Cwd)
	}
	h2 := newHarness(t, goodConfig)
	h2.run("--config", h2.cfgPath, "--chain", "sync", "-C", h2.root, "--", "true")
	if h2.rec.calls[0].Cwd != h2.root {
		t.Errorf("cwd = %q want %q", h2.rec.calls[0].Cwd, h2.root)
	}
}

func TestStdinModeReachesTheExecutorWithNoArgv(t *testing.T) {
	h := newHarness(t, goodConfig)
	h.run("--config", h.cfgPath, "--stdin", "--handlers", "notify", "--verify", "true")
	if len(h.rec.calls) != 1 || !h.rec.calls[0].Options.Stdin || h.rec.calls[0].Options.Argv != nil {
		t.Fatalf("calls = %+v", h.rec.calls)
	}
}

func TestConfigIsReadOnceAtStartup(t *testing.T) {
	h := newHarness(t, goodConfig)
	h.rt.Executor = executorFunc(func(p *Plan) int {
		// Rewriting the file mid-run must not change the plan's view of it.
		h.writeConfig(h.cfgPath, "garbage = = =")
		return 0
	})
	if code, _, stderr := h.run("--config", h.cfgPath, "--chain", "sync", "--", "true"); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
}

type executorFunc func(*Plan) int

func (f executorFunc) Execute(p *Plan) int { return f(p) }

func TestPendingExecutorReportsAndCleansUp(t *testing.T) {
	h := newHarness(t, goodConfig)
	h.rt.Executor = pendingExecutor{}
	code, _, stderr := h.run("--config", h.cfgPath, "--chain", "sync", "--", "true")
	if code != 70 || !strings.Contains(stderr, "not implemented") {
		t.Errorf("code=%d stderr=%q", code, stderr)
	}
	if dirs := h.runDirs(); len(dirs) != 0 {
		t.Errorf("run directory left behind: %v", dirs)
	}
}

func TestConfigLookupOrderEndToEnd(t *testing.T) {
	one := "[handler.one]\ncommand=[\"x\"]\n"
	h := newHarness(t, "")
	flagCfg := filepath.Join(h.root, "flag.toml")
	envCfg := filepath.Join(h.root, "env.toml")
	xdgCfg := filepath.Join(h.env["XDG_CONFIG_HOME"], "pg-rescue", "config.toml")
	homeCfg := filepath.Join(h.rt.Home, ".config", "pg-rescue", "config.toml")
	for _, p := range []string{flagCfg, envCfg, xdgCfg, homeCfg} {
		h.writeConfig(p, one)
	}
	h.env["PG_RESCUE_CONFIG"] = envCfg

	cfgLine := func(args ...string) string {
		_, stdout, stderr := h.run(append([]string{"check"}, args...)...)
		line, _, _ := strings.Cut(stdout, "\n")
		if line == "" {
			t.Fatalf("no output; stderr: %s", stderr)
		}
		return line
	}
	if got := cfgLine("--config", flagCfg); got != "config: "+flagCfg {
		t.Errorf("flag should win: %s", got)
	}
	if got := cfgLine(); got != "config: "+envCfg {
		t.Errorf("$PG_RESCUE_CONFIG should win over XDG: %s", got)
	}
	delete(h.env, "PG_RESCUE_CONFIG")
	if got := cfgLine(); got != "config: "+xdgCfg {
		t.Errorf("$XDG_CONFIG_HOME should win over ~/.config: %s", got)
	}
	delete(h.env, "XDG_CONFIG_HOME")
	if got := cfgLine(); got != "config: "+homeCfg {
		t.Errorf("~/.config is the fallback: %s", got)
	}
}
