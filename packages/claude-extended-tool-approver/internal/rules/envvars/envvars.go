package envvars

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmdparse"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

// maxReasonNameLen bounds the rendered length of a variable NAME echoed into a
// user-facing permissionDecisionReason.
const maxReasonNameLen = 64

// sanitizeReasonName renders an env var NAME safe to embed in a
// permissionDecisionReason: control characters are escaped and the result is
// truncated. Reason strings are shown to the user as a single-line permission
// prompt, so an embedded newline (or an ANSI escape) in the NAME would corrupt or
// spoof that prompt — and the pg2-3ggxm parser desync produced exactly that: a
// phantom "name" that was a multi-line command fragment, emitted verbatim by the
// live hook. This is defense-in-depth: a real variable name is a short identifier
// and passes through unchanged, so no legitimate reason string is altered, and no
// future parse defect can render a command fragment into a prompt.
func sanitizeReasonName(name string) string {
	var b strings.Builder
	b.Grow(len(name))
	for _, r := range name {
		if b.Len() >= maxReasonNameLen {
			_, _ = b.WriteString("...")
			break
		}
		switch {
		case r == '\n':
			_, _ = b.WriteString(`\n`)
		case r == '\r':
			_, _ = b.WriteString(`\r`)
		case r == '\t':
			_, _ = b.WriteString(`\t`)
		case !unicode.IsPrint(r):
			_, _ = fmt.Fprintf(&b, `\u%04x`, r)
		default:
			_, _ = b.WriteRune(r)
		}
	}
	return b.String()
}

// injectorVars are environment variables whose assignment is GUARANTEED to be a
// code-injection / library-preload vector regardless of value: setting one
// hijacks the dynamic linker or the shell's startup so an attacker-controlled
// payload runs before the command's "safe-looking" executable ever starts.
// These are DECISIVELY rejected — not merely deferred — because the env-var rule
// runs first (factory order) and the leaf's first-match-wins chain would
// otherwise let the safe-commands rule approve a bare `export` and green-light
// the injection (pg2-gkd5e). BASH_FUNC_* (exported shell functions) is handled
// by prefix.
var injectorVars = map[string]bool{
	"LD_PRELOAD":            true,
	"DYLD_INSERT_LIBRARIES": true,
	"LD_LIBRARY_PATH":       true,
	"DYLD_LIBRARY_PATH":     true,
	"BASH_ENV":              true,
	"ENV":                   true,
	"ZDOTDIR":               true,
}

// askVars are dangerous but NOT guaranteed unsafe for a given static value (a
// legitimate PATH tweak, a HOME override). Setting one can still redirect which
// binaries run or where dotfiles/credentials are read, so the assignment is
// NEVER auto-approved — it is escalated to Ask (the user decides). Ask, not
// Abstain: Abstain cannot enforce "never auto-approve" because the safe-commands
// rule approves a bare `export` and first-match-wins would let that win, so only
// a decisive Ask/Reject actually prevents auto-approval (pg2-gkd5e).
var askVars = map[string]bool{
	"PATH": true,
	"HOME": true,
}

// Rule is the unified, DECISIVE environment-assignment guard. It aggregates a
// per-(var,value) sub-verdict most-restrictive-wins and NEVER returns Approve —
// an env assignment is never auto-approved.
type Rule struct {
	exprEval hookio.Evaluator
}

// New constructs the rule with no evaluator. Value-recursion (inspecting the
// inner command of a dynamic value) is unavailable; an unclassifiable value is
// still escalated to Ask rather than guessed safe.
func New() *Rule {
	return &Rule{}
}

// NewWithEvaluator wires the engine so a value that embeds a command/process
// substitution can be recursed through the full rule chain and its verdict
// inherited (pg2-gkd5e, reusing the pg2-1q5i3 substitution machinery). The
// engine also carries the path evaluator used by that recursion.
func NewWithEvaluator(eval hookio.Evaluator) *Rule {
	return &Rule{exprEval: eval}
}

func (r *Rule) Name() string {
	return "env-vars"
}

func (r *Rule) Evaluate(input *hookio.HookInput) hookio.RuleResult {
	if input.ToolName != "Bash" {
		return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
	}
	cmdStr, err := input.BashCommand()
	if err != nil {
		return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
	}
	parsed := cmdparse.Parse(cmdStr)

	// Aggregate every assignment's sub-verdict most-restrictive-wins. Abstain is
	// the identity: a command with no (or only benign) assignments yields Abstain,
	// deferring to the rest of the chain. Approve is never produced.
	result := hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
	for _, pc := range parsed {
		for _, ev := range pc.EnvVars {
			result = hookio.MostRestrictive(result, r.evaluateAssignment(ev, input))
		}
	}
	return result
}

// evaluateAssignment returns the sub-verdict for a single NAME=VALUE assignment.
// The NAME gives the base verdict (injector→Reject, PATH/HOME→Ask, else→Abstain)
// and a VALUE that embeds an unclassifiable substitution escalates decisively
// (never auto-approve) and inherits a stronger verdict from recursing the body.
func (r *Rule) evaluateAssignment(ev cmdparse.EnvAssignment, input *hookio.HookInput) hookio.RuleResult {
	name := r.Name()

	// Base verdict from the variable NAME.
	result := hookio.RuleResult{Decision: hookio.Abstain, Module: name}
	switch {
	case injectorVars[ev.Name] || strings.HasPrefix(ev.Name, "BASH_FUNC_"):
		result = hookio.RuleResult{
			Decision: hookio.Reject,
			Reason:   "refusing to set code-injection env var: " + sanitizeReasonName(ev.Name),
			Module:   name,
		}
	case askVars[ev.Name]:
		result = hookio.RuleResult{
			Decision: hookio.Ask,
			Reason:   "setting sensitive env var requires confirmation: " + sanitizeReasonName(ev.Name),
			Module:   name,
		}
	}

	// Value handling. A value the parser classified as an unclassifiable /
	// non-safe substitution is escalated DECISIVELY to at least Ask so the
	// assignment is never auto-approved — critical for the leading form
	// (`FOO=$(evil) cmd`), where the engine's substitution choke point strips the
	// leading assignment and cannot demote it, leaving the env-var rule as the
	// only guard. The substitution body is then recursed through the full engine
	// (pg2-gkd5e value-recursion via pg2-1q5i3) so a stronger inner verdict
	// (Reject) is inherited. A value on the STATIC safe allowlist
	// (ExpansionSafeCmd, e.g. $(git rev-parse HEAD), $(mktemp -d)) or a plain
	// static/var-ref/arithmetic value carries no escalation.
	if ev.Expansion == cmdparse.ExpansionUnknown {
		result = hookio.MostRestrictive(result, hookio.RuleResult{
			Decision: hookio.Ask,
			Reason:   "env var value contains an unevaluated/unsafe expression: " + sanitizeReasonName(ev.Name),
			Module:   name,
		})
		if r.exprEval != nil {
			for _, sub := range cmdparse.EnumerateSubstitutions(ev.Value) {
				stack := []hookio.StackFrame{{RuleName: name, Command: "env-value", Expression: ev.Raw}}
				result = hookio.MostRestrictive(result, r.exprEval.EvaluateExpression(sub.Body, stack, input))
			}
		}
	}

	return result
}
