// Package pb approves, in the LIVE hook, the `pb` verbs and flags the
// claude-marketplace pb plugin's commands and skills instruct an agent to run.
//
// # Why a legacy rule as well as the command spec
//
// Operator ruling (Phillip, 2026-10-04, verbatim): "for pb *, any command which
// is supposed to work as part of a skill should be autoapproved". The cmddesc
// schema (internal/cmddesc/registry_pb.go, pbSchema) models those verbs, but it
// feeds only the effect engine behind evaluate, replay and the golden corpus.
// The live PreToolUse hook decides through the rule chain built in
// internal/setup, which had no pb rule, so every pb call abstained live and an
// unattended (dontAsk) drain session was denied `pb drain isolate`. This rule is
// the live-hook half of that schema, in the same style as the pggitlander rule.
//
// # Classification
//
// APPROVE exactly the leaf verbs below with only the flags listed for them.
// Every pb verb declares no effect in the schema (flags are inert literals), so
// the rule checks the closed surface and that each token is plain; it does not
// judge paths.
//
//	actor                       --role V --scratchpad V
//	drain isolate               --bead V --repo V --json --actor V --isolation V --session V --git-timeout V
//	drain reclaim               --bead V --repo V --actor V --branch V --git-timeout V --json
//	gate create                 --blocks V --repo V --commit V --commits V --reason V --json
//	gate check                  --dry-run --json
//	gate attach-verified-child  --impl V --title V --gate V --actor V --reason V --json
//	                            --body-file V --acceptance V
//	unstick prepare|batch|marker|report  (flags as in the schema)
//
// plus -h/--help on any of them and a bare pb -h/--help/-v/--version.
//
// NOT approved (abstain): any other verb or flag (notably gate check
// --stale-handler, --stale-after, --last-n, --strict, pb completion, pb help), a
// positional operand, a glued --flag=value, an end-of-options "--", a missing or
// dash-leading flag value, an absolute-path executable, and any leaf with a
// redirection, process substitution, heredoc or live expansion.
package pb

import (
	"fmt"
	"strings"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

// Rule approves the modeled pb verbs.
type Rule struct{}

// New constructs the rule.
func New() *Rule { return &Rule{} }

func (r *Rule) Name() string { return "pb" }

func (r *Rule) Evaluate(input *hookio.HookInput) (hookio.RuleResult, error) {
	if input.ToolName != "Bash" {
		return hookio.NotApplicable()
	}
	parsed, err := hookio.LeavesOf(input)
	if err != nil {
		return hookio.RuleResult{}, fmt.Errorf("pb: read bash command: %w", err)
	}
	for _, pc := range parsed {
		// Bare name only: an absolute-path executable is an untrusted identity.
		if pc.Executable != "pb" {
			continue
		}
		if len(pc.Redirections) > 0 || len(pc.ProcessSubstitutions) > 0 || pc.HasHeredoc {
			continue
		}
		live := false
		for _, l := range pc.ArgLiveExpansion {
			live = live || l
		}
		if live {
			continue
		}
		if why, ok := classify(pc.Args); ok {
			return hookio.RuleResult{Decision: hookio.Approve, Reason: "pb: " + why, Module: r.Name()}, nil
		}
	}
	return hookio.NotApplicable()
}

// flag spec: true = takes one value, false = boolean.
type spec map[string]bool

var leaves = map[string]spec{
	"actor": {"--role": true, "--scratchpad": true},
	"drain isolate": {
		"--bead": true, "--repo": true, "--json": false,
		// Present in the installed `pb drain isolate --help` (pb 0.0.0-08b8af97)
		// but not yet in the cmddesc schema; the drain skills pass --actor.
		"--actor": true, "--isolation": true, "--session": true, "--git-timeout": true,
	},
	"drain reclaim": {
		"--bead": true, "--repo": true, "--actor": true, "--branch": true,
		"--git-timeout": true, "--json": false,
	},
	"gate create": {
		"--blocks": true, "--repo": true, "--commit": true, "--commits": true,
		"--reason": true, "--json": false,
	},
	"gate check": {"--dry-run": false, "--json": false},
	"gate attach-verified-child": {
		"--impl": true, "--title": true, "--gate": true, "--actor": true,
		"--reason": true, "--json": false, "--body-file": true, "--acceptance": true,
	},
	"unstick prepare": {
		"--full": false, "--id-prefix": true, "--json": false,
		"--label": true, "--root": true, "--workdir": true,
	},
	"unstick batch": {"--ids": true, "--json": false, "--name": true, "--workdir": true},
	"unstick marker": {
		"--check": false, "--export": true, "--json": false, "--outcome": true,
		"--reason": true, "--recheck-when": true,
	},
	"unstick report": {"--json": false, "--root": true, "--workdir": true},
}

var groups = map[string]bool{"drain": true, "gate": true, "unstick": true}

func isHelp(a string) bool { return a == "-h" || a == "--help" }

func classify(args []string) (string, bool) {
	if len(args) == 0 {
		return "", false
	}
	if len(args) == 1 && (isHelp(args[0]) || args[0] == "-v" || args[0] == "--version") {
		return "help/version", true
	}
	path, rest := args[0], args[1:]
	if groups[path] {
		if len(rest) == 0 {
			return "", false
		}
		path, rest = path+" "+rest[0], rest[1:]
	}
	flags, ok := leaves[path]
	if !ok {
		return "", false
	}
	for i := 0; i < len(rest); i++ {
		a := rest[i]
		if !plain(a) {
			return "", false
		}
		if isHelp(a) {
			continue
		}
		takesValue, known := flags[a]
		if !known {
			return "", false
		}
		if !takesValue {
			continue
		}
		if i+1 >= len(rest) || strings.HasPrefix(rest[i+1], "-") || !plain(rest[i+1]) {
			return "", false
		}
		i++
	}
	return "modeled verb pb " + path, true
}

// plain reports whether tok carries no shell metacharacter, quote, backslash,
// expansion or control character.
func plain(tok string) bool {
	if tok == "" || strings.ContainsAny(tok, ";&|<>()$`\\\"'{}*?[]!~#\x7f") {
		return false
	}
	for _, r := range tok {
		if r < 0x20 {
			return false
		}
	}
	return true
}
