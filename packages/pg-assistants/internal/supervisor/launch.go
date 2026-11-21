package supervisor

import (
	"slices"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-assistants/internal/config"
)

// ExitToolRule is the permission every session is started with (an assistant's
// own allowed_tools follow it): nobody is
// attached to answer a prompt, and an assistant asks to stop by running this
// exact command on its own. The rule only adds an allowance; every other
// permission comes from the operator's own settings.
const ExitToolRule = "Bash(pg-assistants exit)"

// blankedMarkers are the CLAUDE* variables a session is started with empty,
// so it never mistakes itself for a child of another Claude.
var blankedMarkers = []string{
	"CLAUDECODE",
	"CLAUDE_CODE_CHILD_SESSION",
	"CLAUDE_CODE_ENTRYPOINT",
	"CLAUDE_CODE_SESSION_ID",
	"CLAUDE_CODE_EXECPATH",
}

// LaunchArgv is an assistant's pane command:
//
//	<Shell> -l -i -c 'exec "$@"' <Shell> <Claude> --allowed-tools 'Bash(pg-assistants exit)' [<AllowedTools>...]
//	  --session-id <id> --name <Name> --model <Model> --effort <Effort> -- <Prompt>
//
// Claude runs under the operator's login shell, started as an interactive
// login shell, so the session gets the same startup files, PATH and session
// variables as a terminal (launchd and tmux pass only a bare environment).
// exec keeps the pane pid equal to the claude pid. --allowed-tools takes any
// number of values, so it comes first, a single-value flag follows it, and --
// ends the options so the prompt can never be read as one more tool rule.
// Tokens are passed as they are: x/tmux escapes what tmux's parser needs.
func LaunchArgv(p config.Paths, a config.Assistant, sessionID string) []string {
	argv := []string{
		p.Shell, "-l", "-i", "-c", `exec "$@"`, p.Shell,
		p.Claude,
		"--allowed-tools", ExitToolRule,
	}
	argv = append(argv, a.AllowedTools...)
	return append(
		argv,
		"--session-id", sessionID,
		"--name", a.Name,
		"--model", a.Model,
		"--effort", a.Effort,
		"--", a.Prompt,
	)
}

// LaunchEnv is an assistant's session environment, as K=V entries for tmux
// new-session -e: DISABLE_AUTO_COMPACT=1 (a full session fails instead of
// compacting, which the kill point relies on), PG_ASSISTANT_ID and
// PG_ASSISTANTS_STATE_DIR (read by `pg-assistants exit`), the five CLAUDE*
// markers set empty, then the blank names (the config's blank_env) set
// empty, sorted and once each, then a.Env sorted by name. An a.Env entry for
// any of the first eight names is dropped, and a blank name that is one of
// them or is set by a.Env is not blanked. There is no PATH entry: the login
// shell sets it.
//
// The blank names matter because the tmux server keeps the environment of
// the client that started it, the daemon, which runs under the operator's
// login shell: that shell already ran the startup files and exported their
// run-once guards, and every pane starts from the server's environment. An
// empty guard makes the session's own login shell run those files again
// (each guard tests for an empty value), so the session gets the current
// PATH and session variables rather than those of the server's start.
func LaunchEnv(a config.Assistant, stateDir string, blank []string) []string {
	env := []string{
		"DISABLE_AUTO_COMPACT=1",
		"PG_ASSISTANT_ID=" + a.ID,
		"PG_ASSISTANTS_STATE_DIR=" + stateDir,
	}
	for _, k := range blankedMarkers {
		env = append(env, k+"=")
	}
	reserved := map[string]bool{"DISABLE_AUTO_COMPACT": true, "PG_ASSISTANT_ID": true, "PG_ASSISTANTS_STATE_DIR": true}
	for _, k := range blankedMarkers {
		reserved[k] = true
	}
	blanks := make([]string, 0, len(blank))
	for _, k := range blank {
		if _, own := a.Env[k]; !reserved[k] && !own {
			blanks = append(blanks, k)
		}
	}
	slices.Sort(blanks)
	for _, k := range slices.Compact(blanks) {
		env = append(env, k+"=")
	}
	keys := make([]string, 0, len(a.Env))
	for k := range a.Env {
		if !reserved[k] {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	for _, k := range keys {
		env = append(env, k+"="+a.Env[k])
	}
	return env
}
