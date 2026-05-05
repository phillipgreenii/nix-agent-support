package pathsafety

import (
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/patheval"
)

var fileTools = map[string]bool{
	"Read": true, "Write": true, "Edit": true, "MultiEdit": true, "Delete": true,
}

var searchTools = map[string]bool{
	"Glob": true, "Grep": true,
}

type Rule struct {
	eval *patheval.PathEvaluator
}

func New(eval *patheval.PathEvaluator) *Rule {
	return &Rule{eval: eval}
}

func (r *Rule) Name() string {
	return "path-safety"
}

func (r *Rule) Evaluate(input *hookio.HookInput) hookio.RuleResult {
	if fileTools[input.ToolName] {
		path, err := input.FilePath()
		if err != nil {
			return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
		}
		switch input.ToolName {
		case "Read":
			if r.eval.IsDenyRead(path) {
				return hookio.RuleResult{Decision: hookio.Reject, Reason: "path is deny-read: " + path, Module: r.Name()}
			}
			access := r.eval.Evaluate(path)
			if access.CanRead() {
				return hookio.RuleResult{Decision: hookio.Approve, Reason: "path allows read: " + path, Module: r.Name()}
			}
			return hookio.RuleResult{
				Decision: hookio.Abstain,
				Reason:   "path is " + access.String() + " " + path + " (deferred to claude-code)",
				Module:   r.Name(),
			}
		case "Write", "Edit", "MultiEdit", "Delete":
			if r.eval.IsDenyWrite(path) {
				return hookio.RuleResult{Decision: hookio.Reject, Reason: "path is deny-write: " + path, Module: r.Name()}
			}
			access := r.eval.Evaluate(path)
			if access.CanWrite() {
				return hookio.RuleResult{Decision: hookio.Approve, Reason: "path allows write: " + path, Module: r.Name()}
			}
			return hookio.RuleResult{Decision: hookio.Abstain, Reason: "path access unknown: " + path + " (deferred to claude-code)", Module: r.Name()}
		default:
			return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
		}
	} else if searchTools[input.ToolName] {
		return r.evaluateSearch(input)
	}
	return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
}

func (r *Rule) evaluateSearch(input *hookio.HookInput) hookio.RuleResult {
	path, err := input.SearchPath()
	if err != nil {
		return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
	}
	if path == "" {
		return hookio.RuleResult{
			Decision: hookio.Approve,
			Reason:   "search tool with no explicit path (defaults to CWD)",
			Module:   r.Name(),
		}
	}
	if r.eval.IsDenyRead(path) {
		return hookio.RuleResult{
			Decision: hookio.Reject,
			Reason:   "search path is deny-read: " + path,
			Module:   r.Name(),
		}
	}
	access := r.eval.Evaluate(path)
	if access.CanRead() {
		return hookio.RuleResult{
			Decision: hookio.Approve,
			Reason:   "search path allows read: " + path,
			Module:   r.Name(),
		}
	}
	return hookio.RuleResult{
		Decision: hookio.Abstain,
		Reason:   "search path is " + access.String() + " " + path + " (deferred to claude-code)",
		Module:   r.Name(),
	}
}
