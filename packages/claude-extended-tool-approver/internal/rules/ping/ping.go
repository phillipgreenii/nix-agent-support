// Package ping approves a bounded ICMP echo probe in the live PreToolUse hook:
// `ping -c <n> [-W <s>] <host>` and its siblings.
//
// # Why a legacy rule as well as the command spec
//
// The live hook decides through the rule chain built in internal/setup; the
// declarative command specs (internal/cmddesc, internal/embeddedspecs) drive
// only `evaluate`, `replay` and the future cutover. pingSchema alone therefore
// approved nothing in the running hook. This rule mirrors pingSchema
// (internal/cmddesc/registry_plugin_tools.go) so the live hook approves the
// same shapes and refuses the same ones.
//
// # Classification
//
//   - APPROVE: -c or -w bounds the run; every operand of -c/-w/-W/-s/-t is a
//     plain decimal 1..120; the only other flags are the inert -n -q -v -D -O
//     -4 -6 -a -U; exactly one destination made of letters, digits, '.', '-',
//     '_' and ':' that does not start with '-'.
//   - everything else (bare ping, -f, -l, -i, -I, unknown flags, no or several
//     destinations, an expanded destination): NotApplicable.
//
// A leaf with a redirection, a process substitution, a heredoc or a live
// expansion in any argument also defers.
package ping

import (
	"fmt"
	"path/filepath"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

// Rule approves bounded `ping`. It takes no configuration.
type Rule struct{}

// New constructs the ping rule.
func New() *Rule { return &Rule{} }

func (r *Rule) Name() string { return "ping" }

func (r *Rule) Evaluate(input *hookio.HookInput) (hookio.RuleResult, error) {
	if input.ToolName != "Bash" {
		return hookio.NotApplicable()
	}
	parsed, err := hookio.LeavesOf(input)
	if err != nil {
		return hookio.RuleResult{}, fmt.Errorf("ping: read bash command: %w", err)
	}
	for _, pc := range parsed {
		if filepath.Base(pc.Executable) != "ping" {
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
		if boundedProbe(pc.Args) {
			return hookio.RuleResult{
				Decision: hookio.Approve,
				Reason:   "ping: bounded probe of one plain host",
				Module:   r.Name(),
			}, nil
		}
	}
	return hookio.NotApplicable()
}

// boundedProbe mirrors pingSchema: a closed flag set, -c or -w present, and
// exactly one plain destination.
func boundedProbe(args []string) bool {
	bounded := false
	dests := 0
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "-c", "-w", "-W", "-s", "-t":
			if i+1 >= len(args) || !bound(args[i+1]) {
				return false
			}
			if a == "-c" || a == "-w" {
				bounded = true
			}
			i++
		case "-n", "-q", "-v", "-D", "-O", "-4", "-6", "-a", "-U":
		default:
			if !destination(a) {
				return false
			}
			dests++
		}
	}
	return bounded && dests == 1
}

// bound mirrors cmddesc "ping-bound": a plain decimal 1..120, no leading zero.
func bound(tok string) bool {
	if tok == "" || len(tok) > 3 || tok[0] == '0' {
		return false
	}
	n := 0
	for _, c := range tok {
		if c < '0' || c > '9' {
			return false
		}
		n = n*10 + int(c-'0')
	}
	return n >= 1 && n <= 120
}

// destination mirrors cmddesc "ping-destination".
func destination(tok string) bool {
	if tok == "" || len(tok) > 253 || tok[0] == '-' {
		return false
	}
	for _, c := range tok {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '.', c == '-', c == '_', c == ':':
		default:
			return false
		}
	}
	return true
}
