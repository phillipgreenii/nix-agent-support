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
	got := LaunchEnv(a, "/s")
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

// The extra environment comes after the fixed entries, sorted by name, and
// can override none of them.
func TestLaunchEnvExtrasSortedAndNeverOverride(t *testing.T) {
	fixed := LaunchEnv(exampleAssistant(), "/s")
	a := exampleAssistant()
	a.Env = map[string]string{"ZED": "z", "ALPHA": "a", "MID": "m"}
	for _, e := range fixed {
		k, _, _ := strings.Cut(e, "=")
		a.Env[k] = "overridden"
	}
	got := LaunchEnv(a, "/s")
	want := append(slices.Clone(fixed), "ALPHA=a", "MID=m", "ZED=z")
	if !slices.Equal(got, want) {
		t.Errorf("LaunchEnv =\n  %q\nwant\n  %q", got, want)
	}
	if len(fixed) != 8 {
		t.Errorf("fixed entries = %d, want 8: %q", len(fixed), fixed)
	}
}
