package main

import (
	"path/filepath"
	"strings"
	"testing"
)

const validConfig = `[paths]
tmux = "/example/store/bin/tmux"
claude = "/example/store/bin/claude"
pg_claude_control = "/example/store/bin/pg-claude-control"
shell = "/bin/zsh"
state_dir = "/example/state/pg-assistants"
claude_json = "/example/home/.claude.json"

[[assistant]]
id = "example-a"
name = "Example A"
model = "claude-sonnet-5-5"
effort = "medium"
cwd = "/example/work/a"
prompt = "Resume your duties per AGENTS.md."

[assistant.context]
limit = 200000
nudge = 0.80
interrupt = 0.95
`

func TestConfigCheckExitCodes(t *testing.T) {
	valid := tempConfig(t, validConfig)
	badEffort := tempConfig(t, strings.Replace(validConfig, `effort = "medium"`, `effort = "turbo"`, 1))
	badGlobal := tempConfig(t, "[grace]\nkill = \"0s\"\n"+validConfig)
	unparseable := tempConfig(t, "tick = [oops\n"+validConfig)
	relativePath := tempConfig(t, strings.Replace(validConfig, `"/example/store/bin/tmux"`, `"tmux"`, 1))
	missing := filepath.Join(t.TempDir(), "absent.toml")

	cases := []struct {
		name       string
		args       []string
		want       int
		wantStdout string // substring, when set
		wantStderr string // substring, when set
	}{
		{"valid", []string{"config", "check", "--config", valid}, 0, "ok: 1 assistants", ""},
		{"assistant problem", []string{"config", "check", "--config", badEffort}, 7, "", "assistant[example-a].effort: "},
		{"global problem", []string{"config", "check", "--config", badGlobal}, 7, "", "grace.kill: "},
		{"unparseable", []string{"config", "check", "--config", unparseable}, 7, "", "pg-assistants: "},
		{"relative path", []string{"config", "check", "--config", relativePath}, 7, "", "paths.tmux"},
		{"missing file", []string{"config", "check", "--config", missing}, 7, "", "pg-assistants: "},
		{"no --config", []string{"config", "check"}, 2, "", "--config"},
		{"unknown flag", []string{"config", "check", "--bogus"}, 2, "", ""},
		{"stray argument", []string{"config", "check", "--config", valid, "extra"}, 2, "", ""},
		{"no subcommand", []string{"config"}, 2, "", ""},
		{"unknown subcommand", []string{"config", "frob"}, 2, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, stdout, stderr := runCLI(t, tc.args...)
			if code != tc.want {
				t.Fatalf("exit = %d, want %d (stdout %q, stderr %q)", code, tc.want, stdout, stderr)
			}
			if tc.wantStdout != "" && !strings.Contains(stdout, tc.wantStdout) {
				t.Errorf("stdout = %q, want it to contain %q", stdout, tc.wantStdout)
			}
			if tc.wantStderr != "" && !strings.Contains(stderr, tc.wantStderr) {
				t.Errorf("stderr = %q, want it to contain %q", stderr, tc.wantStderr)
			}
		})
	}
}

func TestConfigCheckPrintsEachProblemOnItsOwnLine(t *testing.T) {
	body := "[grace]\nkill = \"0s\"\n" + strings.Replace(validConfig, `effort = "medium"`, `effort = "turbo"`, 1)
	code, _, stderr := runCLI(t, "config", "check", "--config", tempConfig(t, body))
	if code != 7 {
		t.Fatalf("exit = %d, want 7", code)
	}
	lines := strings.Split(strings.TrimRight(stderr, "\n"), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "grace.kill: ") || !strings.HasPrefix(lines[1], "assistant[example-a].effort: ") {
		t.Errorf("stderr lines = %q, want one line per problem, global first", lines)
	}
}

func TestTopLevelDispatch(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want int
	}{
		{"no arguments", nil, 2},
		{"unknown command", []string{"frob"}, 2},
		{"--version", []string{"--version"}, 0},
		{"--help", []string{"--help"}, 0},
		{"config --help", []string{"config", "--help"}, 0},
		{"config check --help", []string{"config", "check", "--help"}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if code, stdout, stderr := runCLI(t, tc.args...); code != tc.want {
				t.Fatalf("exit = %d, want %d (stdout %q, stderr %q)", code, tc.want, stdout, stderr)
			}
		})
	}
	if _, stdout, _ := runCLI(t, "--version"); stdout != "pg-assistants dev\n" {
		t.Errorf("--version printed %q, want %q", stdout, "pg-assistants dev\n")
	}
}
