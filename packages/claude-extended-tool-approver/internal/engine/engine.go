package engine

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmdparse"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/patheval"
)

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

	// Evaluate each sub-command, track most restrictive
	mostRestrictive := hookio.RuleResult{Decision: hookio.Abstain, Module: "engine"}

	for _, pc := range parsed {
		if pc.Executable == "" {
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

		// Evaluate I/O redirections
		redirResult := e.evaluateRedirections(pc.Redirections, origin.PathEval)
		if redirResult.Decision > cmdResult.Decision {
			cmdResult = redirResult
		} else if redirResult.Decision == hookio.Abstain && cmdResult.Decision == hookio.Approve {
			// Unknown redirection path demotes an approval to abstain
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
