package supervisor

import (
	"slices"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-assistants/internal/config"
)

// ExitToolRule is the one permission a session is started with: nobody is
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
//	<Shell> -l -i -c 'exec "$@"' <Shell> <Claude> --allowed-tools 'Bash(pg-assistants exit)'
//	  --session-id <id> --name <Name> --model <Model> --effort <Effort> -- <Prompt>
//
// Claude runs under the operator's login shell, started as an interactive
// login shell, so the session gets the same startup files, PATH and session
// variables as a terminal (launchd and tmux pass only a bare environment).
// exec keeps the pane pid equal to the claude pid. --allowed-tools takes any
// number of values, so it comes first, a single-value flag follows it, and --
// ends the options so the prompt can never be read as one more tool rule.
func LaunchArgv(p config.Paths, a config.Assistant, sessionID string) []string {
	return []string{
		p.Shell, "-l", "-i", "-c", `exec "$@"`, p.Shell,
		p.Claude,
		"--allowed-tools", ExitToolRule,
		"--session-id", sessionID,
		"--name", a.Name,
		"--model", a.Model,
		"--effort", a.Effort,
		"--", a.Prompt,
	}
}

// LaunchEnv is an assistant's session environment, as K=V entries for tmux
// new-session -e: DISABLE_AUTO_COMPACT=1 (a full session fails instead of
// compacting, which the kill point relies on), PG_ASSISTANT_ID and
// PG_ASSISTANTS_STATE_DIR (read by `pg-assistants exit`), the five CLAUDE*
// markers set empty, then a.Env sorted by name. An a.Env entry for any of the
// first eight names is dropped. There is no PATH entry: the login shell sets
// it.
func LaunchEnv(a config.Assistant, stateDir string) []string {
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
