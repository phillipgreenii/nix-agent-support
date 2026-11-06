package safecmds

import "strings"

// Read-only queries of the recorder store, approved in the LIVE hook.
//
// WHY THIS LIVES IN THE LEGACY RULE CHAIN. The cmddesc schemas for
// `pg-claude-code-recorder status` and `pa-monitor sessions|search`
// (registry_repo_base.go) feed only the effect engine, which decides unit tests
// and corpus replays. The live PreToolUse hook still runs setup.RuleChain, so a
// dontAsk pool session saw an abstain, and therefore a deny, for the exact forms
// the schemas approve. This branch is the live-hook half of those schemas, in the
// same closed-flag-surface style as the packet scripts.
//
// WHAT IS APPROVED, nothing else (mirrors the cmddesc schemas):
//
//   - pg-claude-code-recorder status [--json] [--short] [--allow-partial]
//   - pa-monitor sessions [--json] [--stats] [--since V] [--before V]
//   - pa-monitor search [--json] [--substring] [--session V] [--since V]
//     [--before V] WORD
//
// A flag outside the surface (status --watch, --socket, --config), an extra or
// missing positional, a glued --flag=value, a value that opens with "-", or any
// word carrying a live shell expansion is refused. serve, remove, coverage,
// the other pa-monitor subcommands and pg-claude-control are never approved.
type readOnlyQuerySpec struct {
	bools  map[string]bool
	values map[string]bool
	// positionals is the exact number of non-flag words the query takes.
	positionals int
}

var readOnlyQueries = map[string]readOnlyQuerySpec{
	"pg-claude-code-recorder status": {
		bools: map[string]bool{"--json": true, "--short": true, "--allow-partial": true},
	},
	"pa-monitor sessions": {
		bools:  map[string]bool{"--json": true, "--stats": true},
		values: map[string]bool{"--since": true, "--before": true},
	},
	"pa-monitor search": {
		bools:       map[string]bool{"--json": true, "--substring": true},
		values:      map[string]bool{"--session": true, "--since": true, "--before": true},
		positionals: 1,
	},
}

// readOnlyQueryIssue returns "" and true when basename+args is an approved
// read-only query, ("", false) when it is not one of these commands at all, and
// a reason with true when it is one but outside the approved surface.
func readOnlyQueryIssue(basename string, args []string, live []bool) (string, bool) {
	if basename != "pg-claude-code-recorder" && basename != "pa-monitor" {
		return "", false
	}
	if len(args) == 0 {
		return "has no read-only query subcommand", true
	}
	spec, ok := readOnlyQueries[basename+" "+args[0]]
	if !ok {
		return args[0] + " is not an approved read-only query", true
	}
	isLive := func(i int) bool { return i >= len(live) || live[i] }
	pos := 0
	for i := 1; i < len(args); i++ {
		a := args[i]
		if isLive(i) {
			return "has a dynamically-expanded argument " + a, true
		}
		switch {
		case spec.bools[a]:
		case spec.values[a]:
			if i+1 >= len(args) {
				return "flag " + a + " is missing its value", true
			}
			i++
			if isLive(i) || strings.HasPrefix(args[i], "-") {
				return "flag " + a + " has a dynamic or option-like value " + args[i], true
			}
		case strings.HasPrefix(a, "-"):
			return "has an argument outside its closed flag surface: " + a, true
		default:
			pos++
		}
	}
	if pos != spec.positionals {
		return "takes exactly the documented positional arguments", true
	}
	return "", true
}
