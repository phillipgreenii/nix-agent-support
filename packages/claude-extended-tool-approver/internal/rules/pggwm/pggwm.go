// Package pggwm approves the read-only verbs of the pg-gwm worktree manager in
// the LIVE hook: list, info, preserved list, config check, reap --dry-run,
// completion and version.
//
// # Why a legacy rule as well as the command spec
//
// The cmddesc schema (internal/cmddesc/registry_pg_gwm.go) feeds only the
// effect engine that drives evaluate, replay and the golden corpus. The live
// PreToolUse hook decides through the rule chain built in internal/setup, which
// had no pg-gwm approval, so every bare read-only call abstained live while the
// corpus case (which runs the effect engine) approved. This rule is the
// live-hook half of that schema and mirrors its closed surface.
//
// # Classification
//
//   - APPROVE: the read-only verbs above, with only the flags listed here.
//   - NOT approved (left to the rest of the chain / the operator): lease-class
//     verbs, reap without --dry-run, preserved prune, serve, any unknown verb or
//     flag, and any path-valued flag (--repo, --config), which the effect
//     engine path-checks and this rule does not model.
//
// The raw-worktree deny lives in the separate pggwmworktree rule and is not
// touched here. A leaf with a redirection, process substitution, heredoc or a
// live expansion in any argument defers.
package pggwm

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

// Rule approves read-only pg-gwm verbs. It takes no configuration.
type Rule struct{}

// New constructs the pg-gwm read-only rule.
func New() *Rule { return &Rule{} }

func (r *Rule) Name() string { return "pg-gwm-readonly" }

func (r *Rule) Evaluate(input *hookio.HookInput) (hookio.RuleResult, error) {
	if input.ToolName != "Bash" {
		return hookio.NotApplicable()
	}
	parsed, err := hookio.LeavesOf(input)
	if err != nil {
		return hookio.RuleResult{}, fmt.Errorf("pg-gwm-readonly: read bash command: %w", err)
	}
	for _, pc := range parsed {
		if filepath.Base(pc.Executable) != "pg-gwm" {
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
		if readOnly(pc.Args) {
			return hookio.RuleResult{
				Decision: hookio.Approve,
				Reason:   "pg-gwm: read-only verb",
				Module:   r.Name(),
			}, nil
		}
	}
	return hookio.NotApplicable()
}

type surface struct {
	bools  map[string]bool
	values map[string]bool
	// positionals is the number of plain operands allowed (-1: any).
	positionals int
	// required is a boolean flag that MUST be present.
	required string
}

var common = []string{"--json"}

func mk(bools, values []string, pos int, required string) surface {
	s := surface{bools: map[string]bool{}, values: map[string]bool{"--format": true}, positionals: pos, required: required}
	for _, b := range append(append([]string{}, common...), bools...) {
		s.bools[b] = true
	}
	for _, v := range values {
		s.values[v] = true
	}
	return s
}

var verbs = map[string]surface{
	"list":           mk([]string{"--include-unmanaged"}, nil, 0, ""),
	"info":           mk(nil, []string{"--path", "--id", "--lease-id", "--name", "--print"}, -1, ""),
	"preserved list": mk(nil, nil, 0, ""),
	"config check":   mk(nil, nil, 0, ""),
	"reap":           mk([]string{"--all-repos"}, []string{"--deadline"}, 0, "--dry-run"),
	"completion":     {bools: map[string]bool{}, values: map[string]bool{}, positionals: -1},
	"version":        {bools: map[string]bool{}, values: map[string]bool{}, positionals: 0},
}

func readOnly(args []string) bool {
	if len(args) == 0 {
		return false
	}
	verb, rest := args[0], args[1:]
	if (verb == "preserved" || verb == "config") && len(rest) > 0 {
		verb, rest = verb+" "+rest[0], rest[1:]
	}
	s, ok := verbs[verb]
	if !ok {
		return false
	}
	if s.required != "" {
		s.bools = map[string]bool{s.required: true, "--all-repos": s.bools["--all-repos"], "--json": true}
	}
	seenRequired := s.required == ""
	pos := 0
	for i := 0; i < len(rest); i++ {
		a := rest[i]
		if !plain(a) {
			return false
		}
		switch {
		case s.bools[a]:
			if a == s.required {
				seenRequired = true
			}
		case strings.HasPrefix(a, "-"):
			name, glued, hasEq := strings.Cut(a, "=")
			if !s.values[name] {
				return false
			}
			if hasEq {
				if glued == "" {
					return false
				}
				continue
			}
			if i+1 >= len(rest) || strings.HasPrefix(rest[i+1], "-") || !plain(rest[i+1]) {
				return false
			}
			i++
		default:
			pos++
			if s.positionals >= 0 && pos > s.positionals {
				return false
			}
		}
	}
	return seenRequired
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
