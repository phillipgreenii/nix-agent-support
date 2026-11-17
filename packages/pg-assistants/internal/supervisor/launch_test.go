package supervisor

import (
	"slices"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-assistants/internal/config"
)

func exampleAssistant() config.Assistant {
	return config.Assistant{
		ID:     "example-a",
		Name:   "Example A",
		Model:  "claude-sonnet-5-5",
		Effort: "medium",
		Prompt: "Resume your duties.",
	}
}

func TestLaunchArgv(t *testing.T) {
	got := LaunchArgv(config.Paths{Shell: "/bin/zsh", Claude: "/c/claude"}, exampleAssistant(), "u1")
	want := []string{
		"/bin/zsh", "-l", "-i", "-c", `exec "$@"`, "/bin/zsh",
		"/c/claude", "--allowed-tools", "Bash(pg-assistants exit)",
		"--session-id", "u1", "--name", "Example A", "--model", "claude-sonnet-5-5",
		"--effort", "medium", "--", "Resume your duties.",
	}
	if !slices.Equal(got, want) {
		t.Errorf("LaunchArgv =\n  %q\nwant\n  %q", got, want)
	}
}

// --allowed-tools takes any number of values, so the token after its one value
// MUST be a flag (or a positional argument would become a second tool rule),
// and -- MUST end the options so the prompt is the only positional argument.
func TestLaunchArgvVariadicFlagIsFollowedByAFlag(t *testing.T) {
	a := exampleAssistant()
	a.Prompt = "Resume, then --keep going"
	argv := LaunchArgv(config.Paths{Shell: "/bin/zsh", Claude: "/c/claude"}, a, "u1")

	i := slices.Index(argv, "--allowed-tools")
	if i < 0 || i+2 >= len(argv) {
		t.Fatalf("no --allowed-tools with a value and a following token in %q", argv)
	}
	if next := argv[i+2]; !strings.HasPrefix(next, "--") {
		t.Errorf("token after the --allowed-tools value = %q, want a flag", next)
	}
	end := slices.Index(argv, "--")
	if end < 0 {
		t.Fatalf("no -- in %q", argv)
	}
	if after := argv[end+1:]; !slices.Equal(after, []string{a.Prompt}) {
		t.Errorf("after -- = %q, want only the prompt %q", after, a.Prompt)
	}
}

func TestLaunchEnv(t *testing.T) {
	a := exampleAssistant()
	a.Env = map[string]string{"PG_ASSISTANT_ID": "x", "FOO": "bar"}
	got := LaunchEnv(a, "/s", nil)
	want := []string{
		"DISABLE_AUTO_COMPACT=1",
		"PG_ASSISTANT_ID=example-a",
		"PG_ASSISTANTS_STATE_DIR=/s",
		"CLAUDECODE=",
		"CLAUDE_CODE_CHILD_SESSION=",
		"CLAUDE_CODE_ENTRYPOINT=",
		"CLAUDE_CODE_SESSION_ID=",
		"CLAUDE_CODE_EXECPATH=",
		"FOO=bar",
	}
	if !slices.Equal(got, want) {
		t.Errorf("LaunchEnv =\n  %q\nwant\n  %q", got, want)
	}
	for _, e := range got {
		if strings.HasPrefix(e, "PATH=") {
			t.Errorf("LaunchEnv sets %q; the login shell sets PATH", e)
		}
	}
}

// The config's blank_env names come after the CLAUDE* markers, sorted and
// once each, set and empty. A name the daemon itself sets, a marker, or a name
// the assistant's own env sets is not blanked: the fixed entry or the
// assistant's value stands.
func TestLaunchEnvBlanksTheConfigsNames(t *testing.T) {
	a := exampleAssistant()
	a.Env = map[string]string{"FOO": "bar", "EXAMPLE_KEEP": "mine"}
	blank := []string{
		"__HM_SESS_VARS_SOURCED", "PG_ASSISTANTS_CONFIG_HASH", "__HM_SESS_VARS_SOURCED",
		"PG_ASSISTANT_ID", "CLAUDECODE", "EXAMPLE_KEEP", "XPC_SERVICE_NAME",
	}
	got := LaunchEnv(a, "/s", blank)
	want := []string{
		"DISABLE_AUTO_COMPACT=1",
		"PG_ASSISTANT_ID=example-a",
		"PG_ASSISTANTS_STATE_DIR=/s",
		"CLAUDECODE=",
		"CLAUDE_CODE_CHILD_SESSION=",
		"CLAUDE_CODE_ENTRYPOINT=",
		"CLAUDE_CODE_SESSION_ID=",
		"CLAUDE_CODE_EXECPATH=",
		"PG_ASSISTANTS_CONFIG_HASH=",
		"XPC_SERVICE_NAME=",
		"__HM_SESS_VARS_SOURCED=",
		"EXAMPLE_KEEP=mine",
		"FOO=bar",
	}
	if !slices.Equal(got, want) {
		t.Errorf("LaunchEnv =\n  %q\nwant\n  %q", got, want)
	}
}

// The tokens are handed to x/tmux as they are: it escapes a trailing ";"
// (and its own tests prove it), so none is escaped here.
func TestLaunchPassesTokensUnescaped(t *testing.T) {
	a := exampleAssistant()
	a.Name, a.Model, a.Prompt = "Example A;", "claude-sonnet-5-5;", "Resume now;"
	a.Env = map[string]string{"FOO": "bar;"}
	p := config.Paths{Shell: "/bin/zsh;", Claude: "/c/claude;"}

	argv := LaunchArgv(p, a, "u1")
	for _, tok := range []string{"/bin/zsh;", "/c/claude;", "Example A;", "claude-sonnet-5-5;", "Resume now;"} {
		if !slices.Contains(argv, tok) {
			t.Errorf("LaunchArgv = %q, want it to hold %q", argv, tok)
		}
	}
	env := LaunchEnv(a, "/s;", nil)
	for _, e := range []string{"PG_ASSISTANTS_STATE_DIR=/s;", "FOO=bar;"} {
		if !slices.Contains(env, e) {
			t.Errorf("LaunchEnv = %q, want it to hold %q", env, e)
		}
	}
	for _, e := range append(argv, env...) {
		if strings.Contains(e, `\;`) {
			t.Errorf("%q is escaped before x/tmux", e)
		}
	}
}

func TestLaunchEnvExtrasSortedAndNeverOverride(t *testing.T) {
	fixed := LaunchEnv(exampleAssistant(), "/s", nil)
	a := exampleAssistant()
	a.Env = map[string]string{"ZED": "z", "ALPHA": "a", "MID": "m"}
	for _, e := range fixed {
		k, _, _ := strings.Cut(e, "=")
		a.Env[k] = "overridden"
	}
	got := LaunchEnv(a, "/s", nil)
	want := append(slices.Clone(fixed), "ALPHA=a", "MID=m", "ZED=z")
	if !slices.Equal(got, want) {
		t.Errorf("LaunchEnv =\n  %q\nwant\n  %q", got, want)
	}
	if len(fixed) != 8 {
		t.Errorf("fixed entries = %d, want 8: %q", len(fixed), fixed)
	}
}
