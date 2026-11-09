// Package pgreview approves the one command the unattended pg-review analyst
// runs: `pg-review agent q <provider> <query-name> [--arg value ...]`.
//
// # Why a legacy rule as well as the command spec
//
// The live hook decides through the rule chain built in internal/setup; the
// declarative command specs (internal/cmddesc, internal/embeddedspecs) drive
// only `evaluate`, `replay` and the future cutover. A spec alone therefore
// approves nothing in the running hook: `pg-review job doctor` submitted its
// sample command to the real hook binary and got no decision. This rule mirrors
// the pg-review command spec so the live hook approves the same shapes and
// refuses the same ones.
//
// # Classification (mirrors internal/cmddesc/registry_pg_review.go)
//
//   - `agent q` APPROVE: the query broker accepts only read-only query names the
//     provider lists for the run's lens, so the arguments are inert data to it.
//     The guarantee added here is their shape: the provider is [a-z0-9-]+ and
//     every later operand is a plain token (no shell metacharacter, quote,
//     backslash, expansion or control character).
//   - everything else (`agent file`, `agent context`, `human *`, `job *`): left
//     alone (NotApplicable), so the operator keeps the decision.
//
// A leaf with a redirection, a process substitution or a live expansion in any
// argument also defers.
package pgreview

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

// Rule approves `pg-review agent q`. It takes no configuration.
type Rule struct{}

// New constructs the pg-review rule.
func New() *Rule { return &Rule{} }

func (r *Rule) Name() string { return "pg-review" }

func (r *Rule) Evaluate(input *hookio.HookInput) (hookio.RuleResult, error) {
	if input.ToolName != "Bash" {
		return hookio.NotApplicable()
	}
	parsed, err := hookio.LeavesOf(input)
	if err != nil {
		return hookio.RuleResult{}, fmt.Errorf("pg-review: read bash command: %w", err)
	}
	for _, pc := range parsed {
		if filepath.Base(pc.Executable) != "pg-review" {
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
		if agentQ(pc.Args) {
			return hookio.RuleResult{
				Decision: hookio.Approve,
				Reason:   "pg-review: agent q is a read-only evidence query",
				Module:   r.Name(),
			}, nil
		}
	}
	return hookio.NotApplicable()
}

// agentQ reports whether args are `[--output X] agent q [--output X] [--run N]
// <provider> <rest...>` with a valid provider and at least one plain operand.
func agentQ(args []string) bool {
	i := 0
	i, ok := skipOutput(args, i)
	if !ok || i >= len(args) || args[i] != "agent" {
		return false
	}
	i++
	if i, ok = skipOutput(args, i); !ok || i >= len(args) || args[i] != "q" {
		return false
	}
	i++
	for i < len(args) && strings.HasPrefix(args[i], "-") {
		switch args[i] {
		case "--output", "--run":
			if i+1 >= len(args) || !plain(args[i+1]) {
				return false
			}
			i += 2
		default:
			return false
		}
	}
	rest := args[i:]
	if len(rest) < 2 || !provider(rest[0]) {
		return false
	}
	for _, a := range rest[1:] {
		if !plain(a) {
			return false
		}
	}
	return true
}

// skipOutput consumes any leading `--output <value>` pairs.
func skipOutput(args []string, i int) (int, bool) {
	for i < len(args) && args[i] == "--output" {
		if i+1 >= len(args) || !plain(args[i+1]) {
			return i, false
		}
		i += 2
	}
	return i, true
}

func provider(tok string) bool {
	if tok == "" {
		return false
	}
	for _, r := range tok {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
			return false
		}
	}
	return true
}

// plain mirrors cmddesc.pgReviewToken.
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
