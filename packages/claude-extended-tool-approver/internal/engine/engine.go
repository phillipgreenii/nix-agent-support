package engine

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmdparse"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/patheval"
)

// devFdPattern matches /dev/fd/<n> for any file-descriptor number.
var devFdPattern = regexp.MustCompile(`^/dev/fd/[0-9]+$`)

// isSafeRedirectTarget reports whether path is one of the standard special
// device files that are always safe as an I/O redirection target — for reading
// (stdin) and writing (stdout/stderr) alike: /dev/null, /dev/stdout,
// /dev/stderr, /dev/tty, and /dev/fd/<n>. The PathEvaluator does not model these
// pseudo-files (it classifies them PathUnknown), so without this short-circuit a
// redirect to one would demote an otherwise-approved command to Abstain
// (pg2-9ctmb). Kept redirect-scoped on purpose: it does NOT make these paths
// writable to the rest of the ruleset (e.g. `rm /dev/null` is unaffected).
func isSafeRedirectTarget(path string) bool {
	switch path {
	case "/dev/null", "/dev/stdout", "/dev/stderr", "/dev/tty":
		return true
	}
	return devFdPattern.MatchString(path)
}

type Engine struct {
	rules    []hookio.RuleModule
	pathEval *patheval.PathEvaluator
	trace    bool
}

func New(rules ...hookio.RuleModule) *Engine {
	return &Engine{rules: rules}
}

// RegisterRules sets the rule list (for create-then-register pattern).
func (e *Engine) RegisterRules(rules ...hookio.RuleModule) {
	e.rules = rules
}

// SetPathEvaluator sets the path evaluator for I/O redirection evaluation.
func (e *Engine) SetPathEvaluator(pe *patheval.PathEvaluator) {
	e.pathEval = pe
}

// SetTrace enables or disables trace collection and stderr logging.
func (e *Engine) SetTrace(enabled bool) {
	e.trace = enabled
}

func (e *Engine) Evaluate(input *hookio.HookInput) hookio.RuleResult {
	var trace []hookio.TraceEntry

	for _, rule := range e.rules {
		result := rule.Evaluate(input)

		if e.trace {
			entry := hookio.TraceEntry{
				RuleName: rule.Name(),
				Decision: result.Decision,
				Reason:   result.Reason,
			}
			trace = append(trace, entry)
			fmt.Fprintf(os.Stderr, "claude-extended-tool-approver: TRACE %s -> %s: %s\n",
				rule.Name(), result.Decision, result.Reason)
		}

		if result.Decision != hookio.Abstain {
			if input.ToolName == "Bash" {
				if cmd, err := input.BashCommand(); err == nil {
					if comment := cmdparse.ExtractComment(cmd); comment != "" {
						result.Reason = result.Reason + " (note: " + comment + ")"
					}
				}
			}
			fmt.Fprintf(os.Stderr, "claude-extended-tool-approver: %s -> %s: %s\n",
				rule.Name(), result.Decision, result.Reason)
			result.Trace = trace
			return result
		}
	}

	result := hookio.RuleResult{Decision: hookio.Abstain}
	if e.trace {
		result.Trace = trace
	}
	return result
}

// EvaluateHook is the single dispatch point for real hook decisions (Facade).
// Bash commands are routed through EvaluateExpression, which splits compounds
// and folds every leaf/redirection/process-substitution most-restrictive-wins;
// all other tools use the first-match-wins Evaluate (EvaluateExpression is
// Bash-only). All real-decision callers (the PreToolUse hook and offline replay)
// MUST go through this so baselines match the live hook.
func (e *Engine) EvaluateHook(input *hookio.HookInput) hookio.RuleResult {
	if input.ToolName == "Bash" {
		if cmd, err := input.BashCommand(); err == nil {
			return e.EvaluateExpression(cmd, nil, input)
		}
	}
	return e.Evaluate(input)
}

func (e *Engine) EvaluateExpression(expr string, stack []hookio.StackFrame, origin *hookio.HookInput) hookio.RuleResult {
	normalized := normalizeExpression(expr)
	// Check for cycle: has this exact expression been evaluated before?
	for _, frame := range stack {
		if frame.Expression == normalized {
			return hookio.RuleResult{
				Decision: hookio.Abstain,
				Reason:   "recursive evaluation: cycle detected (command repeated in stack)",
				Module:   "engine",
			}
		}
	}

	// Strip comments line by line
	lines := strings.Split(expr, "\n")
	for i, line := range lines {
		lines[i] = cmdparse.StripComment(line)
	}
	cleaned := strings.Join(lines, "\n")

	// Parse into sub-commands
	parsed := cmdparse.Parse(cleaned)
	if len(parsed) == 0 {
		return hookio.RuleResult{Decision: hookio.Abstain, Module: "engine"}
	}

	// Evaluate each sub-command, track most restrictive.
	// Seed with Approve — the least-restrictive identity for the fold: an
	// expression is Approve iff EVERY leaf independently approves.
	mostRestrictive := hookio.RuleResult{Decision: hookio.Approve, Reason: "all sub-commands approved", Module: "engine"}

	for _, pc := range parsed {
		if pc.Executable == "" {
			// Command-less leaf: no executable, but it may carry redirections or a
			// heredoc (e.g. the trailing "> /etc/passwd" of a subshell) that MUST
			// still be evaluated — otherwise a write to a protected path is
			// silently approved.
			if pc.HasHeredoc {
				return hookio.RuleResult{Decision: hookio.Abstain, Reason: "recursive evaluation: heredoc detected", Module: "engine"}
			}
			redirResult := e.evaluateRedirections(pc.Redirections, origin.PathEval)
			if redirResult.Decision > mostRestrictive.Decision {
				mostRestrictive = redirResult
			}
			continue
		}

		// Heredoc detected — Abstain
		if pc.HasHeredoc {
			return hookio.RuleResult{Decision: hookio.Abstain, Reason: "recursive evaluation: heredoc detected", Module: "engine"}
		}

		// Build synthetic HookInput
		syntheticInput := &hookio.HookInput{
			SessionID:      origin.SessionID,
			CWD:            origin.CWD,
			ToolName:       "Bash",
			ToolInput:      mustBashJSON(pc.Raw),
			PermissionMode: origin.PermissionMode,
			HookEventName:  origin.HookEventName,
			PathEval:       origin.PathEval,
		}

		// Evaluate through rule chain
		cmdResult := e.Evaluate(syntheticInput)

		// Command-substitution guard: an unresolved, non-safe $(...) / backtick in
		// the executable or ANY arg runs an inner command the leaf's own rule never
		// sees (e.g. `echo $(rm -rf ~)` — echo is "always safe" and approves). Demote
		// such a leaf to at least Abstain. $(date)/$(mktemp) stay approved.
		if hasUnsafeSubstitution(pc) && hookio.Abstain > cmdResult.Decision {
			cmdResult = hookio.RuleResult{Decision: hookio.Abstain, Reason: "unresolved command substitution runs an unevaluated inner command", Module: "engine"}
		}

		// Evaluate I/O redirections. With the restrictiveness ordering
		// (Approve < Abstain < Ask < Reject) a plain most-restrictive-wins
		// comparison correctly lets an unknown redirection path (Abstain) demote
		// an otherwise-approved command — no special case needed.
		redirResult := e.evaluateRedirections(pc.Redirections, origin.PathEval)
		if redirResult.Decision > cmdResult.Decision {
			cmdResult = redirResult
		}

		// Evaluate process substitutions recursively
		for _, psub := range pc.ProcessSubstitutions {
			psubStack := append(stack, hookio.StackFrame{RuleName: "engine", Command: "process-substitution", Expression: normalized})
			psubResult := e.EvaluateExpression(psub, psubStack, origin)
			if psubResult.Decision > cmdResult.Decision {
				cmdResult = psubResult
			}
		}

		// Track most restrictive
		if cmdResult.Decision > mostRestrictive.Decision {
			mostRestrictive = cmdResult
		}
	}

	return mostRestrictive
}

func normalizeExpression(expr string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(expr)), " ")
}

// hasUnsafeSubstitution reports whether a parsed leaf's executable or any arg
// embeds an unresolved, non-safe command substitution ($(...) / backtick).
func hasUnsafeSubstitution(pc cmdparse.ParsedCommand) bool {
	if cmdparse.HasUnsafeCommandSubstitution(pc.Executable) {
		return true
	}
	for _, a := range pc.Args {
		if cmdparse.HasUnsafeCommandSubstitution(a) {
			return true
		}
	}
	return false
}

func mustBashJSON(cmd string) json.RawMessage {
	b, _ := json.Marshal(hookio.BashToolInput{Command: cmd})
	return b
}

func (e *Engine) evaluateRedirections(redirs []hookio.Redirection, override *patheval.PathEvaluator) hookio.RuleResult {
	// No redirections = no opinion (neutral)
	if len(redirs) == 0 {
		return hookio.RuleResult{Decision: hookio.Approve, Reason: "no redirections to evaluate", Module: "engine"}
	}
	pe := e.pathEval
	if override != nil {
		pe = override
	}
	// Redirections present but no path evaluator
	if pe == nil {
		return hookio.RuleResult{Decision: hookio.Abstain, Module: "engine"}
	}
	for _, r := range redirs {
		// Standard special device files are always-safe redirect targets; skip
		// them before consulting the PathEvaluator, which would otherwise report
		// PathUnknown and wrongly demote/reject the command (pg2-9ctmb).
		if isSafeRedirectTarget(r.Path) {
			continue
		}
		access := pe.Evaluate(r.Path)
		switch r.Kind {
		case hookio.RedirectStdin:
			if !access.CanRead() {
				return hookio.RuleResult{Decision: hookio.Abstain, Reason: "redirection: stdin from non-readable path " + r.Path, Module: "engine"}
			}
		default:
			if access == patheval.PathReadOnly {
				return hookio.RuleResult{Decision: hookio.Reject, Reason: "redirection: write to read-only path " + r.Path, Module: "engine"}
			}
			if !access.CanWrite() {
				return hookio.RuleResult{Decision: hookio.Abstain, Reason: "redirection: write to non-writable path " + r.Path, Module: "engine"}
			}
		}
	}
	return hookio.RuleResult{Decision: hookio.Approve, Reason: "redirections: all paths safe", Module: "engine"}
}
